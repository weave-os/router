package proxy

import (
	"context"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type SwitchExperimentConfig struct {
	Live           bool
	Tuning         routingpolicy.SwitchTuning
	CompactTail    int
	CompactTimeout time.Duration
	SummaryTokens  int
}

type ThreadCompactor interface {
	AllowsCompaction(model string) bool
	CompactionOn(model string, summaryTokens int) handover.Summarizer
}

type switchExperimentThread struct {
	lastServed   string
	lastAt       time.Time
	promptTokens int
	caches       sessionpin.ModelCaches
}

type switchExperiment struct {
	cfg         SwitchExperimentConfig
	compactor   ThreadCompactor
	compactions conversation.CompactionStore
	mu          sync.Mutex
	threads     map[string]switchExperimentThread
	prepared    map[string]*preparedCompaction
	spawn       func(parent context.Context, timeout time.Duration, fn func(context.Context))
	now         func() time.Time
}

func (s *Service) WithSwitchExperiment(cfg SwitchExperimentConfig, compactor ThreadCompactor, store conversation.CompactionStore) *Service {
	if cfg.Live {
		s.switchExperiment = &switchExperiment{
			cfg: cfg, compactor: compactor, compactions: store,
			threads: map[string]switchExperimentThread{}, prepared: map[string]*preparedCompaction{},
			now: func() time.Time { return s.clock.Now() },
			spawn: func(parent context.Context, timeout time.Duration, fn func(context.Context)) {
				observability.SafeGoContext(parent, observability.FromContext(parent), timeout, "switch-experiment-compaction", fn)
			},
		}
	}
	return s
}

func (x *switchExperiment) thread(key string) switchExperimentThread {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.threads[key]
}

func (x *switchExperiment) record(key, model string, now time.Time, tokens, messages int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	thread := x.threads[key]
	if thread.caches == nil {
		thread.caches = sessionpin.ModelCaches{}
	}
	thread.lastServed, thread.lastAt, thread.promptTokens = model, now, tokens
	thread.caches[model] = sessionpin.ModelCacheState{ServedAt: now, PromptTokens: tokens, MessageCount: messages}
	x.threads[key] = thread
}

func (s *Service) applySwitchExperiment(ctx context.Context, env *translate.RequestEnvelope, req router.Request, reqHeaders http.Header, res *turnLoopResult) {
	if s.switchExperiment == nil {
		return
	}
	s.switchExperiment.apply(ctx, env, req, s.clock.Now(), !s.requestUsesNonDeploymentCreds(ctx, reqHeaders), res)
}

func (x *switchExperiment) apply(ctx context.Context, env *translate.RequestEnvelope, req router.Request, now time.Time, deploymentCreds bool, res *turnLoopResult) {
	if res.HardPinned || res.Fresh.Model == "" || env.MessageCount() == 0 {
		return
	}
	log := observability.FromContext(ctx)
	key := hex.EncodeToString(res.SessionKey[:])
	original := env.Clone()
	stored, hasCompaction := x.compactions.Load(key, now)
	sticky := hasCompaction && conversation.ApplyThreadCompaction(env, stored)
	if hasCompaction && !sticky {
		x.compactions.Clear(key)
	}
	thread := x.thread(key)
	tokens := env.ContextOverflowTokenEstimate()
	current := thread.lastServed
	if current == "" {
		current = res.Decision.Model
	}
	cacheCold := !thread.lastAt.IsZero() && now.Sub(thread.lastAt) > switchExperimentCacheTTL
	compacting := x.cfg.CompactTail > 0 && x.compactor != nil && deploymentCreds
	summarizer := ""
	if compacting {
		summarizer = x.summarizerFor(current, thread, cacheCold, now)
	}
	ready, hasPrepared := x.readyCompaction(key, original)
	hasReady := hasPrepared && x.tailCovered(original, ready)
	input := routingpolicy.SwitchInput{
		Current:              current,
		Fresh:                res.Fresh.Model,
		EstimatedInputTokens: tokens,
		PromptTokens:         thread.promptTokens,
		CacheCold:            cacheCold,
		Now:                  now,
		MessageCount:         env.MessageCount(),
		UserPrompt:           env.LastUserMessage().HasText,
		ModelCaches:          thread.caches,
		Summarizer:           summarizer,
		SummaryReady:         hasReady,
		Tuning:               x.cfg.Tuning,
	}
	planned, canPlan := x.planCompaction(original, summarizer, now)
	earliest, canPlanEarly := x.planEarliestCompaction(original, summarizer, now)
	if hasReady {
		input.CompactedTokens = x.compactedPromptTokens(original, ready.AnchorMessages())
	} else if canPlan {
		input.CompactedTokens = x.compactedPromptTokens(original, planned.AnchorMessages)
	}
	verdict := routingpolicy.DecideSwitch(input)
	summarizerWarm := thread.caches[summarizer].ServedAt.After(now.Add(-switchExperimentCacheTTL))
	prepareReason := ""
	prepareWith := planned
	switch {
	case verdict.Compact && !hasPrepared:
		prepareReason = "switch"
	case canPlanEarly && summarizerWarm && !cacheCold && tokens >= x.prepareAheadTokens() &&
		2*x.compactedPromptTokens(original, earliest.AnchorMessages) <= tokens:
		prepareReason, prepareWith = "ahead", earliest
	}
	if prepareReason != "" {
		x.prepare(ctx, key, env, prepareWith, req, summarizer, tokens, prepareReason)
		ready, hasPrepared = x.readyCompaction(key, original)
		hasReady = hasPrepared && x.tailCovered(original, ready)
	}
	if verdict.Compact && !hasReady {
		verdict.Compact = false
		verdict.Reason = routingpolicy.SwitchReasonSummaryNotReady
		log.Info("switch experiment summary not ready; switching with full history", "current", current, "serve", verdict.Serve, "summarizer_model", summarizer, "summary_prepared", hasPrepared)
	}
	served := res.Fresh
	if verdict.Serve != res.Fresh.Model {
		served.Model = verdict.Serve
	}
	compacted := false
	if verdict.Compact && hasReady {
		x.compactions.Save(key, ready.plan)
		fresh := original.Clone()
		compacted = conversation.ApplyThreadCompaction(fresh, ready.plan)
		x.discardPrepared(key)
		log.Info("switch experiment compacted", "summarizer_model", ready.plan.Model, "anchor_messages", ready.AnchorMessages(), "summary_input_tokens", ready.usage.InputTokens, "summary_output_tokens", ready.usage.OutputTokens, "summary_cache_read_tokens", ready.usage.CacheRead, "summary_cache_creation_tokens", ready.usage.CacheCreation+ready.usage.CacheCreation1h, "summary_latency_ms", ready.latency.Milliseconds(), "applied", compacted)
		if compacted {
			*env = *fresh
			res.Handover.SummaryUsage = ready.usage
			res.Handover.SummaryTokens = estimateSummaryTokens(ready.plan.Summary)
		}
	}
	res.Decision = served
	res.PinTier = "switch_experiment_" + string(verdict.Reason)
	servedTokens := env.ContextOverflowTokenEstimate()
	x.record(key, served.Model, now, servedTokens, env.MessageCount())
	log.Info("switch experiment decided",
		"current", current, "fresh", res.Fresh.Model, "served", served.Model,
		"reason", string(verdict.Reason), "switch", served.Model != current,
		"compact_verdict", verdict.Compact, "compacted", compacted, "sticky_applied", sticky,
		"estimated_tokens", tokens, "compacted_tokens", input.CompactedTokens, "servedTokens", servedTokens,
		"summarizer_model", summarizer, "summary_ready", hasReady, "prepare_reason", prepareReason,
		"messages", env.MessageCount())
}
