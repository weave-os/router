package httputil

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type modelDiscoveryResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f modelDiscoveryResolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

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
		{"240.0.0.1:443", false},
		{"255.255.255.255:443", false},
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

func TestModelDiscoveryAddressClassification(t *testing.T) {
	for _, testCase := range []struct {
		address string
		public  bool
	}{
		{address: "8.8.8.8", public: true},
		{address: "2606:4700:4700::1111", public: true},
		{address: "127.0.0.1"},
		{address: "10.1.2.3"},
		{address: "172.16.0.1"},
		{address: "192.168.1.1"},
		{address: "169.254.169.254"},
		{address: "224.0.0.1"},
		{address: "0.0.0.0"},
		{address: "0.1.2.3"},
		{address: "100.64.0.1"},
		{address: "::"},
		{address: "::1"},
		{address: "fd00::1"},
		{address: "fe80::1"},
		{address: "ff02::1"},
		{address: "::ffff:127.0.0.1"},
		{address: "::ffff:169.254.169.254"},
		{address: "240.0.0.1"},
		{address: "255.255.255.255"},
		{address: "::ffff:240.0.0.1"},
	} {
		address := netip.MustParseAddr(testCase.address)
		assert.Equal(t, testCase.public, isPublicAddr(address), testCase.address)
	}
}

func TestModelDiscoveryRejectsRestrictedAndMixedDNSAnswersBeforeDial(t *testing.T) {
	var sinkRequests atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sinkRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	target := mustServerAddress(t, sink.URL)

	for name, addresses := range map[string][]netip.Addr{
		"loopback":          {netip.MustParseAddr("127.0.0.1")},
		"private":           {netip.MustParseAddr("10.1.2.3")},
		"link local":        {netip.MustParseAddr("169.254.169.254")},
		"multicast":         {netip.MustParseAddr("224.0.0.1")},
		"unspecified":       {netip.MustParseAddr("0.0.0.0")},
		"zero network":      {netip.MustParseAddr("0.1.2.3")},
		"carrier grade NAT": {netip.MustParseAddr("100.64.0.1")},
		"mapped loopback":   {netip.MustParseAddr("::ffff:127.0.0.1")},
		"mixed answers":     {netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("127.0.0.1")},
	} {
		t.Run(name, func(t *testing.T) {
			var dialCalls atomic.Int32
			client, err := NewModelDiscoveryClient("",
				WithModelDiscoveryResolver(modelDiscoveryResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
					return addresses, nil
				})),
				WithModelDiscoveryDialer(func(ctx context.Context, network, _ string) (net.Conn, error) {
					dialCalls.Add(1)
					return (&net.Dialer{}).DialContext(ctx, network, target)
				}),
			)
			require.NoError(t, err)

			response, requestErr := client.Get("http://models.example:" + serverPort(t, target) + "/models")
			if response != nil {
				response.Body.Close()
			}
			require.Error(t, requestErr)
			assert.ErrorIs(t, requestErr, providers.ErrModelDiscoveryDestination)
			assert.Zero(t, dialCalls.Load(), "a rejected answer must not reach the dialer")
			assert.Zero(t, sinkRequests.Load(), "a rejected answer must not reach the controlled sink")
		})
	}
}

func TestModelDiscoveryDialsValidatedAddressWithoutChangingTheRequestOrigin(t *testing.T) {
	var receivedHost string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		receivedHost = request.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	target := mustServerAddress(t, sink.URL)
	port := serverPort(t, target)
	client, err := NewModelDiscoveryClient("",
		WithModelDiscoveryResolver(modelDiscoveryResolverFunc(func(_ context.Context, _, host string) ([]netip.Addr, error) {
			assert.Equal(t, "models.example", host)
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		})),
		WithModelDiscoveryDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
			assert.Equal(t, net.JoinHostPort("203.0.113.10", port), address)
			return (&net.Dialer{}).DialContext(ctx, network, target)
		}),
	)
	require.NoError(t, err)

	response, err := client.Get("http://models.example:" + port + "/models")
	require.NoError(t, err)
	response.Body.Close()
	assert.Equal(t, "models.example:"+port, receivedHost)
}

func TestModelDiscoveryPrivateOriginExceptionIsExact(t *testing.T) {
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	target := mustServerAddress(t, sink.URL)
	port := serverPort(t, target)
	origin := "http://models.internal:" + port
	var dialCalls atomic.Int32
	client, err := NewModelDiscoveryClient(origin,
		WithModelDiscoveryResolver(modelDiscoveryResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		})),
		WithModelDiscoveryDialer(func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialCalls.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, target)
		}),
	)
	require.NoError(t, err)

	response, err := client.Get(origin + "/gateway/models?limit=1000")
	require.NoError(t, err)
	response.Body.Close()
	assert.Equal(t, int32(1), dialCalls.Load())

	otherPort := strconv.Itoa(mustPortNumber(t, port) + 1)
	response, err = client.Get("http://models.internal:" + otherPort + "/models")
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, providers.ErrModelDiscoveryDestination)
	assert.Equal(t, int32(1), dialCalls.Load(), "a different origin must not inherit the exception")
}

func TestModelDiscoveryRevalidatesDNSOnEachNewConnection(t *testing.T) {
	var sinkRequests atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sinkRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	target := mustServerAddress(t, sink.URL)
	var lookups atomic.Int32
	client, err := NewModelDiscoveryClient("",
		WithModelDiscoveryResolver(modelDiscoveryResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			if lookups.Add(1) == 1 {
				return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		})),
		WithModelDiscoveryDialer(func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, target)
		}),
	)
	require.NoError(t, err)
	requestURL := "http://models.example:" + serverPort(t, target) + "/models"

	firstRequest, err := http.NewRequest(http.MethodGet, requestURL, nil)
	require.NoError(t, err)
	firstRequest.Close = true
	response, err := client.Do(firstRequest)
	require.NoError(t, err)
	response.Body.Close()
	response, err = client.Get(requestURL)
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, providers.ErrModelDiscoveryDestination)
	assert.Equal(t, int32(1), sinkRequests.Load())
}

func TestModelDiscoveryIgnoresAmbientProxyConfiguration(t *testing.T) {
	var proxyRequests atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxyServer.Close()
	t.Setenv("HTTP_PROXY", proxyServer.URL)
	t.Setenv("HTTPS_PROXY", proxyServer.URL)
	t.Setenv("NO_PROXY", "")
	// No egress-mode override here on purpose: NewModelDiscoveryClient sets
	// transport.Proxy = nil unconditionally, so discovery ignores an ambient
	// proxy in every deployment mode.

	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	target := mustServerAddress(t, sink.URL)
	client, err := NewModelDiscoveryClient("",
		WithModelDiscoveryResolver(modelDiscoveryResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		})),
		WithModelDiscoveryDialer(func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, target)
		}),
	)
	require.NoError(t, err)

	response, err := client.Get("http://models.example:" + serverPort(t, target) + "/models")
	require.NoError(t, err)
	response.Body.Close()
	assert.Zero(t, proxyRequests.Load())
}

func TestModelDiscoveryPrivateOriginConfigurationValidation(t *testing.T) {
	for name, configured := range map[string]string{
		"path":              "https://gateway.internal/models",
		"query":             "https://gateway.internal?mode=list",
		"fragment":          "https://gateway.internal#models",
		"wildcard":          "https://*.internal",
		"CIDR":              "https://10.0.0.0/8",
		"userinfo":          "https://user@gateway.internal",
		"empty port":        "https://gateway.internal:",
		"zero port":         "https://gateway.internal:0",
		"out of range port": "https://gateway.internal:65536",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewModelDiscoveryClient(configured)
			assert.Error(t, err)
			assert.NotContains(t, err.Error(), configured)
		})
	}

	httpsURL, err := url.Parse("https://gateway.internal/path")
	require.NoError(t, err)
	origin, err := canonicalDiscoveryOrigin(httpsURL)
	require.NoError(t, err)
	assert.Equal(t, "https://gateway.internal:443", origin)
}

func mustServerAddress(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	return parsed.Host
}

func serverPort(t *testing.T, address string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	require.NoError(t, err)
	return port
}

func mustPortNumber(t *testing.T, port string) int {
	t.Helper()
	portNumber, err := strconv.Atoi(port)
	require.NoError(t, err)
	return portNumber
}
