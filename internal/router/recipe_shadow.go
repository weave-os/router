package router

import (
	"context"
	"errors"
)

type RecipeShadowPick struct {
	Model        string
	Provider     string
	RosterArmID  string
	PolicyGroup  string
	PolicySHA256 string
}

var (
	ErrRecipeShadowProfileInactive   = errors.New("shadow profile is not in the target's current selection set")
	ErrRecipeShadowNoClassifierFacts = errors.New("served decision carries no classifier facts to replay")
	ErrRecipeShadowNoEligibleArm     = errors.New("recipe shadow found no eligible arm")
)

type RecipeShadowServed struct {
	RosterArmID    string
	PolicyGroup    string
	PredictedLabel string
}

type RecipeShadowReplayer interface {
	ServedFacts(served Decision) (RecipeShadowServed, bool)
	Replay(ctx context.Context, profileKey string, served Decision, req Request) (RecipeShadowPick, error)
}
