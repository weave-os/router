package router

import "context"

type RecipeShadowPick struct {
	Model          string
	Provider       string
	RosterArmID    string
	PolicyGroup    string
	PolicySHA256   string
	PredictedLabel string
}

type RecipeShadowServed struct {
	RosterArmID    string
	PolicyGroup    string
	PredictedLabel string
}

type RecipeShadowReplayer interface {
	ServedFacts(served Decision) (RecipeShadowServed, bool)
	Replay(ctx context.Context, profileKey string, served Decision, req Request) (RecipeShadowPick, error)
}
