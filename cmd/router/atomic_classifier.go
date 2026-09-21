package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
)

type atomicClassifierConfig struct {
	Release               string      `json:"release"`
	ReleaseSHA256         string      `json:"release_sha256"`
	Endpoint              string      `json:"endpoint"`
	SelectionPolicyFile   string      `json:"selection_policy_file"`
	SelectionPolicySHA256 string      `json:"selection_policy_sha256"`
	InstallationIDs       []uuid.UUID `json:"installation_ids"`
}

// Legacy/isolated composition only. Managed ingress must first gain an attested
// Modal binding; this config must not bypass the controller's release ownership.
func configureAtomicClassifier(service *proxy.Service, pool *pgxpool.Pool, providers map[string]struct{}) error {
	path := os.Getenv("ROUTER_LLM_CLASSIFIER_CONFIG")
	if path == "" {
		return nil
	}
	if managedServingEnabled() {
		return errors.New("llm classifier requires managed Modal release integration before enabling managed serving")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config atomicClassifierConfig
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("classifier configuration contains trailing JSON")
	}
	roster, err := rosterdata.Load(config.SelectionPolicyFile)
	if err != nil {
		return err
	}
	escalationClasses := []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}
	if roster.SHA256 != config.SelectionPolicySHA256 || roster.SchemaVersion != rosterdata.SchemaVersionPolicyV1 || !slices.Equal(roster.ClassOrder, escalationClasses) {
		return errors.New("classifier selection policy digest, schema or taxonomy mismatch")
	}
	client, err := policyclient.NewAtomicClassifier(config.Endpoint, os.Getenv("ROUTER_LLM_CLASSIFIER_BEARER"), config.Release, config.ReleaseSHA256, nil)
	if err != nil {
		return err
	}
	if err := service.WithClassifierSessions(proxy.ClassifierSessionConfig{Release: config.Release, ReleaseSHA256: config.ReleaseSHA256, SelectionPolicySHA256: config.SelectionPolicySHA256, SigningKey: []byte(os.Getenv("ROUTER_LLM_CLASSIFIER_SIGNING_KEY")), InstallationIDs: config.InstallationIDs}, postgres.NewClassifierSessionRepo(pool), client); err != nil {
		return err
	}
	capabilities := policy.Capabilities{SchemaVersion: policy.SchemaVersionV4, HonorsPreferredModels: true, HonorsQualityPriceBias: true, HonorsClusterModelLists: true, AuthoritativePerTurnSelection: true}
	resolver := policy.NewResolver(catalog.HMMRoutingTargetSet(providers), providers, armid.ForModel, policy.ManagedProviderPolicy())
	classifierRouter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyLLMClassifier, Unavailable: router.ErrClassifierUnavailable,
		ClassifierArtifactID: config.Release, ClassifierArtifactSHA256: config.ReleaseSHA256,
		SelectionPolicyReleaseID: config.Release, SelectionPolicySHA256: config.SelectionPolicySHA256},
		policy.AtomicClassifierFacts{Release: config.Release, ReleaseSHA256: config.ReleaseSHA256}, resolver).
		WithCapabilities(capabilities).WithArmSelector(selection.Selector(roster))
	service.WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyLLMClassifier, Router: classifierRouter, Unavailable: router.ErrClassifierUnavailable, Capabilities: capabilities})
	return nil
}
