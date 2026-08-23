package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/pkg/pluginapi"
	pluginproto "github.com/Tencent/WeKnora/pkg/pluginapi/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ProcessRuntime is the development/desktop runtime. It deliberately has no
// claim of security isolation: use DockerRuntime or another constrained agent
// in a server deployment. Both runtimes implement the same lifecycle surface.
type ProcessRuntime struct {
	Manifest Manifest
	Command  string
	Args     []string
	Address  string

	mu              sync.RWMutex
	cmd             *exec.Cmd
	conn            *grpc.ClientConn
	client          pluginapi.DataSourcePluginClient
	parserClient    pluginapi.ParserPluginClient
	webSearchClient pluginapi.WebSearchPluginClient
	AuditSink       AuditSink
}

func NewProcessRuntime(manifest Manifest, command string, args ...string) *ProcessRuntime {
	return &ProcessRuntime{Manifest: manifest, Command: command, Args: append([]string(nil), args...), AuditSink: LoggerAuditSink{}}
}

func (r *ProcessRuntime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.cmd != nil && r.clientReadyLocked() {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	if r.Command == "" {
		return fmt.Errorf("plugin %q has no process command", r.Manifest.ID)
	}
	address := r.Address
	if address == "" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		address = listener.Addr().String()
		_ = listener.Close()
	}
	commandArgs := append([]string(nil), r.Args...)
	cmd := exec.Command(r.Command, commandArgs...)
	cmd.Env = append(os.Environ(),
		"WEKNORA_PLUGIN_ADDR="+address,
		"WEKNORA_PLUGIN_ID="+r.Manifest.ID,
		"WEKNORA_PLUGIN_PROTOCOL_VERSION="+r.Manifest.ProtocolVersion,
		"WEKNORA_PLUGIN_NETWORK_POLICY="+string(r.Manifest.EffectiveNetworkPolicy()),
		"WEKNORA_PLUGIN_NETWORK_ALLOWLIST="+strings.Join(r.Manifest.Permissions.AllowedDestinations, ","),
	)
	var stderr io.ReadCloser
	if r.AuditSink != nil {
		p, perr := cmd.StderrPipe()
		if perr != nil {
			return fmt.Errorf("capture plugin stderr: %w", perr)
		}
		stderr = p
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start plugin process: %w", err)
	}
	if stderr != nil {
		go ConsumePluginStderr(stderr, r.Manifest.ID, r.AuditSink)
	}
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(connectCtx, address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("connect plugin process: %w", err)
	}
	client := pluginapi.NewDataSourcePluginClient(conn)
	parserClient := pluginapi.NewParserPluginClient(conn)
	webSearchClient := pluginapi.NewWebSearchPluginClient(conn)
	var handshake pluginapi.HandshakeResponse
	var handshakeWire *pluginproto.HandshakeResponse
	if r.Manifest.ExtensionType == ExtensionParser {
		handshakeWire, err = parserClient.Handshake(connectCtx, &pluginproto.HandshakeRequest{})
	} else if r.Manifest.ExtensionType == ExtensionSearch {
		handshakeWire, err = webSearchClient.Handshake(connectCtx, &pluginproto.HandshakeRequest{})
	} else {
		handshakeWire, err = client.Handshake(connectCtx, &pluginproto.HandshakeRequest{})
	}
	if err != nil {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("plugin handshake: %w", err)
	}
	if err := pluginapi.DecodeHandshake(handshakeWire, &handshake); err != nil {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	if handshake.PluginID != "" && handshake.PluginID != r.Manifest.ID {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("plugin handshake ID %q does not match manifest %q", handshake.PluginID, r.Manifest.ID)
	}
	if handshake.ProtocolVersion != "" && handshake.ProtocolVersion != r.Manifest.ProtocolVersion {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("plugin handshake protocol %q does not match manifest %q", handshake.ProtocolVersion, r.Manifest.ProtocolVersion)
	}
	if err := validateRuntimeCapabilities(r.Manifest, handshake.Capabilities); err != nil {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("plugin capabilities: %w", err)
	}
	r.mu.Lock()
	r.Address, r.cmd, r.conn, r.client, r.parserClient, r.webSearchClient = address, cmd, conn, client, parserClient, webSearchClient
	r.mu.Unlock()
	return nil
}

func (r *ProcessRuntime) Stop(context.Context) error {
	r.mu.Lock()
	cmd, conn := r.cmd, r.conn
	r.cmd, r.conn, r.client, r.parserClient, r.webSearchClient = nil, nil, nil, nil, nil
	r.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	_ = cmd.Wait()
	return nil
}

func (r *ProcessRuntime) Health(ctx context.Context) HealthStatus {
	r.mu.RLock()
	client := r.client
	parserClient := r.parserClient
	webSearchClient := r.webSearchClient
	r.mu.RUnlock()
	if client == nil && parserClient == nil && webSearchClient == nil {
		return HealthStatus{State: StateStopped, CheckedAt: time.Now().UTC()}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var wire *pluginproto.HealthResponse
	var err error
	if r.Manifest.ExtensionType == ExtensionParser {
		wire, err = parserClient.Health(checkCtx, &pluginproto.HealthRequest{})
	} else if r.Manifest.ExtensionType == ExtensionSearch {
		wire, err = webSearchClient.Health(checkCtx, &pluginproto.HealthRequest{})
	} else {
		wire, err = client.Health(checkCtx, &pluginproto.HealthRequest{})
	}
	if err != nil {
		return HealthStatus{State: StateUnhealthy, Message: err.Error(), CheckedAt: time.Now().UTC()}
	}
	var health pluginapi.HealthResponse
	if err := pluginapi.DecodeHealth(wire, &health); err != nil {
		return HealthStatus{State: StateUnhealthy, Message: err.Error(), CheckedAt: time.Now().UTC()}
	}
	state := HealthState(health.State)
	if state == "" {
		state = StateRunning
	}
	return HealthStatus{State: state, Message: health.Message, CheckedAt: time.Now().UTC()}
}

func (r *ProcessRuntime) Client() (pluginapi.DataSourcePluginClient, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.client, r.client != nil
}

func (r *ProcessRuntime) ParserClient() (pluginapi.ParserPluginClient, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.parserClient, r.parserClient != nil
}

func (r *ProcessRuntime) WebSearchClient() (pluginapi.WebSearchPluginClient, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.webSearchClient, r.webSearchClient != nil
}

func (r *ProcessRuntime) clientReadyLocked() bool {
	switch r.Manifest.ExtensionType {
	case ExtensionParser:
		return r.parserClient != nil
	case ExtensionSearch:
		return r.webSearchClient != nil
	default:
		return r.client != nil
	}
}

// ConnectorFactory returns an instance-aware resolver factory. Start the
// runtime through Manager before registering this factory, or use lazyStart to
// make a desktop installation self-starting.
func (r *ProcessRuntime) ConnectorFactory(connectorType string, lazyStart bool) datasource.ConnectorFactory {
	return func(ctx context.Context, scope datasource.ConnectorScope, config *types.DataSourceConfig) (datasource.ConnectorLease, error) {
		if lazyStart {
			if err := r.Start(ctx); err != nil {
				return nil, err
			}
		}
		client, ok := r.Client()
		if !ok {
			return nil, fmt.Errorf("plugin %q is not running", r.Manifest.ID)
		}
		return processConnectorLease{connector: &GRPCConnectorProxy{ConnectorType: connectorType, Client: client}}, nil
	}
}

type processConnectorLease struct {
	connector  datasource.Connector
	release    func()
	generation uint64
}

func (l processConnectorLease) Connector() datasource.Connector { return l.connector }
func (l processConnectorLease) Close() error {
	if l.release != nil {
		l.release()
	}
	return nil
}
func (l processConnectorLease) Generation() uint64 { return l.generation }
