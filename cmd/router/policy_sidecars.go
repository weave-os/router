package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"weave-os/router/internal/config"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

const maxConfiguredPolicySidecars = 16

var configuredPolicyStrategyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

var reservedPolicyStrategies = map[router.Strategy]struct{}{
	router.StrategyLLMClassifier: {},
	router.StrategyRL:            {},
	router.StrategyHMM:           {},
	router.StrategyHMMEmbedding:  {},
	router.StrategyHMMBeta:       {},
	router.StrategyHMMQwen:       {},
	router.StrategyDecisionModel: {},
}

func isReservedPolicyStrategy(strategy router.Strategy) bool {
	_, reserved := reservedPolicyStrategies[strategy]
	return reserved
}

type policySidecarEndpoint struct {
	Origin          string `json:"origin"`
	PolicySHA256    string `json:"policy_sha256"`
	AllowHTTPBearer bool   `json:"allow_http_bearer"`
}

func (endpoint *policySidecarEndpoint) UnmarshalJSON(data []byte) error {
	if data[0] == '"' {
		return json.Unmarshal(data, &endpoint.Origin)
	}
	type endpointFields policySidecarEndpoint
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode((*endpointFields)(endpoint)); err != nil {
		return err
	}
	if endpoint.PolicySHA256 == "" {
		return fmt.Errorf("endpoint object requires policy_sha256")
	}
	return nil
}

type policySidecarSettings struct {
	endpoints    string
	authModes    string
	bearerTokens string
	timeout      time.Duration
}

func configuredPolicySidecarsFromEnv(deployed, availableProviders map[string]struct{}, logger *slog.Logger) ([]policy.StrategySpec, error) {
	return buildConfiguredPolicySidecars(
		context.Background(),
		policySidecarSettings{
			endpoints:    config.GetOr("ROUTER_POLICY_SIDECARS", ""),
			authModes:    config.GetOr("ROUTER_POLICY_SIDECAR_AUTH", ""),
			bearerTokens: config.GetOr("ROUTER_POLICY_SIDECAR_BEARER_TOKENS", ""),
			timeout:      parseEnvDurationMs("ROUTER_POLICY_SIDECAR_TIMEOUT_MS", policyclient.DefaultTimeout),
		},
		deployed,
		availableProviders,
		nil,
		logger,
	)
}

func buildConfiguredPolicySidecars(
	ctx context.Context,
	settings policySidecarSettings,
	deployed, availableProviders map[string]struct{},
	httpClient *http.Client,
	logger *slog.Logger,
) ([]policy.StrategySpec, error) {
	endpoints, err := parsePolicySidecarEndpoints(settings.endpoints)
	if err != nil {
		return nil, err
	}
	if len(endpoints) == 0 {
		switch {
		case strings.TrimSpace(settings.authModes) != "":
			return nil, fmt.Errorf("ROUTER_POLICY_SIDECAR_AUTH requires ROUTER_POLICY_SIDECARS")
		case strings.TrimSpace(settings.bearerTokens) != "":
			return nil, fmt.Errorf("ROUTER_POLICY_SIDECAR_BEARER_TOKENS requires ROUTER_POLICY_SIDECARS")
		default:
			return nil, nil
		}
	}
	knownStrategies := make(map[router.Strategy]struct{}, len(endpoints))
	for strategy := range endpoints {
		knownStrategies[strategy] = struct{}{}
	}
	authModes, err := parseConfiguredPolicySidecarSettings(settings.authModes, "ROUTER_POLICY_SIDECAR_AUTH", knownStrategies)
	if err != nil {
		return nil, err
	}
	tokens, err := configuredPolicyTokens(settings.bearerTokens, endpoints, knownStrategies, authModes)
	if err != nil {
		return nil, err
	}

	registrations := make([]policy.StrategySpec, 0, len(endpoints))
	for _, strategy := range slices.Sorted(maps.Keys(endpoints)) {
		endpoint := endpoints[strategy]
		pin := policyclient.WithExpectedPolicySHA256(endpoint.PolicySHA256)
		var client *policyclient.Client
		if token := tokens[strategy]; token != "" {
			client = policyclient.NewBearer(endpoint.Origin, token, httpClient, settings.timeout, pin)
		} else {
			client, err = buildConfiguredPolicyClient(endpoint.Origin, authModes[strategy], settings.timeout, httpClient, pin)
			if err != nil {
				return nil, fmt.Errorf("build %s policy sidecar client: %w", strategy, err)
			}
		}
		capabilities, err := discoverPolicySidecarCapabilities(ctx, strategy, endpoint, client, settings.timeout, logger)
		if err != nil {
			return nil, err
		}
		resolver := policy.NewResolver(
			deployed,
			availableProviders,
			func(model catalog.Model) string { return model.ID },
			policy.ManagedProviderPolicy(),
		)
		unavailable := fmt.Errorf("%s policy router unavailable: %w", strategy, router.ErrStrategyUnavailable)
		adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
			Strategy:    strategy,
			Unavailable: unavailable,
		}, client, resolver).WithCapabilities(capabilities)
		registrations = append(registrations, policy.StrategySpec{
			Strategy:     strategy,
			Router:       adapter,
			Unavailable:  unavailable,
			Capabilities: capabilities,
		})
	}
	return registrations, nil
}

func parsePolicySidecarEndpoints(raw string) (map[router.Strategy]policySidecarEndpoint, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var configured map[string]policySidecarEndpoint
	if err := json.Unmarshal([]byte(raw), &configured); err != nil {
		return nil, fmt.Errorf("parse ROUTER_POLICY_SIDECARS: %w", err)
	}
	if len(configured) > maxConfiguredPolicySidecars {
		return nil, fmt.Errorf("ROUTER_POLICY_SIDECARS has %d entries; maximum is %d", len(configured), maxConfiguredPolicySidecars)
	}
	endpoints := make(map[router.Strategy]policySidecarEndpoint, len(configured))
	for _, configuredName := range slices.Sorted(maps.Keys(configured)) {
		strategy := router.Strategy(strings.ToLower(strings.TrimSpace(configuredName)))
		if !configuredPolicyStrategyPattern.MatchString(string(strategy)) {
			return nil, fmt.Errorf("ROUTER_POLICY_SIDECARS strategy %q must match %s", strategy, configuredPolicyStrategyPattern)
		}
		if isReservedPolicyStrategy(strategy) {
			return nil, fmt.Errorf("ROUTER_POLICY_SIDECARS strategy %q is reserved", strategy)
		}
		if _, duplicate := endpoints[strategy]; duplicate {
			return nil, fmt.Errorf("ROUTER_POLICY_SIDECARS strategy %q is duplicated after normalization", strategy)
		}
		endpoint := configured[configuredName]
		endpoint.Origin = strings.TrimSpace(endpoint.Origin)
		if err := endpoint.validate(); err != nil {
			return nil, fmt.Errorf("ROUTER_POLICY_SIDECARS strategy %q: %w", strategy, err)
		}
		endpoints[strategy] = endpoint
	}
	return endpoints, nil
}

func (endpoint policySidecarEndpoint) validate() error {
	parsed, err := url.Parse(endpoint.Origin)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("requires an HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("requires a credential-free URL without query or fragment")
	}
	if digest := endpoint.PolicySHA256; digest != "" {
		if _, err := hex.DecodeString(digest); err != nil || len(digest) != 64 || strings.ToLower(digest) != digest {
			return fmt.Errorf("invalid policy_sha256")
		}
	}
	return nil
}

func discoverPolicySidecarCapabilities(
	ctx context.Context, strategy router.Strategy, endpoint policySidecarEndpoint,
	client *policyclient.Client, timeout time.Duration, logger *slog.Logger,
) (policy.Capabilities, error) {
	capabilityCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	capabilities, err := client.Capabilities(capabilityCtx)
	if endpoint.PolicySHA256 != "" {
		if err != nil {
			return policy.Capabilities{}, fmt.Errorf("discover %s policy capabilities: %w", strategy, err)
		}
		if capabilities.SchemaVersion != policy.SchemaVersionV1 || !capabilities.AuthoritativePerTurnSelection {
			return policy.Capabilities{}, fmt.Errorf("%s pinned policy sidecar must advertise authoritative policy_router_v1 selection", strategy)
		}
	}
	if err != nil {
		logger.Warn("Policy sidecar capabilities unavailable at boot",
			"strategy", strategy, "sidecar_url", endpoint.Origin, "err", err)
	}
	return capabilities, nil
}

func parseConfiguredPolicySidecarSettings(
	raw, setting string,
	configured map[router.Strategy]struct{},
) (map[router.Strategy]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("parse %s: expected an object mapping strategies to strings", setting)
	}
	authModes := make(map[router.Strategy]string, len(values))
	for configuredName, authMode := range values {
		strategy := router.Strategy(strings.ToLower(strings.TrimSpace(configuredName)))
		if _, ok := configured[strategy]; !ok {
			return nil, fmt.Errorf("%s contains a strategy that is not configured", setting)
		}
		if _, duplicate := authModes[strategy]; duplicate {
			return nil, fmt.Errorf("%s contains a duplicated strategy after normalization", setting)
		}
		authModes[strategy] = authMode
	}
	return authModes, nil
}
