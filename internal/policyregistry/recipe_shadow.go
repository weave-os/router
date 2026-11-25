package policyregistry

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/rosterdata"
)

const (
	recipePolicyCacheSize         = 128
	recipePolicyCacheTTL          = time.Minute
	inactiveRecipeProfileCacheTTL = 30 * time.Second
)

type RecipePolicy struct {
	Policy PolicyObject
	Roster *rosterdata.Roster
}

type RecipePolicySource struct {
	store            ServingStore
	target           ServingTarget
	cache            *expirable.LRU[string, RecipePolicy]
	inactiveProfiles *expirable.LRU[string, struct{}]
	flights          singleflight.Group
}

func NewRecipePolicySource(store ServingStore, target ServingTarget) (*RecipePolicySource, error) {
	if store == nil || target == "" {
		return nil, errors.New("recipe policy source requires a serving store and target")
	}
	return &RecipePolicySource{
		store:            store,
		target:           target,
		cache:            expirable.NewLRU[string, RecipePolicy](recipePolicyCacheSize, nil, recipePolicyCacheTTL),
		inactiveProfiles: expirable.NewLRU[string, struct{}](recipePolicyCacheSize, nil, inactiveRecipeProfileCacheTTL),
	}, nil
}

func (s *RecipePolicySource) ProfilePolicy(ctx context.Context, profileKey string) (RecipePolicy, error) {
	if cached, found := s.cache.Get(profileKey); found {
		return cached, nil
	}
	if _, inactive := s.inactiveProfiles.Get(profileKey); inactive {
		return RecipePolicy{}, router.ErrRecipeShadowProfileInactive
	}
	loaded, err, _ := s.flights.Do(profileKey, func() (any, error) {
		return s.readProfilePolicy(ctx, profileKey)
	})
	if errors.Is(err, router.ErrRecipeShadowProfileInactive) {
		s.inactiveProfiles.Add(profileKey, struct{}{})
	}
	if err != nil {
		return RecipePolicy{}, err
	}
	recipe, _ := loaded.(RecipePolicy)
	s.cache.Add(profileKey, recipe)
	return recipe, nil
}

func (s *RecipePolicySource) readProfilePolicy(ctx context.Context, profileKey string) (RecipePolicy, error) {
	snapshot, err := s.store.ReadServingState(ctx, s.target)
	if err != nil {
		return RecipePolicy{}, err
	}
	if err := snapshot.State.Validate(s.store.RootURI(), s.target); err != nil {
		return RecipePolicy{}, err
	}
	current, exists := snapshot.State.Activations[snapshot.State.CurrentActivationID]
	if !exists {
		return RecipePolicy{}, errors.New("target has no current activation")
	}
	selectionSet, err := readSelectionSetView(ctx, s.store, current.SelectionSet)
	if err != nil {
		return RecipePolicy{}, err
	}
	selection, exists := selectionSet.Profiles[profileKey]
	if !exists {
		return RecipePolicy{}, router.ErrRecipeShadowProfileInactive
	}
	prepared, err := ReadPreparedSelection(ctx, s.store, s.target, profileKey, selection)
	if err != nil {
		return RecipePolicy{}, err
	}
	if diagnostics := prepared.Policy.CatalogDiagnostics(); len(diagnostics) != 0 {
		return RecipePolicy{}, errors.New("shadow profile policy contains arms absent from the worker catalog")
	}
	return RecipePolicy{Policy: prepared.PolicyReference, Roster: prepared.Policy}, nil
}
