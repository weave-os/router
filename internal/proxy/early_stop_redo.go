package proxy

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

// ReasonEarlyStopRedo marks a Claude Code tool-result turn re-served on a
// higher-tier candidate after the routed model ended the agent turn without
// calling a tool.
const ReasonEarlyStopRedo = "early-stop-redo"

const markerReasonEarlyStop = "re-served after an early stop"

// earlyStopRedoTarget picks the candidate a stopped tool-result turn is
// re-served on: the highest-tier model the policy scored for this turn that
// the request may reach, strictly above the served model's tier. ok is false
// when the served model is already the top of its candidates.
func (s *Service) earlyStopRedoTarget(ctx context.Context, served router.Decision, est, sigSavings, outputReserve int) (router.Decision, bool) {
	servedTier := catalog.TierFor(served.Model)
	if served.Metadata == nil || servedTier == catalog.TierUnknown {
		return router.Decision{}, false
	}
	ordered := higherTierCandidates(servedTier, served.Metadata.CandidateModels, served.Metadata.CandidateScores)
	admitted := s.rescueDecisions(ctx, served, ordered, ReasonEarlyStopRedo, est, sigSavings, outputReserve)
	// The rescue walk reorders for provider diversity; the redo wants the
	// strongest admitted model, so pick in tier-and-score order.
	for _, model := range ordered {
		for _, target := range admitted {
			if target.Model == model {
				return target, true
			}
		}
	}
	return router.Decision{}, false
}

// higherTierCandidates orders the candidates above tier from the top tier
// down, best score first within a tier.
func higherTierCandidates(tier catalog.Tier, candidates []string, scores map[string]float32) []string {
	var ordered []string
	for _, model := range candidates {
		if catalog.TierFor(model) > tier && !slices.Contains(ordered, model) {
			ordered = append(ordered, model)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		ti, tj := catalog.TierFor(ordered[i]), catalog.TierFor(ordered[j])
		if ti != tj {
			return ti > tj
		}
		return scores[ordered[i]] > scores[ordered[j]]
	})
	return ordered
}

// lowerTierCandidates lists the candidates below the redo target's tier. They
// are kept out for the rest of the agent turn once a redo serves, so the turn
// cannot be redone twice.
func lowerTierCandidates(target string, candidates []string) []string {
	tier := catalog.TierFor(target)
	var lower []string
	for _, model := range candidates {
		if t := catalog.TierFor(model); t != catalog.TierUnknown && t < tier {
			lower = append(lower, model)
		}
	}
	return lower
}

// earlyStopRedoMode resolves the mode for this turn: off unless it is a
// streamed Claude Code main-thread tool-result turn the router chose the model
// for.
func (s *Service) earlyStopRedoMode(ctx context.Context, routeRes turnLoopResult, decision router.Decision, streaming, agentShadow bool) flags.EarlyStopRedoMode {
	mode := s.ResolveEarlyStopRedo(ctx)
	if mode == flags.EarlyStopRedoOff || !streaming || agentShadow ||
		ClientIdentityFrom(ctx).ClientApp != ClientAppClaudeCode ||
		routeRes.TurnType != turntype.ToolResult ||
		routeRes.HardPinned || routeRes.CallerModelPassthrough || routeRes.UsageBypass ||
		strings.HasPrefix(decision.Reason, translate.ReasonUserForceModel) {
		return flags.EarlyStopRedoOff
	}
	return mode
}

// earlyStopRedo carries what ProxyMessages lends a re-serve: the gate holding
// the stopped response, the closures that build and dispatch the candidate,
// and the served-attempt state the redo replaces (or restores on failure).
type earlyStopRedo struct {
	gate     *dispatch.EarlyStopGate
	prelude  *preludeBuffer
	header   http.Header
	log      *slog.Logger
	target   router.Decision
	build    func(target router.Decision) (dispatchAttempt, []catalog.ProviderBinding, string, effortResolution, context.Context, error)
	dispatch func(ctx context.Context, target router.Decision, bindings []catalog.ProviderBinding, attempt dispatchAttempt) (int, error)
	served   *earlyStopServed
}

// earlyStopServed points at ProxyMessages' record of the attempt that served.
type earlyStopServed struct {
	decision   *router.Decision
	bindings   *[]catalog.ProviderBinding
	marker     *string
	winnerIdx  *int
	extractor  **telemetry.UsageExtractor
	summary    *translate.ResponseSummary
	reqStats   *providers.RequestMutationStats
	fastServed *bool
	effort     *effortResolution
}

type earlyStopServedSnapshot struct {
	decision   router.Decision
	bindings   []catalog.ProviderBinding
	marker     string
	winnerIdx  int
	extractor  *telemetry.UsageExtractor
	summary    translate.ResponseSummary
	reqStats   providers.RequestMutationStats
	fastServed bool
	effort     effortResolution
}

func (p *earlyStopServed) save() earlyStopServedSnapshot {
	return earlyStopServedSnapshot{
		decision: *p.decision, bindings: *p.bindings, marker: *p.marker, winnerIdx: *p.winnerIdx,
		extractor: *p.extractor, summary: *p.summary, reqStats: *p.reqStats, fastServed: *p.fastServed, effort: *p.effort,
	}
}

func (p *earlyStopServed) restore(s earlyStopServedSnapshot) {
	*p.decision, *p.bindings, *p.marker, *p.winnerIdx = s.decision, s.bindings, s.marker, s.winnerIdx
	*p.extractor, *p.summary, *p.reqStats, *p.fastServed, *p.effort = s.extractor, s.summary, s.reqStats, s.fastServed, s.effort
}

// redoEarlyStop re-serves a withheld early stop on redo.target. If the candidate
// cannot be dispatched, nothing changes and the caller releases the stopped
// response. If the redo fails before output, the stopped response is
// delivered instead, so the client never loses a turn the router discarded.
func (s *Service) redoEarlyStop(ctx context.Context, redo earlyStopRedo) (redone bool, err error) {
	attempt, bindings, marker, effort, redoCtx, buildErr := redo.build(redo.target)
	if buildErr != nil || len(bindings) == 0 {
		redo.log.Warn("Early-stop redo: candidate is not dispatchable; serving the stopped response",
			"redo_model", redo.target.Model, "redo_provider", redo.target.Provider, "err", buildErr)
		return false, nil
	}
	stopped := redo.served.save()
	header := redo.header.Clone()
	status, body := redo.gate.TakeWithheld()
	redo.prelude.Discard()
	*redo.served.summary = translate.ResponseSummary{}
	*redo.served.reqStats = providers.RequestMutationStats{}
	redo.log.Info("Early-stop redo: re-serving the turn on a higher-tier candidate",
		"stopped_model", stopped.decision.Model, "redo_model", redo.target.Model, "redo_provider", redo.target.Provider,
		"stopped_output_bytes", len(body))
	winnerIdx, err := redo.dispatch(redoCtx, redo.target, bindings, attempt)
	if err == nil || redo.prelude.Committed() {
		*redo.served.decision, *redo.served.bindings, *redo.served.marker = redo.target, bindings, marker
		*redo.served.winnerIdx, *redo.served.effort = winnerIdx, effort
		return err == nil, err
	}
	redo.log.Warn("Early-stop redo failed before output; serving the stopped response",
		"redo_model", redo.target.Model, "upstream_status", upstreamStatus(err), "err", err)
	redo.served.restore(stopped)
	redo.prelude.Discard()
	clear(redo.header)
	maps.Copy(redo.header, header)
	redo.prelude.Seal()
	if status != 0 {
		redo.prelude.WriteHeader(status)
	}
	if _, err := redo.gate.Write(body); err != nil {
		return false, err
	}
	redo.gate.Flush()
	return false, nil
}
