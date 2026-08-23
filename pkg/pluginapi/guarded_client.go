package pluginapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// AuditPrefix is the stderr line prefix that marks a structured audit event
// emitted by the plugin SDK. The host runtime scans the plugin stderr stream,
// parses lines carrying this prefix into audit events, and passes every other
// line through as ordinary plugin logs.
const AuditPrefix = "plugin_audit "

// NetworkPolicy mirrors the host manifest permissions.network field.
type NetworkPolicy string

const (
	NetworkNone      NetworkPolicy = "none"
	NetworkEgress    NetworkPolicy = "egress"
	NetworkAllowlist NetworkPolicy = "allowlist"
)

// pluginAuditEvent is the wire shape emitted to stderr by the SDK. It
// deliberately omits plugin_id: a plugin may forge its own identity, so the
// host binds the plugin_id from the manifest it manages rather than trusting
// anything the plugin writes.
type pluginAuditEvent struct {
	Action      string `json:"action"`
	Destination string `json:"destination"`
	Policy      string `json:"policy"`
	Reason      string `json:"reason"`
	Timestamp   string `json:"timestamp"`
}

// auditWriteMu keeps a single audit line atomic on the shared stderr stream.
var auditWriteMu sync.Mutex

// NewGuardedHTTPClient returns a controlled http.Client whose outbound dials
// are checked against the given network policy. Rejection is mandatory; audit
// output is best effort (a failure to write stderr never allows the request).
func NewGuardedHTTPClient(policy NetworkPolicy, allowlist []string) *http.Client {
	policy = normalizeNetworkPolicy(policy)
	guard := &networkGuard{policy: policy, allowlist: allowlist}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	baseDialer := &net.Dialer{Timeout: 10 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if err := guard.check(ctx, address); err != nil {
			return nil, err
		}
		return baseDialer.DialContext(ctx, network, address)
	}
	return &http.Client{Transport: transport}
}

type networkGuard struct {
	policy    NetworkPolicy
	allowlist []string
	resolver  func(context.Context, string) ([]net.IP, error)
}

func (g *networkGuard) check(ctx context.Context, address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	var reason string
	if isForbiddenHost(host) {
		reason = "destination is a loopback, metadata or internal host"
	}
	switch g.policy {
	case NetworkNone:
		reason = "outbound network is disabled"
	case NetworkAllowlist:
		if reason == "" && !matchesAllowlist(g.allowlist, host, port) {
			reason = "destination is not in allowlist"
		}
	case NetworkEgress:
		if reason == "" {
			if ip := net.ParseIP(host); ip != nil && isForbiddenIP(ip) {
				reason = "destination resolves to a forbidden address"
			}
		}
	}
	if reason == "" && (g.policy == NetworkEgress || g.policy == NetworkAllowlist) {
		resolver := g.resolver
		if resolver == nil {
			resolver = func(resolveCtx context.Context, resolveHost string) ([]net.IP, error) {
				return net.DefaultResolver.LookupIP(resolveCtx, "ip", resolveHost)
			}
		}
		addresses, resolveErr := resolver(ctx, host)
		if resolveErr != nil {
			reason = "DNS resolution failed"
		} else {
			for _, resolved := range addresses {
				if isForbiddenIP(resolved) {
					reason = "destination resolves to a forbidden address"
					break
				}
			}
		}
	}
	if reason == "" {
		return nil
	}
	g.emitAudit(address, reason)
	return fmt.Errorf("plugin network blocked: %s", reason)
}

// emitAudit is best effort: the request is rejected regardless of whether the
// audit line reaches stderr.
func (g *networkGuard) emitAudit(address, reason string) {
	event := pluginAuditEvent{
		Action:      "network_denied",
		Destination: address,
		Policy:      string(g.policy),
		Reason:      reason,
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	auditWriteMu.Lock()
	defer auditWriteMu.Unlock()
	_, _ = fmt.Fprintf(os.Stderr, "%s%s\n", AuditPrefix, line)
}

func matchesAllowlist(allowlist []string, host, port string) bool {
	host = strings.ToLower(host)
	for _, entry := range allowlist {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if entry == host || entry == net.JoinHostPort(host, port) {
			return true
		}
		if strings.HasPrefix(entry, "*.") && strings.HasSuffix(host, strings.TrimPrefix(entry, "*")) {
			return true
		}
	}
	return false
}

func isForbiddenIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func isForbiddenHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "localhost" || host == "metadata.google.internal" ||
		host == "169.254.169.254" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".internal")
}

// Environment variable names injected by the host runtime into the plugin
// process. The policy is decided by the host from the manifest, not hard-coded
// by the plugin, so a plugin cannot silently opt into networking it did not
// declare.
const (
	EnvNetworkPolicy    = "WEKNORA_PLUGIN_NETWORK_POLICY"
	EnvNetworkAllowlist = "WEKNORA_PLUGIN_NETWORK_ALLOWLIST"
)

// PolicyFromEnv reads the host-injected network policy. An empty value means
// the host did not inject one; callers should treat that as NetworkNone.
func PolicyFromEnv() NetworkPolicy {
	return normalizeNetworkPolicy(NetworkPolicy(strings.TrimSpace(os.Getenv(EnvNetworkPolicy))))
}

func normalizeNetworkPolicy(policy NetworkPolicy) NetworkPolicy {
	switch policy {
	case NetworkNone, NetworkEgress, NetworkAllowlist:
		return policy
	default:
		return NetworkNone
	}
}

// AllowlistFromEnv reads the host-injected allowlist (comma-separated hosts).
func AllowlistFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv(EnvNetworkAllowlist))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// NewPluginHTTPClient returns a guarded client configured from the policy the
// host injected via environment variables. Plugin business code should use
// this instead of hard-coding a policy or using http.DefaultClient.
func NewPluginHTTPClient() *http.Client {
	return NewGuardedHTTPClient(PolicyFromEnv(), AllowlistFromEnv())
}
