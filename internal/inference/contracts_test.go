package inference_test

import (
	"context"

	"weave-os/router/internal/inference"
)

type contractExecutor struct{}

func (contractExecutor) Execute(context.Context, inference.InvocationRequest, inference.ResolvedPlan) (inference.ExecutionOutcome, error) {
	return inference.ExecutionOutcome{}, nil
}

var _ inference.Executor = contractExecutor{}
