package policyregistry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"weave-os/router/internal/router/rosterdata"
)

const (
	recipePolicyCacheSize = 128
	recipePolicyCacheTTL  = time.Minute
)

var ErrRecipeProfileNotActive = errors.New("shadow profile is not in the target's current selection set")

type RecipePolicy struct {
	ProfileKey string
	Policy     PolicyObject
	Roster     *rosterdata.Roster
}

type RecipePolicySource struct {
	store   ServingStore
	target  ServingTarget
	loaded  *expirable.LRU[string, RecipePolicy]
	flights singleflight.Group
}

func NewRecipePolicySource(store ServingStore, target ServingTarget) (*RecipePolicySource, error) {
	if store == nil || target == "" {
		return nil, errors.New("recipe policy source requires a serving store and target")
	}
	return &RecipePolicySource{
		store:  store,
		target: target,
		loaded: expirable.NewLRU[string, RecipePolicy](recipePolicyCacheSize, nil, recipePolicyCacheTTL),
	}, nil
}

func (s *RecipePolicySource) ProfilePolicy(ctx context.Context, profileKey string) (RecipePolicy, error) {
	if profileKey == "" {
		return RecipePolicy{}, errors.New("recipe policy requires a profile key")
	}
	if cached, found := s.loaded.Get(profileKey); found {
		return cached, nil
	}
	loaded, err, _ := s.flights.Do(profileKey, func() (any, error) {
		return s.readProfilePolicy(ctx, profileKey)
	})
	if err != nil {
		return RecipePolicy{}, err
	}
	recipe, ok := loaded.(RecipePolicy)
	if !ok {
		return RecipePolicy{}, fmt.Errorf("recipe policy load returned %T", loaded)
	}
	s.loaded.Add(profileKey, recipe)
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
	set, err := readSelectionSetView(ctx, s.store, current.SelectionSet)
	if err != nil {
		return RecipePolicy{}, err
	}
	selection, exists := set.Profiles[profileKey]
	if !exists {
		return RecipePolicy{}, ErrRecipeProfileNotActive
	}
	prepared, err := ReadPreparedSelection(ctx, s.store, s.target, profileKey, selection)
	if err != nil {
		return RecipePolicy{}, err
	}
	if diagnostics := prepared.Policy.CatalogDiagnostics(); len(diagnostics) != 0 {
		return RecipePolicy{}, errors.New("shadow profile policy contains arms absent from the worker catalog")
	}
	return RecipePolicy{ProfileKey: profileKey, Policy: prepared.PolicyReference, Roster: prepared.Policy}, nil
}
