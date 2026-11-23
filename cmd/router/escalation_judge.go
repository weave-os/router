package main

import (
	"time"
	"weave-os/router/internal/config"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/escalationjudge"
	"weave-os/router/internal/postgres"
	openaiCompatProvider "weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/escalationdashboard"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
)

func newSwitchyardEscalationJudge(enabled bool, plans *policy.PlanResolver, executor *dispatch.Executor, apiKey string) llmescalation.Judge {
	if !enabled {
		return nil
	}
	judge, err := escalationjudge.New(plans, executor, apiKey, config.GetOr("FIREWORKS_BASE_URL", openaiCompatProvider.FireworksBaseURL))
	if err != nil {
		panic(err)
	}
	return judge
}

func newEscalationAdmin(dashboard escalationdashboard.Store, store *postgres.LLMEscalationRepo, invalidate func(string), switchyardJudge, qwenJudge llmescalation.Judge, switchyardActive bool) *floor.Admin {
	return &floor.Admin{
		Dashboard: dashboard, Sessions: store, Configuration: store, InvalidateSelection: invalidate,
		SwitchyardJudge: switchyardJudge, QwenJudge: qwenJudge, SwitchyardActive: switchyardActive, Clock: time.Now,
	}
}
