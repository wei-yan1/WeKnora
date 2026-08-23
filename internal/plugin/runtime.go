package plugin

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Runtime is the small common lifecycle surface shared by built-in adapters
// and external process/container runtimes. The manager owns ordering and state;
// the runtime owns the implementation-specific start/stop mechanics.
type Runtime interface {
	Start(context.Context) error
	Stop(context.Context) error
	Health(context.Context) HealthStatus
}

type CapabilityReporter interface {
	Capabilities() []string
}

func validateRuntimeCapabilities(manifest Manifest, actual []string) error {
	declared := make(map[string]struct{}, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		declared[capability] = struct{}{}
	}
	seen := make(map[string]struct{}, len(actual))
	for _, capability := range actual {
		if _, ok := declared[capability]; !ok {
			return fmt.Errorf("runtime capability %q is not declared", capability)
		}
		seen[capability] = struct{}{}
	}
	for _, capability := range manifest.Capabilities {
		if _, ok := seen[capability]; !ok {
			return fmt.Errorf("manifest capability %q was not confirmed by runtime", capability)
		}
	}
	return nil
}

type BuiltinRuntime struct {
	StartFunc        func(context.Context) error
	StopFunc         func(context.Context) error
	HealthFunc       func(context.Context) HealthStatus
	CapabilitiesList []string
}

func (r *BuiltinRuntime) Start(ctx context.Context) error {
	if r.StartFunc != nil {
		return r.StartFunc(ctx)
	}
	return nil
}
func (r *BuiltinRuntime) Stop(ctx context.Context) error {
	if r.StopFunc != nil {
		return r.StopFunc(ctx)
	}
	return nil
}
func (r *BuiltinRuntime) Health(ctx context.Context) HealthStatus {
	if r.HealthFunc != nil {
		return r.HealthFunc(ctx)
	}
	return HealthStatus{State: StateRunning, CheckedAt: time.Now().UTC()}
}
func (r *BuiltinRuntime) Capabilities() []string { return append([]string(nil), r.CapabilitiesList...) }

type pluginEntry struct {
	manifest       Manifest
	runtime        Runtime
	state          HealthStatus
	generation     uint64
	started        bool
	healthFailures int
	restartCount   int
	admission      *admissionController
	lifecycle      sync.Mutex
}

type Manager struct {
	mu               sync.RWMutex
	hostVersion      string
	entries          map[string]*pluginEntry
	supervisorMu     sync.Mutex
	supervisorCancel context.CancelFunc
	supervisorWG     sync.WaitGroup
	admissionConfig  AdmissionConfig
}

// HealthSupervisorConfig controls active health monitoring. Monitoring is
// opt-in so existing embedders retain the previous on-demand behavior.
type HealthSupervisorConfig struct {
	Interval         time.Duration
	FailureThreshold int
	RestartEnabled   bool
	MaxRestartCount  int
}

const (
	defaultHealthInterval    = 30 * time.Second
	defaultFailureThreshold  = 3
	defaultCancellationGrace = 5 * time.Second
)

func NewManager(hostVersion string) *Manager {
	return &Manager{hostVersion: hostVersion, entries: make(map[string]*pluginEntry), admissionConfig: defaultAdmissionConfig()}
}

func NewManagerWithAdmission(hostVersion string, config AdmissionConfig) *Manager {
	return &Manager{hostVersion: hostVersion, entries: make(map[string]*pluginEntry), admissionConfig: config}
}

type InvocationLease struct {
	Context    context.Context
	Generation uint64
	release    func()
	once       sync.Once
}

func (l *InvocationLease) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.release != nil {
			l.release()
		}
	})
}

// AcquireInvocation admits one business call and pins the current runtime
// generation until release. Restart/stop enters draining and waits for these
// leases before terminating the shared process.
func (m *Manager) AcquireInvocation(ctx context.Context, id string) (*InvocationLease, error) {
	m.mu.RLock()
	entry, ok := m.entries[id]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("plugin %q not registered", id)
	}
	if !entry.started {
		m.mu.RUnlock()
		return nil, fmt.Errorf("plugin %q is not running", id)
	}
	generation, admission := entry.generation, entry.admission
	m.mu.RUnlock()
	callCtx, release, err := admission.acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &InvocationLease{Context: callCtx, Generation: generation, release: release}, nil
}

func (m *Manager) GenerationValid(id string, generation uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.entries[id]
	return ok && entry.started && entry.generation == generation
}

func (m *Manager) Admission(id string) (AdmissionStatus, bool) {
	m.mu.RLock()
	entry, ok := m.entries[id]
	if !ok {
		m.mu.RUnlock()
		return AdmissionStatus{}, false
	}
	admission := entry.admission
	m.mu.RUnlock()
	return admission.status(), true
}

// StartHealthSupervisor periodically probes every started plugin. It updates
// the same state returned by Health(), marks repeatedly failing plugins
// unhealthy, and optionally performs bounded restarts with a fresh lifecycle
// generation. Calling it twice is idempotent.
func (m *Manager) StartHealthSupervisor(parent context.Context, cfg HealthSupervisorConfig) error {
	if parent == nil {
		return fmt.Errorf("health supervisor context is nil")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultHealthInterval
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = defaultFailureThreshold
	}
	if cfg.RestartEnabled && cfg.MaxRestartCount <= 0 {
		return fmt.Errorf("max restart count must be positive when restart is enabled")
	}
	m.supervisorMu.Lock()
	defer m.supervisorMu.Unlock()
	if m.supervisorCancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	m.supervisorCancel = cancel
	m.supervisorWG.Add(1)
	go m.healthSupervisorLoop(ctx, cfg)
	return nil
}

func (m *Manager) StopHealthSupervisor() {
	m.supervisorMu.Lock()
	cancel := m.supervisorCancel
	m.supervisorCancel = nil
	m.supervisorMu.Unlock()
	if cancel != nil {
		cancel()
		m.supervisorWG.Wait()
	}
}

func (m *Manager) healthSupervisorLoop(ctx context.Context, cfg HealthSupervisorConfig) {
	defer m.supervisorWG.Done()
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, info := range m.List() {
				m.superviseOne(ctx, info.Manifest.ID, cfg)
			}
		}
	}
}

func (m *Manager) superviseOne(ctx context.Context, id string, cfg HealthSupervisorConfig) {
	m.mu.RLock()
	entry, ok := m.entries[id]
	if !ok || !entry.started {
		m.mu.RUnlock()
		return
	}
	runtime := entry.runtime
	generation := entry.generation
	m.mu.RUnlock()

	health := runtime.Health(ctx)
	if health.State == "" {
		health.State = StateRunning
	}
	health.Generation = generation
	health.CheckedAt = time.Now().UTC()

	m.mu.Lock()
	entry, ok = m.entries[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	if !entry.started || entry.generation != generation {
		m.mu.Unlock()
		return
	}
	if entry.admission.status().Draining {
		m.mu.Unlock()
		return
	}
	if health.State == StateRunning {
		entry.healthFailures = 0
		entry.restartCount = 0
	} else {
		entry.healthFailures++
		if entry.healthFailures >= cfg.FailureThreshold && health.State != StateFailed {
			health.State = StateUnhealthy
		}
	}
	entry.state = health
	shouldRestart := cfg.RestartEnabled && entry.healthFailures >= cfg.FailureThreshold && entry.restartCount < cfg.MaxRestartCount
	if shouldRestart {
		entry.restartCount++
	}
	m.mu.Unlock()

	if shouldRestart {
		m.restartOne(ctx, id, runtime, generation)
	}
}

func (m *Manager) restartOne(ctx context.Context, id string, runtime Runtime, expectedGeneration uint64) {
	m.mu.RLock()
	entry, ok := m.entries[id]
	if !ok {
		m.mu.RUnlock()
		return
	}
	admission := entry.admission
	drainTimeout := admission.config.DrainTimeout
	m.mu.RUnlock()
	entry.lifecycle.Lock()
	defer entry.lifecycle.Unlock()
	m.mu.RLock()
	current, ok := m.entries[id]
	if !ok || current != entry || !entry.started || entry.generation != expectedGeneration {
		m.mu.RUnlock()
		return
	}
	m.mu.RUnlock()
	m.mu.Lock()
	entry.state = HealthStatus{State: StateDraining, CheckedAt: time.Now().UTC(), Generation: entry.generation}
	m.mu.Unlock()
	drainAdmission(ctx, admission, drainTimeout)
	stopCtx, cancelStop := context.WithTimeout(ctx, 15*time.Second)
	stopErr := runtime.Stop(stopCtx)
	cancelStop()
	if stopErr != nil {
		m.mu.Lock()
		if entry, ok := m.entries[id]; ok {
			entry.state = HealthStatus{State: StateFailed, Message: "restart stop failed: " + stopErr.Error(), CheckedAt: time.Now().UTC(), Generation: entry.generation}
		}
		m.mu.Unlock()
		return
	}
	postStopCtx, cancelPostStop := context.WithTimeout(ctx, defaultCancellationGrace)
	postStopErr := admission.waitDrained(postStopCtx)
	cancelPostStop()
	if postStopErr != nil {
		m.mu.Lock()
		entry.started = false
		entry.state = HealthStatus{State: StateFailed, Message: "old plugin calls did not terminate after stop: " + postStopErr.Error(), CheckedAt: time.Now().UTC(), Generation: entry.generation}
		m.mu.Unlock()
		return
	}
	startCtx, cancelStart := context.WithTimeout(ctx, 30*time.Second)
	startErr := runtime.Start(startCtx)
	cancelStart()
	if startErr != nil {
		m.mu.Lock()
		if entry, ok := m.entries[id]; ok {
			entry.started = false
			entry.state = HealthStatus{State: StateFailed, Message: "restart start failed: " + startErr.Error(), CheckedAt: time.Now().UTC(), Generation: entry.generation}
		}
		m.mu.Unlock()
		return
	}
	health := runtime.Health(ctx)
	if health.State == "" {
		health.State = StateRunning
	}
	m.mu.Lock()
	if entry, ok := m.entries[id]; ok {
		entry.started = true
		entry.generation++
		entry.healthFailures = 0
		health.Generation = entry.generation
		health.CheckedAt = time.Now().UTC()
		entry.state = health
	}
	m.mu.Unlock()
	admission.resume()
}

func (m *Manager) Register(manifest Manifest, runtime Runtime) error {
	if runtime == nil {
		return fmt.Errorf("%w: runtime is nil", ErrManifestInvalid)
	}
	if err := manifest.Validate(m.hostVersion); err != nil {
		return err
	}
	if reporter, ok := runtime.(CapabilityReporter); ok {
		declared := make(map[string]struct{}, len(manifest.Capabilities))
		for _, capability := range manifest.Capabilities {
			declared[capability] = struct{}{}
		}
		for _, capability := range reporter.Capabilities() {
			if _, ok := declared[capability]; !ok {
				return fmt.Errorf("%w: runtime capability %q is not declared", ErrManifestInvalid, capability)
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[manifest.ID]; exists {
		return fmt.Errorf("plugin %q already registered", manifest.ID)
	}
	m.entries[manifest.ID] = &pluginEntry{manifest: manifest, runtime: runtime, state: HealthStatus{State: StateDiscovered}, admission: newAdmissionController(m.admissionConfig)}
	return nil
}

func (m *Manager) Start(ctx context.Context, id string) error {
	m.mu.RLock()
	entry, ok := m.entries[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("plugin %q not registered", id)
	}
	m.mu.RUnlock()
	entry.lifecycle.Lock()
	defer entry.lifecycle.Unlock()
	m.mu.Lock()
	if entry.started {
		status := entry.admission.status()
		if status.Draining {
			m.mu.Unlock()
			return fmt.Errorf("plugin %q cannot start while previous runtime is draining", id)
		}
		m.mu.Unlock()
		return nil
	}
	if status := entry.admission.status(); status.Active > 0 {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q cannot start while %d old invocation(s) remain", id, status.Active)
	}
	entry.state.State = StateStarting
	entry.state.CheckedAt = time.Now().UTC()
	runtime := entry.runtime
	m.mu.Unlock()
	if err := runtime.Start(ctx); err != nil {
		m.mu.Lock()
		entry.state = HealthStatus{State: StateFailed, Message: err.Error(), CheckedAt: time.Now().UTC(), Generation: entry.generation}
		m.mu.Unlock()
		return fmt.Errorf("start plugin %q: %w", id, err)
	}
	health := runtime.Health(ctx)
	if health.State == "" {
		health.State = StateRunning
	}
	m.mu.Lock()
	entry.started = true
	entry.generation++
	health.Generation = entry.generation
	health.CheckedAt = time.Now().UTC()
	entry.state = health
	m.mu.Unlock()
	entry.admission.resume()
	return nil
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	m.mu.RLock()
	entry, ok := m.entries[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("plugin %q not registered", id)
	}
	m.mu.RUnlock()
	entry.lifecycle.Lock()
	defer entry.lifecycle.Unlock()
	m.mu.Lock()
	if !entry.started {
		entry.state.State = StateStopped
		m.mu.Unlock()
		return nil
	}
	runtime := entry.runtime
	admission := entry.admission
	entry.state = HealthStatus{State: StateDraining, CheckedAt: time.Now().UTC(), Generation: entry.generation}
	m.mu.Unlock()
	drainAdmission(ctx, admission, admission.config.DrainTimeout)
	if err := runtime.Stop(ctx); err != nil {
		m.mu.Lock()
		entry.state = HealthStatus{State: StateFailed, Message: err.Error(), CheckedAt: time.Now().UTC(), Generation: entry.generation}
		m.mu.Unlock()
		return fmt.Errorf("stop plugin %q: %w", id, err)
	}
	postStopCtx, cancelPostStop := context.WithTimeout(context.WithoutCancel(ctx), defaultCancellationGrace)
	postStopErr := admission.waitDrained(postStopCtx)
	cancelPostStop()
	if postStopErr != nil {
		m.mu.Lock()
		entry.started = false
		entry.state = HealthStatus{State: StateFailed, Message: "old plugin calls did not terminate after stop: " + postStopErr.Error(), CheckedAt: time.Now().UTC(), Generation: entry.generation}
		m.mu.Unlock()
		return fmt.Errorf("stop plugin %q: %w", id, postStopErr)
	}
	m.mu.Lock()
	entry.started = false
	entry.state = HealthStatus{State: StateStopped, CheckedAt: time.Now().UTC(), Generation: entry.generation}
	m.mu.Unlock()
	return nil
}

func drainAdmission(ctx context.Context, admission *admissionController, timeout time.Duration) {
	admission.beginDrain()
	drainCtx, cancelDrain := context.WithTimeout(ctx, timeout)
	err := admission.waitDrained(drainCtx)
	cancelDrain()
	if err != nil {
		admission.cancelActive(ErrPluginDrainTimeout)
	}
}

func (m *Manager) Health(ctx context.Context, id string) (HealthStatus, error) {
	m.mu.RLock()
	entry, ok := m.entries[id]
	if !ok {
		m.mu.RUnlock()
		return HealthStatus{}, fmt.Errorf("plugin %q not registered", id)
	}
	runtime := entry.runtime
	started := entry.started
	generation := entry.generation
	current := entry.state
	m.mu.RUnlock()
	if !started {
		return current, nil
	}
	health := runtime.Health(ctx)
	if health.State == "" {
		health.State = StateRunning
	}
	health.Generation = generation
	health.CheckedAt = time.Now().UTC()
	m.mu.Lock()
	entry.state = health
	m.mu.Unlock()
	return health, nil
}

func (m *Manager) Get(id string) (PluginInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.entries[id]
	if !ok {
		return PluginInfo{}, false
	}
	return PluginInfo{Manifest: entry.manifest, State: entry.state}, true
}

// Unregister removes a stopped or failed plugin from the control plane. It is
// used when a multi-plugin discovery pass fails part way through, so a later
// retry does not inherit stale registrations.
func (m *Manager) Unregister(ctx context.Context, id string) error {
	if err := m.Stop(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.entries, id)
	m.mu.Unlock()
	return nil
}

func (m *Manager) List() []PluginInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]PluginInfo, 0, len(m.entries))
	for _, entry := range m.entries {
		result = append(result, PluginInfo{Manifest: entry.manifest, State: entry.state})
	}
	return result
}
