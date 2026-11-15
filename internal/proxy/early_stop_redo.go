package proxy

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
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

// redoEarlyStop re-serves a withheld early stop on redo.target. If the candidate
// cannot be dispatched, nothing changes and the caller releases the stopped
// response. If the redo fails before output, the stopped response is
// delivered instead, so the client never loses a turn the router discarded.
func (s *Service) redoEarlyStop(ctx context.Context, redo dispatch.EarlyStopRedo) (redone bool, err error) {
	attempt, bindings, marker, effort, redoCtx, buildErr := redo.Build(redo.Target)
	if buildErr != nil || len(bindings) == 0 {
		redo.Log.Warn("Early-stop redo: candidate is not dispatchable; serving the stopped response",
			"redo_model", redo.Target.Model, "redo_provider", redo.Target.Provider, "err", buildErr)
		return false, nil
	}
	stopped := redo.Served.Save()
	header := redo.Header.Clone()
	status, body := redo.Gate.TakeWithheld()
	redo.Prelude.Discard()
	*redo.Served.Summary = translate.ResponseSummary{}
	*redo.Served.ReqStats = providers.RequestMutationStats{}
	redo.Log.Info("Early-stop redo: re-serving the turn on a higher-tier candidate",
		"stopped_model", stopped.Decision.Model, "redo_model", redo.Target.Model, "redo_provider", redo.Target.Provider,
		"stopped_output_bytes", len(body))
	winnerIdx, err := redo.Dispatch(redoCtx, redo.Target, bindings, attempt)
	if err == nil || redo.Prelude.Committed() {
		*redo.Served.Decision, *redo.Served.Bindings, *redo.Served.Marker = redo.Target, bindings, marker
		*redo.Served.WinnerIdx, *redo.Served.Effort = winnerIdx, effort
		return err == nil, err
	}
	redo.Log.Warn("Early-stop redo failed before output; serving the stopped response",
		"redo_model", redo.Target.Model, "upstream_status", upstreamStatus(err), "err", err)
	redo.Served.Restore(stopped)
	redo.Prelude.Discard()
	clear(redo.Header)
	maps.Copy(redo.Header, header)
	redo.Prelude.Seal()
	if status != 0 {
		redo.Prelude.WriteHeader(status)
	}
	if _, err := redo.Gate.Write(body); err != nil {
		return false, err
	}
	redo.Gate.Flush()
	return false, nil
}
