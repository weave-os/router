package router_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

var (
	testArtifactSHA = strings.Repeat("a", 64)
	testRosterSHA   = strings.Repeat("b", 64)
)

func TestParsePolicyPinAcceptsArtifactAtRoster(t *testing.T) {
	pin, err := router.ParsePolicyPin(" " + strings.ToUpper(testArtifactSHA) + "@" + testRosterSHA + " ")

	require.NoError(t, err)
	assert.Equal(t, testArtifactSHA, pin.ArtifactSHA256, "digests normalise to lowercase")
	assert.Equal(t, testRosterSHA, pin.RosterSHA256)
	assert.Equal(t, testArtifactSHA+"@"+testRosterSHA, pin.String())
}

func TestParsePolicyPinRejectsMalformedValues(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":            "",
		"no separator":     testArtifactSHA,
		"short artifact":   "abc@" + testRosterSHA,
		"short roster":     testArtifactSHA + "@abc",
		"non hex":          strings.Repeat("z", 64) + "@" + testRosterSHA,
		"extra separator":  testArtifactSHA + "@" + testRosterSHA + "@" + testRosterSHA,
		"missing roster":   testArtifactSHA + "@",
		"missing artifact": "@" + testRosterSHA,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := router.ParsePolicyPin(raw)
			assert.ErrorIs(t, err, router.ErrPolicyPinMalformed)
		})
	}
}

func TestHonouredPolicyPinRequiresAuthorization(t *testing.T) {
	pin := router.PolicyPin{ArtifactSHA256: testArtifactSHA, RosterSHA256: testRosterSHA}

	_, honoured := router.HonouredPolicyPin(context.Background())
	assert.False(t, honoured, "no header means no pin")

	unauthorized := router.WithPolicyPinRequest(context.Background(), router.PolicyPinRequest{Pin: pin})
	_, honoured = router.HonouredPolicyPin(unauthorized)
	assert.False(t, honoured, "an unauthorized pin must not steer routing")
	request, requested := router.PolicyPinRequestFrom(unauthorized)
	assert.True(t, requested)
	assert.Equal(t, pin, request.Pin)

	authorized := router.WithPolicyPinRequest(context.Background(), router.PolicyPinRequest{Pin: pin, Authorized: true})
	got, honoured := router.HonouredPolicyPin(authorized)
	assert.True(t, honoured)
	assert.Equal(t, pin, got)
}
