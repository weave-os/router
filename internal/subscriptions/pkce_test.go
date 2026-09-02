package subscriptions_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"workweave/router/internal/subscriptions"
)

func TestPKCEChallengeRoundTrip(t *testing.T) {
	challenge, err := subscriptions.NewPKCEChallenge()
	require.NoError(t, err)
	require.NotEmpty(t, challenge.Verifier)
	require.NotEmpty(t, challenge.Challenge)
	require.NoError(t, subscriptions.VerifyPKCE(challenge.Verifier, challenge.Challenge))
	require.Error(t, subscriptions.VerifyPKCE("wrong", challenge.Challenge))
}
