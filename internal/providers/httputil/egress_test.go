package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestrictedTransportRefusesANonPublicDestination(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	restricted := &http.Client{Transport: newTransport(time.Second, time.Second, time.Second, true)}
	_, err := restricted.Get(upstream.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRestrictedDestination)

	unrestricted := &http.Client{Transport: newTransport(time.Second, time.Second, time.Second, false)}
	resp, err := unrestricted.Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRestrictDestinationClassifiesAddresses(t *testing.T) {
	for _, tc := range []struct {
		address string
		allowed bool
	}{
		{"8.8.8.8:443", true},
		{"[2606:4700:4700::1111]:443", true},
		{"169.254.169.254:80", false},
		{"0.0.0.1:80", false},
		{"[::ffff:0.0.0.1]:80", false},
		{"[::ffff:169.254.169.254]:80", false},
		{"127.0.0.1:8080", false},
		{"10.1.2.3:443", false},
		{"192.168.1.1:443", false},
		{"172.16.0.1:443", false},
		{"100.64.0.1:443", false},
		{"[fd00::1]:443", false},
		{"[fe80::1]:443", false},
		{"0.0.0.0:443", false},
		{"224.0.0.1:443", false},
	} {
		err := restrictDestination("tcp", tc.address, nil)
		if tc.allowed {
			assert.NoError(t, err, tc.address)
			continue
		}
		assert.ErrorIs(t, err, ErrRestrictedDestination, tc.address)
	}
}

func TestPublicDestinationsOnlyFollowsDeploymentModeUnlessOverridden(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		override string
		want     bool
	}{
		{mode: "managed", want: true},
		{mode: "selfhosted", want: false},
		{mode: "", want: false},
		{mode: "managed", override: "false", want: false},
		{mode: "selfhosted", override: "true", want: true},
		{mode: "managed", override: "nonsense", want: true},
	} {
		t.Setenv("ROUTER_DEPLOYMENT_MODE", tc.mode)
		t.Setenv(restrictUpstreamEgressEnv, tc.override)
		assert.Equal(t, tc.want, publicDestinationsOnlyFromEnv(), "mode=%q override=%q", tc.mode, tc.override)
	}
}

func TestRestrictedTransportIgnoresAnEnvironmentProxy(t *testing.T) {
	// Through a proxy the dialer connects to the proxy, so restrictDestination
	// would inspect the proxy's address and never the upstream's.
	restricted := newTransport(time.Second, time.Second, time.Second, true)
	assert.Nil(t, restricted.Proxy, "a restricted transport must not route through a proxy")

	unrestricted := newTransport(time.Second, time.Second, time.Second, false)
	assert.NotNil(t, unrestricted.Proxy, "an unrestricted transport still honors the environment proxy")
}
