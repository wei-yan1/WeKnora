package plugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/pkg/pluginapi"
	pluginproto "github.com/Tencent/WeKnora/pkg/pluginapi/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DockerRuntime is the server-side secure runtime for the P0 no-network
// acceptance path. It talks to the plugin over a mounted Unix socket, so the
// plugin container needs no network namespace at all. The main application
// does not need Docker socket access through this type; production deployments
// should invoke it from a constrained runtime-agent when Docker privileges are
// separated from the API process.
//
// DockerRuntime depends only on the shared control-plane client
// (pluginapi.PluginControlClient), never on type-specific protocol clients.
type DockerRuntime struct {
	Manifest     Manifest
	Image        string
	DockerBinary string
	SocketDir    string

	mu            sync.RWMutex
	cmd           *exec.Cmd
	conn          *grpc.ClientConn
	controlClient pluginapi.PluginControlClient
	containerName string
	ownsSocketDir bool
	AuditSink     AuditSink
}

func NewDockerRuntime(manifest Manifest, image string) *DockerRuntime {
	return &DockerRuntime{Manifest: manifest, Image: image, DockerBinary: "docker", AuditSink: LoggerAuditSink{}}
}

func (r *DockerRuntime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.cmd != nil && r.clientReadyLocked() {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	if r.Image == "" {
		return fmt.Errorf("plugin %q has no OCI image", r.Manifest.ID)
	}
	if r.Manifest.EffectiveNetworkPolicy() != NetworkNone {
		return fmt.Errorf("docker runtime currently requires network policy none; controlled egress belongs in the runtime agent")
	}
	docker := r.DockerBinary
	if docker == "" {
		docker = "docker"
	}
	dir := r.SocketDir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "weknora-plugin-")
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.SocketDir = dir
		r.ownsSocketDir = true
		r.mu.Unlock()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	hostSocket := filepath.Join(dir, "plugin.sock")
	containerSocket := "/run/weknora/plugin.sock"
	containerName := r.pluginContainerName()
	args := r.dockerArgs(dir, containerSocket, containerName)
	cmd := exec.Command(docker, args...)
	var stderr io.ReadCloser
	if r.AuditSink != nil {
		p, perr := cmd.StderrPipe()
		if perr != nil {
			return fmt.Errorf("capture docker plugin stderr: %w", perr)
		}
		stderr = p
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start docker plugin: %w", err)
	}
	if stderr != nil {
		go ConsumePluginStderr(stderr, r.Manifest.ID, r.AuditSink)
	}
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dialer := func(dialCtx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(dialCtx, "unix", hostSocket)
	}
	conn, err := grpc.DialContext(connectCtx, "unix://"+hostSocket, grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("connect docker plugin: %w", err)
	}
	controlClient := pluginapi.NewPluginControlClient(conn)
	handshakeWire, err := controlClient.Handshake(connectCtx, &pluginproto.HandshakeRequest{})
	if err != nil {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("docker plugin handshake: %w", err)
	}
	if _, err := validateHandshake(r.Manifest, handshakeWire); err != nil {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	r.mu.Lock()
	r.cmd, r.conn, r.controlClient, r.containerName = cmd, conn, controlClient, containerName
	r.mu.Unlock()
	return nil
}

// pluginContainerName derives the deterministic container name shared between
// docker run (Start) and docker stop (Stop).
func (r *DockerRuntime) pluginContainerName() string {
	return "weknora-plugin-" + strings.NewReplacer(".", "-", "_", "-").Replace(r.Manifest.ID)
}

func (r *DockerRuntime) dockerArgs(dir, containerSocket, containerName string) []string {
	return []string{"run", "--rm", "--name", containerName, "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "512m", "-v", dir + ":/run/weknora:rw", "-e", "WEKNORA_PLUGIN_ADDR=unix://" + containerSocket, "-e", "WEKNORA_PLUGIN_ID=" + r.Manifest.ID, "-e", "WEKNORA_PLUGIN_PROTOCOL_VERSION=" + r.Manifest.ProtocolVersion, "-e", "WEKNORA_PLUGIN_NETWORK_POLICY=" + string(r.Manifest.EffectiveNetworkPolicy()), "-e", "WEKNORA_PLUGIN_NETWORK_ALLOWLIST=" + strings.Join(r.Manifest.Permissions.AllowedDestinations, ","), r.Image}
}

// dockerStopGrace is the container-level SIGTERM window granted by
// `docker stop -t` before Docker force-kills the container.
const dockerStopGrace = 5 * time.Second

// Stop cancels in-flight calls, then stops the container through Docker's own
// lifecycle: SIGTERM inside the container, the grace window, then SIGKILL —
// the same cancel-then-force semantics as the process runtime. Signaling or
// killing the docker CLI child process instead would orphan a running
// container, so the CLI is only killed as a fallback when the container-level
// stop fails.
func (r *DockerRuntime) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	cmd, conn, dir, ownsDir, container := r.cmd, r.conn, r.SocketDir, r.ownsSocketDir, r.containerName
	r.cmd, r.conn, r.controlClient = nil, nil, nil
	r.ownsSocketDir = false
	r.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if container != "" {
		docker := r.DockerBinary
		if docker == "" {
			docker = "docker"
		}
		stopCtx, cancel := context.WithTimeout(ctx, dockerStopGrace+10*time.Second)
		defer cancel()
		graceSeconds := fmt.Sprintf("%d", int(dockerStopGrace.Seconds()))
		stop := exec.CommandContext(stopCtx, docker, "stop", "-t", graceSeconds, container)
		if err := stop.Run(); err != nil && cmd != nil && cmd.Process != nil {
			// Container-level stop failed; kill the CLI process so Stop never
			// hangs on a wedged docker invocation.
			_ = cmd.Process.Kill()
		}
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Wait()
	}
	if dir == "" || !ownsDir {
		return nil
	}
	return os.RemoveAll(dir)
}

func (r *DockerRuntime) Health(ctx context.Context) HealthStatus {
	r.mu.RLock()
	controlClient := r.controlClient
	r.mu.RUnlock()
	if controlClient == nil {
		return HealthStatus{State: StateStopped, CheckedAt: time.Now().UTC()}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	wire, err := controlClient.Health(checkCtx, &pluginproto.HealthRequest{})
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

// Conn exposes the raw gRPC connection so protocol adapters can construct their
// own type-specific clients. The runtime keeps only the control-plane client.
func (r *DockerRuntime) Conn() *grpc.ClientConn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.conn
}

func (r *DockerRuntime) clientReadyLocked() bool {
	return r.controlClient != nil
}
