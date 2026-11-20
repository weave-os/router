package main

import (
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/escalationjudge"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
)

func newSwitchyardEscalationJudge(enabled bool, plans *policy.PlanResolver, executor *dispatch.Executor, apiKey string) llmescalation.Judge {
	if !enabled {
		return nil
	}
	judge, err := escalationjudge.New(plans, executor, apiKey)
	if err != nil {
		panic(err)
	}
	return judge
}
