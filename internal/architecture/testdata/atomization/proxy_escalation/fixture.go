package proxyescalation

import (
	"context"

	"weave-os/router/internal/escalationjudge"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
)

var _ = escalationjudge.New

func drive(ctx context.Context, store escalation.Store, judge llmescalation.Judge, request llmescalation.JudgeRequest) {
	_, _, _ = store.Claim(ctx, [32]byte{}, "", "", [32]byte{})
	_, _ = judge.Judge(ctx, request)
}
