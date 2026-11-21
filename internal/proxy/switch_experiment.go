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
	Live        bool
	Tuning      routingpolicy.SwitchTuning
	CompactTail int
}

type ThreadCompactor interface {
	CompactionOn(model string) handover.Summarizer
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
}

func (s *Service) WithSwitchExperiment(cfg SwitchExperimentConfig, compactor ThreadCompactor, store conversation.CompactionStore) *Service {
	if cfg.Live {
		s.switchExperiment = &switchExperiment{cfg: cfg, compactor: compactor, compactions: store, threads: map[string]switchExperimentThread{}}
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
	plan, canCompact := conversation.ThreadCompaction{}, false
	if x.cfg.CompactTail > 0 && x.compactor != nil {
		plan, canCompact = conversation.PlanThreadCompaction(original, x.cfg.CompactTail, "pending", current, now)
	}
	compactedTokens := 0
	if canCompact {
		compactedTokens = switchCompactionSummaryTokens + int(float64(original.ContextOverflowTokenEstimate())*original.MessagesShareFrom(plan.AnchorMessages))
	}
	input := routingpolicy.SwitchInput{
		Current:              current,
		Fresh:                res.Fresh.Model,
		EstimatedInputTokens: tokens,
		PromptTokens:         thread.promptTokens,
		CacheCold:            !thread.lastAt.IsZero() && now.Sub(thread.lastAt) > 5*time.Minute,
		Now:                  now,
		MessageCount:         env.MessageCount(),
		UserPrompt:           env.LastUserMessage().HasText,
		ModelCaches:          thread.caches,
		CompactedTokens:      compactedTokens,
		Tuning:               x.cfg.Tuning,
	}
	verdict := routingpolicy.DecideSwitch(input)
	served := res.Fresh
	if verdict.Serve != res.Fresh.Model {
		served.Model = verdict.Serve
	}
	compacted := false
	if verdict.Compact && canCompact && deploymentCreds {
		summary, usage, err := x.compactor.CompactionOn(current).Summarize(clearCredentials(ctx), env, req)
		switch {
		case err != nil || summary == "":
			log.Warn("switch experiment compaction failed; switching with full history", "err", err, "current", current)
		default:
			plan.Summary = summary
			x.compactions.Save(key, plan)
			fresh := original.Clone()
			compacted = conversation.ApplyThreadCompaction(fresh, plan)
			log.Info("switch experiment compacted", "summarizer_model", current, "anchor_messages", plan.AnchorMessages, "summary_input_tokens", usage.InputTokens, "summary_output_tokens", usage.OutputTokens, "summary_cache_read_tokens", usage.CacheRead, "summary_cache_creation_tokens", usage.CacheCreation+usage.CacheCreation1h, "applied", compacted)
			if compacted {
				*env = *fresh
				res.Handover.SummaryUsage = usage
				res.Handover.SummaryTokens = estimateSummaryTokens(summary)
			}
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
		"estimated_tokens", tokens, "compacted_tokens", compactedTokens, "servedTokens", servedTokens,
		"messages", env.MessageCount())
}

const switchCompactionSummaryTokens = 4_000
