package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// RequestIdentityRepository resolves the credential subject Weave projected
// for an email inside one installation. The router stores no accounts, so this
// projection is the only way an inbound request email becomes a person.
type RequestIdentityRepository interface {
	GetSubscriberForEmail(ctx context.Context, installationID, email string) (string, error)
}

const (
	requestIdentityCacheSize = 8192
	// requestIdentityCacheTTL bounds how long a withdrawn projection keeps
	// serving the person it used to name. Every inference request resolves an
	// identity, so an uncached lookup per turn would put the projection table
	// on the hot path.
	requestIdentityCacheTTL = 30 * time.Second
)

// WithRequestIdentities wires per-caller identity resolution. Without it, a
// request's subscriptions and allowance stay bound to the key it authenticated
// with, which is the pre-shared-key behaviour.
func (s *Service) WithRequestIdentities(repo RequestIdentityRepository) *Service {
	s.requestIdentities = repo
	s.requestIdentityCache = expirable.NewLRU[string, string](requestIdentityCacheSize, nil, requestIdentityCacheTTL)
	return s
}

// SubscriptionOwnerForRequest resolves the person whose subscriptions and
// included allowance this request draws on. A routing key may be shared across
// an organization, so the caller — the normalized email the client sends — owns
// the pool, not the key that authenticated the call.
//
// The header is client-asserted, so resolution is confined to subjects Weave
// projected into this installation: an unknown address, or one belonging to
// another organization, resolves to nothing and leaves the key's own identity
// in place rather than silently selecting someone else's pool.
func (s *Service) SubscriptionOwnerForRequest(ctx context.Context, key *APIKey, email string) (SubscriptionOwner, error) {
	return s.subscriptionOwnerForRequest(ctx, key, email, true)
}

// SubscriptionOwnerForRequestUncached resolves the caller against the live
// projection, ignoring the cache. Managing a subscription account is rare and
// destructive, so it reads the projection Weave has now rather than the one it
// had up to a TTL ago, and a withdrawn identity stops naming its former owner
// immediately.
func (s *Service) SubscriptionOwnerForRequestUncached(ctx context.Context, key *APIKey, email string) (SubscriptionOwner, error) {
	return s.subscriptionOwnerForRequest(ctx, key, email, false)
}

func (s *Service) subscriptionOwnerForRequest(ctx context.Context, key *APIKey, email string, cached bool) (SubscriptionOwner, error) {
	owner := SubscriptionOwnerForKey(key)
	if key == nil || s.requestIdentities == nil || s.requestIdentityCache == nil ||
		email == "" || key.InstallationID == "" {
		return owner, nil
	}
	subscriberID, err := s.resolveRequestSubscriber(ctx, key.InstallationID, email, cached)
	if err != nil {
		return owner, err
	}
	if subscriberID != "" {
		owner.SubscriberID = subscriberID
	}
	return owner, nil
}

func (s *Service) resolveRequestSubscriber(ctx context.Context, installationID, email string, cached bool) (string, error) {
	cacheKey := installationID + "|" + email
	if cached {
		if subscriberID, hit := s.requestIdentityCache.Get(cacheKey); hit {
			return subscriberID, nil
		}
	}
	subscriberID, err := s.requestIdentities.GetSubscriberForEmail(ctx, installationID, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if errors.Is(err, sql.ErrNoRows) {
		subscriberID = ""
	}
	// An unprojected address is cached too: most installations send addresses
	// that will never resolve, and each would otherwise query per request.
	s.requestIdentityCache.Add(cacheKey, subscriberID)
	return subscriberID, nil
}
