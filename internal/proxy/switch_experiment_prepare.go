package proxy

import (
	"context"
	"errors"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

const (
	switchExperimentCacheTTL             = 5 * time.Minute
	switchCompactPrepareAheadTokens      = 50_000
	switchCompactRefreshTokens           = 50_000
	switchCompactRetryAfter              = 15 * time.Second
	switchCompactRefreshTailFactor       = 3
	DefaultSwitchCompactionTimeout       = 20 * time.Second
	DefaultSwitchCompactionSummaryTokens = 1_500
	switchCompactionFailureTimeout       = "timeout"
	switchCompactionFailureUpstream      = "upstream_error"
	switchCompactionFailureEmpty         = "empty_summary"
)

type preparedCompaction struct {
	plan         conversation.ThreadCompaction
	usage        handover.Usage
	latency      time.Duration
	promptTokens int
	inflight     bool
	failedAt     time.Time
}

type readyCompaction struct {
	plan    conversation.ThreadCompaction
	usage   handover.Usage
	latency time.Duration
}

func (r readyCompaction) AnchorMessages() int { return r.plan.AnchorMessages }

func (x *switchExperiment) compactedPromptTokens(env *translate.RequestEnvelope, anchor int) int {
	return x.summaryTokens() + int(float64(env.ContextOverflowTokenEstimate())*env.MessagesShareFrom(anchor))
}

func (x *switchExperiment) summaryTokens() int {
	if x.cfg.SummaryTokens > 0 {
		return x.cfg.SummaryTokens
	}
	return DefaultSwitchCompactionSummaryTokens
}

func (x *switchExperiment) tailCovered(env *translate.RequestEnvelope, ready readyCompaction) bool {
	return env.MessageCount()-ready.AnchorMessages() >= x.cfg.CompactTail
}

func (x *switchExperiment) prepareAheadTokens() int {
	return max(x.cfg.Tuning.CompactMinPromptTokens, switchCompactPrepareAheadTokens)
}

func (x *switchExperiment) compactTimeout() time.Duration {
	if x.cfg.CompactTimeout > 0 {
		return x.cfg.CompactTimeout
	}
	return DefaultSwitchCompactionTimeout
}

func (x *switchExperiment) summarizerFor(current string, thread switchExperimentThread, cacheCold bool, now time.Time) string {
	if x.compactor.AllowsCompaction(current) && !cacheCold {
		return current
	}
	warmest, warmestAt := "", now.Add(-switchExperimentCacheTTL)
	for model, state := range thread.caches {
		if x.compactor.AllowsCompaction(model) && state.ServedAt.After(warmestAt) {
			warmest, warmestAt = model, state.ServedAt
		}
	}
	if warmest != "" {
		return warmest
	}
	if x.compactor.AllowsCompaction(policy.PrecompactionDefaultModel) {
		return policy.PrecompactionDefaultModel
	}
	return ""
}

func (x *switchExperiment) planCompaction(env *translate.RequestEnvelope, summarizer string, now time.Time) (conversation.ThreadCompaction, bool) {
	if summarizer == "" {
		return conversation.ThreadCompaction{}, false
	}
	return conversation.PlanThreadCompaction(env, x.cfg.CompactTail, "pending", summarizer, now)
}

func (x *switchExperiment) planEarliestCompaction(env *translate.RequestEnvelope, summarizer string, now time.Time) (conversation.ThreadCompaction, bool) {
	if summarizer == "" {
		return conversation.ThreadCompaction{}, false
	}
	for tail := x.cfg.CompactTail; tail >= 1; tail-- {
		if plan, ok := conversation.PlanThreadCompaction(env, tail, "pending", summarizer, now); ok {
			return plan, true
		}
	}
	return conversation.ThreadCompaction{}, false
}

func (x *switchExperiment) readyCompaction(key string, env *translate.RequestEnvelope) (readyCompaction, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	p := x.prepared[key]
	if p == nil || p.plan.Summary == "" || !conversation.ThreadCompactionFits(env, p.plan) {
		return readyCompaction{}, false
	}
	return readyCompaction{plan: p.plan, usage: p.usage, latency: p.latency}, true
}

func (x *switchExperiment) discardPrepared(key string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.prepared, key)
}

func (x *switchExperiment) claimPreparation(key string, tokens, messages int, now time.Time) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	p := x.prepared[key]
	switch {
	case p == nil:
		x.prepared[key] = &preparedCompaction{inflight: true, promptTokens: tokens}
		return true
	case p.inflight:
		return false
	case !p.failedAt.IsZero() && now.Sub(p.failedAt) < switchCompactRetryAfter:
		return false
	case p.plan.Summary != "" && tokens-p.promptTokens < switchCompactRefreshTokens &&
		messages-p.plan.AnchorMessages < switchCompactRefreshTailFactor*x.cfg.CompactTail:
		return false
	}
	p.inflight = true
	return true
}

func (x *switchExperiment) finishPreparation(key string, tokens int, now time.Time, done preparedCompaction) {
	x.mu.Lock()
	defer x.mu.Unlock()
	p := x.prepared[key]
	if p == nil {
		return
	}
	p.inflight = false
	if done.plan.Summary == "" {
		p.failedAt = now
		return
	}
	done.promptTokens = tokens
	*p = done
}

func (x *switchExperiment) prepare(ctx context.Context, key string, served *translate.RequestEnvelope, plan conversation.ThreadCompaction, req router.Request, summarizer string, tokens int, reason string) {
	now := plan.CreatedAt
	if !x.claimPreparation(key, tokens, served.MessageCount(), now) {
		return
	}
	log := observability.FromContext(ctx)
	history := served.Clone()
	timeout := x.compactTimeout()
	summarize := x.compactor.CompactionOn(summarizer, x.summaryTokens())
	x.spawn(clearCredentials(ctx), timeout, func(callCtx context.Context) {
		started := x.now()
		summary, usage, err := summarize.Summarize(callCtx, history, req)
		latency := x.now().Sub(started)
		failure := ""
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded):
			failure = switchCompactionFailureTimeout
		case err != nil:
			failure = switchCompactionFailureUpstream
		case summary == "":
			failure = switchCompactionFailureEmpty
		}
		if failure != "" {
			x.finishPreparation(key, tokens, now, preparedCompaction{})
			log.Warn("switch experiment summary preparation failed", "err", err, "failure", failure, "reason", reason, "summarizer_model", summarizer, "summary_latency_ms", latency.Milliseconds(), "summary_input_tokens", usage.InputTokens, "summary_output_tokens", usage.OutputTokens)
			return
		}
		plan.Summary = summary
		x.finishPreparation(key, tokens, now, preparedCompaction{plan: plan, usage: usage, latency: latency})
		log.Info("switch experiment summary prepared", "reason", reason, "summarizer_model", summarizer, "anchor_messages", plan.AnchorMessages, "prompt_tokens", tokens, "summary_latency_ms", latency.Milliseconds(), "summary_input_tokens", usage.InputTokens, "summary_output_tokens", usage.OutputTokens, "summary_cache_read_tokens", usage.CacheRead, "summary_cache_creation_tokens", usage.CacheCreation+usage.CacheCreation1h)
	})
}
