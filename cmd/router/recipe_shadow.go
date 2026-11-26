package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/policyregistry"
	servingpostgres "weave-os/router/internal/postgres/serving"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/recipeshadow"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/telemetry/policyshadow"
)

type recipeProfileRosters struct {
	source *policyregistry.RecipePolicySource
}

func (r recipeProfileRosters) ProfileRoster(ctx context.Context, profileKey string) (recipeshadow.ProfileRoster, error) {
	recipe, err := r.source.ProfilePolicy(ctx, profileKey)
	if err != nil {
		return recipeshadow.ProfileRoster{}, err
	}
	return recipeshadow.ProfileRoster{Roster: recipe.Roster, PolicySHA256: recipe.Policy.SHA256}, nil
}

func recipeShadowFor(pool *pgxpool.Pool, admission *middleware.ServingAdmissionConfig, availableProviders map[string]struct{}) *policyshadow.RecipeRecorder {
	if admission == nil || admission.Store == nil {
		return nil
	}
	source, err := policyregistry.NewRecipePolicySource(admission.Store, admission.Identity.Target)
	if err != nil {
		panic(err)
	}
	return &policyshadow.RecipeRecorder{
		Profiles:    servingpostgres.NewRoutingModelProfileRepo(pool),
		Replayer:    recipeshadow.NewReplayer(recipeProfileRosters{source: source}, availableProviders),
		ShadowModel: router.ModelAutoEfficient,
	}
}
