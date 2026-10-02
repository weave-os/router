package iam

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

func TestAuthorizerRetainsAudienceTokenAfterStartupContextEnds(t *testing.T) {
	authorizer := NewAuthorizer(context.Background())
	sources, tokens := 0, 0
	authorizer.newSource = func(ctx context.Context, audience string) (oauth2.TokenSource, error) {
		sources++
		return tokenSourceFunc(func() (*oauth2.Token, error) {
			require.NoError(t, ctx.Err())
			tokens++
			return &oauth2.Token{AccessToken: audience, Expiry: time.Now().Add(time.Hour)}, nil
		}), nil
	}
	startupCtx, cancel := context.WithCancel(context.Background())
	token, err := authorizer.IdentityToken(startupCtx, "https://worker.example")
	require.NoError(t, err)
	require.Equal(t, "https://worker.example", token)
	cancel()
	token, err = authorizer.IdentityToken(context.Background(), "https://worker.example")
	require.NoError(t, err)
	require.Equal(t, "https://worker.example", token)
	require.Equal(t, 1, sources)
	require.Equal(t, 1, tokens)
	token, err = authorizer.IdentityToken(context.Background(), "https://other.example")
	require.NoError(t, err)
	require.Equal(t, "https://other.example", token)
	require.Equal(t, 2, sources)
}

func TestAuthorizerCanceledRequestDoesNotCancelSharedRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		authorizer := NewAuthorizer(context.Background())
		release := make(chan struct{})
		authorizer.newSource = func(context.Context, string) (oauth2.TokenSource, error) {
			return tokenSourceFunc(func() (*oauth2.Token, error) {
				<-release
				return &oauth2.Token{AccessToken: "retained", Expiry: time.Now().Add(time.Hour)}, nil
			}), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := authorizer.IdentityToken(ctx, "https://worker.example")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		close(release)
		token, err := authorizer.IdentityToken(context.Background(), "https://worker.example")
		require.NoError(t, err)
		require.Equal(t, "retained", token)
	})
}

func TestAuthorizerRetriesRejectedTokenSource(t *testing.T) {
	authorizer := NewAuthorizer(context.Background())
	unavailable := errors.New("identity unavailable")
	authorizer.newSource = func(context.Context, string) (oauth2.TokenSource, error) { return nil, unavailable }
	_, err := authorizer.IdentityToken(context.Background(), "https://worker.example")
	require.ErrorIs(t, err, unavailable)
	authorizer.newSource = func(context.Context, string) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "ready"}), nil
	}
	token, err := authorizer.IdentityToken(context.Background(), "https://worker.example")
	require.NoError(t, err)
	require.Equal(t, "ready", token)
}

func TestAuthorizerRefreshesTokensWhileRequestsDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		processCtx, stop := context.WithCancel(context.Background())
		defer stop()
		authorizer := NewAuthorizer(processCtx)
		tokens := 0
		authorizer.newSource = func(ctx context.Context, audience string) (oauth2.TokenSource, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return tokenSourceFunc(func() (*oauth2.Token, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				tokens++
				return &oauth2.Token{AccessToken: audience, Expiry: time.Now().Add(time.Minute)}, nil
			}), nil
		}
		_, err := authorizer.IdentityToken(context.Background(), "https://worker.example")
		require.NoError(t, err)
		stop()
		time.Sleep(time.Minute + time.Second)
		token, err := authorizer.IdentityToken(context.Background(), "https://worker.example")
		require.NoError(t, err)
		require.Equal(t, "https://worker.example", token)
		token, err = authorizer.IdentityToken(context.Background(), "https://other.example")
		require.NoError(t, err)
		require.Equal(t, "https://other.example", token)
		require.Equal(t, 3, tokens)
	})
}
