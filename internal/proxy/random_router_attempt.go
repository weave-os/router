package proxy

import (
	"context"
	"errors"
	"net/http"
	"time"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	randomrouter "weave-os/router/internal/router/random_router"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/telemetry"
)

func recordRandomProviderAttempt(s *Service, ctx context.Context, plan inference.ResolvedPlan, target inference.Target, lease subscriptions.Lease, started time.Time, err error) {
	state := randomBenchmarkFrom(ctx)
	index := state.ProviderCalls
	state.ProviderCalls++
	store, ok := s.telemetry.(InferenceAttemptStore)
	if !ok {
		return
	}
	outcome := inference.AttemptOutcomeServed
	if err != nil {
		outcome = inference.AttemptOutcomeFailed
	}
	event := inference.AttemptEvent{
		Provenance: inference.Provenance{
			Purpose:          plan.Purpose(),
			PolicyID:         plan.PolicyID(),
			RegistryRevision: inference.PolicyRevision(plan.RegistryRevision()),
			PolicyRevision:   plan.PolicyRevision(),
		},
		RequestID:          observability.RequestIDFromContext(ctx),
		OperationID:        string(plan.Purpose()),
		AttemptIndex:       index,
		Target:             target,
		Outcome:            outcome,
		UpstreamStatusCode: upstreamStatus(err),
		Latency:            time.Since(started),
		Usage:              state.AttemptUsage,
		CostUSD:            state.AttemptCostUSD,
		Funding: inference.AttemptFunding{
			SubscriptionAccountID: lease.AccountID,
			Source:                inference.FundingSourceIncludedCodex,
			CapacitySource:        inference.CapacitySourceLinkedCodex,
			PoolID:                state.Config.InstallationID,
			PolicySHA256:          state.Config.PolicySHA256,
			ObservedAt:            started,
		},
	}
	if err != nil {
		event.FailureReason = string(classifyTurnError(err, "", 0))
	}
	if persistErr := store.InsertInferenceAttempt(context.WithoutCancel(ctx), InsertInferenceAttemptParams{InstallationID: state.Config.InstallationID, Event: event}); persistErr != nil {
		observability.FromContext(ctx).Error("Random attempt evidence could not be persisted", "attempt_index", index, "err", persistErr)
	}
}

type leasedProviderAttempt struct {
	inputs   failoverInputs
	decision router.Decision
	target   inference.Target
	lease    subscriptions.Lease
	client   providers.Client
	managed  bool
}

func invokeLeasedProviderAttempt(s *Service, ctx context.Context, plan inference.ResolvedPlan, attempt leasedProviderAttempt) (time.Time, error) {
	started := time.Now()
	state := randomBenchmarkFrom(ctx)
	if state == nil {
		return started, attempt.inputs.attempt(ctx, attempt.decision, attempt.client)
	}
	credentials := CredentialsFromContext(ctx)
	if !attempt.managed || credentials == nil || !credentials.OAuth || credentials.Source != credSourceCodexSubscription || len(credentials.AccountID) == 0 || len(credentials.APIKey) == 0 || credentials.SubscriptionAccountID != attempt.lease.AccountID || attempt.decision.Model != state.SelectedModel || !randomrouter.IncludesTarget(attempt.decision) || !state.Config.ExpiresAt.After(s.clockNow()) {
		return started, dispatchAbort{err: randomrouter.ErrUnavailable}
	}
	observation := &randomResponseObservation{state: state}
	client := dispatch.ObserveProxyResponse(attempt.client, observation.start, randomrouter.ErrUnavailable)
	err := attempt.inputs.attempt(ctx, attempt.decision, client)
	if observation.originalErr != nil {
		err = observation.originalErr
	}
	if observation.called {
		recordRandomProviderAttempt(s, ctx, plan, attempt.target, attempt.lease, started, err)
	}
	return started, err
}

type randomResponseObservation struct {
	state       *randomrouter.RequestState
	called      bool
	originalErr error
	terminal    *ingress.ResponsesTerminalObserver
	extractor   *telemetry.UsageExtractor
}

func (o *randomResponseObservation) start(_ context.Context, decision router.Decision, prepared providers.PreparedRequest, writer http.ResponseWriter) (http.ResponseWriter, func(error) error, error) {
	if o.called || prepared.Endpoint != providers.EndpointResponses || decision.Model != o.state.SelectedModel || !randomrouter.IncludesTarget(decision) {
		return nil, nil, randomrouter.ErrUnavailable
	}
	o.called = true
	o.terminal = ingress.NewResponsesTerminalObserver(writer, true)
	var observed http.ResponseWriter
	o.extractor, observed = newFallbackUsageExtractor(o.terminal, decision.Provider)
	return observed, o.finish, nil
}

func (o *randomResponseObservation) finish(err error) error {
	o.terminal.Finalize()
	if errors.Is(err, context.Canceled) && o.terminal.Observed() && o.terminal.Err() == nil {
		err = nil
	}
	o.originalErr = err
	facts := o.extractor.UsageFacts()
	input, output := o.extractor.Tokens()
	cacheCreation, cacheRead := o.extractor.CacheTokens()
	o.state.AttemptUsage = inference.Usage{Known: facts.Present && facts.Input.Reported && facts.Output.Reported, InputTokens: input, OutputTokens: output, CacheCreationTokens: cacheCreation, CacheReadTokens: cacheRead}
	price, _ := catalog.PrimaryPriceFor(o.state.SelectedModel)
	o.state.AttemptCostUSD = catalog.EffectiveInputCost(input, cacheCreation, cacheRead, price, providers.ProviderOpenAI, o.extractor.UsageModifiers()) + catalog.EffectiveOutputCost(catalog.PromptTokens(input, cacheCreation, cacheRead, providers.ProviderOpenAI), output, price, o.extractor.UsageModifiers())
	if err == nil {
		err = o.terminal.Err()
	}
	return err
}
