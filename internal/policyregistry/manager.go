package policyregistry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

const defaultPollInterval = time.Minute

var ErrNoActivePolicy = errors.New("no active router policy snapshot")

type Loader interface {
	RootURI() string
	ReadHead(context.Context, Environment, Lane) (HeadSnapshot, error)
	ReadRelease(context.Context, ObjectRef) (Release, error)
	ReadPolicy(context.Context, ObjectRef) (*rosterdata.Roster, error)
}

type Candidate struct {
	AuxiliaryModels           map[string]ObjectRef
	HeadSnapshot              HeadSnapshot
	Release                   Release
	ClassifierReleaseV5       *ClassifierReleaseManifestV1
	ClassifierReleaseID       string
	ClassifierSelectionID     string
	ClassifierWorkerURL       string
	ClassifierWorkerAudience  string
	ClassifierEnabledVariants []policy.ClassifierVariantID
	SupportedAPIMajors        []policy.ClassifierAPIMajor
	Policy                    *rosterdata.Roster
	StrategyPolicies          map[router.Strategy]StrategyPolicy
	ClassifierAudience        string
}

type Snapshot struct {
	Candidate
	Routers map[router.Strategy]router.Router
}

type Builder func(context.Context, Candidate) (map[router.Strategy]router.Router, error)

type ManagerStatus struct {
	Environment              Environment `json:"environment"`
	Lane                     Lane        `json:"lane"`
	ActiveHeadGeneration     int64       `json:"active_head_generation"`
	ActiveReleaseID          string      `json:"active_release_id,omitempty"`
	ActivePolicySHA256       string      `json:"active_policy_sha256,omitempty"`
	LatestObservedGeneration int64       `json:"latest_observed_generation"`
	RejectedGeneration       int64       `json:"rejected_generation,omitempty"`
	LastSuccessfulRefresh    time.Time   `json:"last_successful_refresh,omitempty"`
	LastRejectionReason      string      `json:"last_rejection_reason,omitempty"`
	Polling                  bool        `json:"polling"`
}

type Manager struct {
	loader      Loader
	environment Environment
	lane        Lane
	builder     Builder
	logger      *slog.Logger
	pollEvery   time.Duration
	trigger     chan struct{}
	active      atomic.Pointer[Snapshot]
	refreshMu   sync.Mutex
	statusMu    sync.RWMutex
	status      ManagerStatus
}

func NewManager(loader Loader, environment Environment, lane Lane, builder Builder, logger *slog.Logger) (*Manager, error) {
	if loader == nil || builder == nil {
		return nil, errors.New("router policy manager requires a loader and builder")
	}
	if err := ValidateEnvironment(environment); err != nil {
		return nil, err
	}
	if err := ValidateLane(lane); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		loader: loader, environment: environment, lane: lane, builder: builder,
		logger: logger, pollEvery: defaultPollInterval, trigger: make(chan struct{}, 1),
		status: ManagerStatus{Environment: environment, Lane: lane},
	}, nil
}

func (m *Manager) Active() *Snapshot { return m.active.Load() }

func (m *Manager) Refresh(ctx context.Context) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()

	headSnapshot, err := m.loader.ReadHead(ctx, m.environment, m.lane)
	if err != nil {
		m.recordRejection(0, err)
		return err
	}
	m.recordObserved(headSnapshot.Generation)
	if current := m.active.Load(); current != nil && headSnapshot.Generation <= current.HeadSnapshot.Generation {
		return nil
	}

	releaseRef := ObjectRef{
		URI: headSnapshot.Head.ReleaseURI, SHA256: headSnapshot.Head.ReleaseSHA256,
		Generation: headSnapshot.Head.ReleaseGeneration,
	}
	release, err := m.loader.ReadRelease(ctx, releaseRef)
	if err != nil {
		m.recordRejection(headSnapshot.Generation, err)
		return fmt.Errorf("read policy release: %w", err)
	}
	policyRef := ObjectRef{URI: release.Policy.URI, SHA256: release.Policy.SHA256, Generation: release.Policy.Generation}
	selectionPolicy, err := m.loader.ReadPolicy(ctx, policyRef)
	if err != nil {
		m.recordRejection(headSnapshot.Generation, err)
		return fmt.Errorf("read selection policy: %w", err)
	}
	if !slices.Equal(selectionPolicy.ClassOrder, release.Classifier.ClassOrder) {
		err = errors.New("classifier class order does not match Go selection policy")
		m.recordRejection(headSnapshot.Generation, err)
		return err
	}
	candidate := Candidate{HeadSnapshot: headSnapshot, Release: release, Policy: selectionPolicy}
	routers, err := m.builder(ctx, candidate)
	if err != nil {
		m.recordRejection(headSnapshot.Generation, err)
		return fmt.Errorf("build policy runtime snapshot: %w", err)
	}
	if len(routers) == 0 {
		err = errors.New("policy runtime snapshot has no routers")
		m.recordRejection(headSnapshot.Generation, err)
		return err
	}
	if current := m.active.Load(); current != nil && headSnapshot.Generation <= current.HeadSnapshot.Generation {
		return nil
	}
	m.active.Store(&Snapshot{Candidate: candidate, Routers: routers})
	m.recordSuccess(headSnapshot.Generation, headSnapshot.Head.ReleaseSHA256, release.Policy.SHA256)
	m.logger.Info("Router policy snapshot activated",
		"environment", m.environment, "lane", m.lane, "head_generation", headSnapshot.Generation,
		"release_id_prefix", digestPrefix(headSnapshot.Head.ReleaseSHA256), "policy_sha256_prefix", digestPrefix(release.Policy.SHA256))
	return nil
}

func (m *Manager) Run(ctx context.Context) {
	m.setPolling(true)
	defer m.setPolling(false)
	for {
		delay := jitteredInterval(m.pollEvery)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-m.trigger:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := m.Refresh(refreshCtx); err != nil && ctx.Err() == nil {
			m.logger.Warn("Router policy refresh rejected; retaining last-known-good snapshot",
				"environment", m.environment, "lane", m.lane, "err", err)
		}
		cancel()
	}
}

func (m *Manager) Trigger() {
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}

func (m *Manager) Status() ManagerStatus {
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	return m.status
}

func (m *Manager) CheckHealth(context.Context) error {
	if m.active.Load() == nil {
		return ErrNoActivePolicy
	}
	return nil
}

func (m *Manager) ClusterRoster(ctx context.Context, strategy router.Strategy) (policy.RosterSnapshot, error) {
	snapshot := ServingSnapshotFromContext(ctx)
	if snapshot == nil {
		snapshot = m.active.Load()
	}
	if snapshot == nil {
		return policy.RosterSnapshot{}, ErrNoActivePolicy
	}
	roster := snapshotRoster(snapshot, strategy)
	status := m.Status()
	roster.Environment, roster.Lane = string(m.environment), string(m.lane)
	roster.LatestObservedGeneration, roster.RejectedGeneration = status.LatestObservedGeneration, status.RejectedGeneration
	roster.LastRejectionReason = status.LastRejectionReason
	return roster, nil
}

func snapshotRoster(snapshot *Snapshot, strategy router.Strategy) policy.RosterSnapshot {
	roster, reference := snapshot.policyForStrategy(strategy)
	clusters := make(map[string][]string, len(roster.Clusters))
	for label, cluster := range roster.Clusters {
		clusters[label] = append([]string(nil), cluster.Arms...)
	}
	harnesses := make(map[string]map[string][]string)
	for label, cluster := range roster.Clusters {
		for harness, arms := range cluster.ArmsByHarness {
			if harnesses[string(harness)] == nil {
				harnesses[string(harness)] = make(map[string][]string, len(roster.Clusters))
			}
			harnesses[string(harness)][label] = append([]string(nil), arms...)
		}
	}
	for harness, harnessClusters := range harnesses {
		for label, cluster := range roster.Clusters {
			if len(harnessClusters[label]) == 0 {
				harnessClusters[label] = append([]string(nil), cluster.Arms...)
			}
		}
		harnesses[harness] = harnessClusters
	}
	return policy.RosterSnapshot{
		SchemaVersion: string(roster.SchemaVersion), ReleaseID: snapshot.HeadSnapshot.Head.ReleaseSHA256,
		PolicySHA256: reference.SHA256, RosterSHA256: reference.SHA256,
		HeadGeneration: snapshot.HeadSnapshot.Generation, Clusters: clusters, Harnesses: harnesses,
	}
}

func (m *Manager) AllRosterArms() ([]string, error) {
	snapshot := m.active.Load()
	if snapshot == nil {
		return nil, ErrNoActivePolicy
	}
	return snapshot.Policy.AllArms(), nil
}

func (m *Manager) Roster(context.Context) ([]string, error) { return m.AllRosterArms() }

func (m *Manager) DistributionRoster(_ context.Context, strategy router.Strategy) (*rosterdata.Roster, error) {
	snapshot := m.Active()
	if snapshot == nil || snapshot.Policy == nil {
		return nil, ErrNoActivePolicy
	}
	roster, _ := snapshot.policyForStrategy(strategy)
	return roster, nil
}

type DynamicRouter struct {
	manager  *Manager
	strategy router.Strategy
	prepared *Snapshot
}

func NewAdmittedRouter(strategy router.Strategy, prepared *Snapshot) *DynamicRouter {
	return &DynamicRouter{strategy: strategy, prepared: prepared}
}

func NewDynamicRouter(manager *Manager, strategy router.Strategy) *DynamicRouter {
	return &DynamicRouter{manager: manager, strategy: strategy}
}

func (r *DynamicRouter) Available() bool {
	return r != nil && (r.prepared != nil || r.manager != nil && r.manager.Active() != nil)
}

func (r *DynamicRouter) Route(ctx context.Context, request router.Request) (router.Decision, error) {
	activeRouter, err := r.activeRouter(ctx)
	if err != nil {
		return router.Decision{}, err
	}
	return activeRouter.Route(ctx, request)
}

func (r *DynamicRouter) PreviewRoute(ctx context.Context, request router.Request) (policy.PreviewResult, error) {
	activeRouter, err := r.activeRouter(ctx)
	if err != nil {
		return policy.PreviewResult{}, err
	}
	previewer, ok := activeRouter.(policy.RoutePreviewer)
	if !ok {
		return policy.PreviewResult{}, ErrNoActivePolicy
	}
	return previewer.PreviewRoute(ctx, request)
}

func (r *DynamicRouter) CurrentCapabilities() policy.Capabilities {
	if r != nil && r.prepared != nil {
		return r.CapabilitiesForRequest(WithServingSnapshot(context.Background(), r.prepared))
	}
	return r.CapabilitiesForRequest(context.Background())
}

func (r *DynamicRouter) CapabilitiesForRequest(ctx context.Context) policy.Capabilities {
	activeRouter, err := r.activeRouter(ctx)
	if err != nil {
		return policy.Capabilities{}
	}
	source, ok := activeRouter.(policy.CapabilitySource)
	if !ok {
		return policy.Capabilities{}
	}
	return source.CurrentCapabilities()
}

func (r *DynamicRouter) ReportOutcome(ctx context.Context, payload map[string]interface{}) error {
	activeRouter, err := r.activeRouter(ctx)
	if err != nil {
		return err
	}
	reporter, ok := activeRouter.(policy.OutcomeReporter)
	if !ok {
		return nil
	}
	return reporter.ReportOutcome(ctx, payload)
}

func (r *DynamicRouter) ReportFeedback(ctx context.Context, payload map[string]interface{}) error {
	activeRouter, err := r.activeRouter(ctx)
	if err != nil {
		return err
	}
	reporter, ok := activeRouter.(policy.FeedbackReporter)
	if !ok {
		return nil
	}
	return reporter.ReportFeedback(ctx, payload)
}

func (r *DynamicRouter) ObserveEscalation(ctx context.Context, request escalation.ObserveRequest) (escalation.ObserveResponse, error) {
	activeRouter, err := r.activeRouter(ctx)
	if err != nil {
		return escalation.ObserveResponse{}, err
	}
	observer, ok := activeRouter.(escalation.Observer)
	if !ok {
		return escalation.ObserveResponse{}, ErrNoActivePolicy
	}
	return observer.ObserveEscalation(ctx, request)
}

func (r *DynamicRouter) activeRouter(ctx context.Context) (router.Router, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: %w", router.ErrStrategyUnavailable, ErrNoActivePolicy)
	}
	snapshot := ServingSnapshotFromContext(ctx)
	if snapshot == nil && r.manager != nil {
		snapshot = r.manager.Active()
	}
	if snapshot == nil {
		return nil, fmt.Errorf("%w: %w", router.ErrStrategyUnavailable, ErrNoActivePolicy)
	}
	activeRouter := snapshot.Routers[r.strategy]
	if activeRouter == nil {
		return nil, fmt.Errorf("strategy %q missing from active policy snapshot: %w: %w", r.strategy, router.ErrStrategyUnavailable, ErrNoActivePolicy)
	}
	return activeRouter, nil
}

func digestPrefix(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func (m *Manager) recordObserved(generation int64) {
	m.statusMu.Lock()
	m.status.LatestObservedGeneration = generation
	m.statusMu.Unlock()
}

func (m *Manager) recordRejection(generation int64, err error) {
	m.statusMu.Lock()
	if generation > 0 {
		m.status.LatestObservedGeneration = generation
		m.status.RejectedGeneration = generation
	}
	m.status.LastRejectionReason = err.Error()
	m.statusMu.Unlock()
}

func (m *Manager) recordSuccess(generation int64, releaseID, policySHA string) {
	m.statusMu.Lock()
	m.status.ActiveHeadGeneration = generation
	m.status.ActiveReleaseID = releaseID
	m.status.ActivePolicySHA256 = policySHA
	m.status.LatestObservedGeneration = generation
	m.status.RejectedGeneration = 0
	m.status.LastRejectionReason = ""
	m.status.LastSuccessfulRefresh = time.Now().UTC()
	m.statusMu.Unlock()
}

func (m *Manager) setPolling(polling bool) {
	m.statusMu.Lock()
	m.status.Polling = polling
	m.statusMu.Unlock()
}

func jitteredInterval(base time.Duration) time.Duration {
	if base <= 0 {
		return defaultPollInterval
	}
	quarter := base / 4
	if quarter == 0 {
		return base
	}
	offset, _ := rand.Int(rand.Reader, big.NewInt(int64(quarter*2)+1))
	return base - quarter + time.Duration(offset.Int64())
}

var _ router.Router = (*DynamicRouter)(nil)
var _ policy.RoutePreviewer = (*DynamicRouter)(nil)
var _ policy.CapabilitySource = (*DynamicRouter)(nil)
var _ policy.OutcomeReporter = (*DynamicRouter)(nil)
var _ policy.FeedbackReporter = (*DynamicRouter)(nil)
var _ escalation.Observer = (*DynamicRouter)(nil)
