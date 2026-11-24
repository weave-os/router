package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/requestcontext"
)

func withPickedModelFunding(ctx context.Context, servedAsIs bool, provider string) context.Context {
	if !auth.ClientModelPassthroughFrom(ctx) {
		return ctx
	}
	if !servedAsIs {
		return auth.WithoutClientModelPassthrough(ctx)
	}
	return credentials.FundOnOwnPlan(ctx, provider)
}

func withCodexPickFunding(ctx context.Context, servedModel, provider string) context.Context {
	selected, _, _ := strings.Cut(requestcontext.CodexSelectedModel(ctx), ":")
	if selected == "" || selected != servedModel {
		return ctx
	}
	return credentials.FundOnOwnPlan(ctx, provider)
}
