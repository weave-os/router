package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/compactioncheckpoint"
	"weave-os/router/internal/translate"
)

const compactionCheckpointTTL = 24 * time.Hour

func (s *Service) compactionCheckpointEligible(in compactionInput) bool {
	if s.compactionCheckpoints == nil || in.CredentialIdentity == "" || in.Endpoint == "" {
		return false
	}
	var zero [16]byte
	if in.SessionKey == zero {
		return false
	}
	return verifiedCompactionClient(in.ClientApp)
}

func (s *Service) compactionCheckpointPolicyDigest(ctx context.Context, in compactionInput, pol compactionPolicy) ([sha256.Size]byte, error) {
	identity := struct {
		ClientApp         string
		SummaryModel      string
		Policy            compactionPolicy
		AvailableModels   map[string]struct{}
		EnabledProviders  map[string]struct{}
		ExcludedModels    map[string]struct{}
		AllowedModels     map[string]struct{}
		GatewayProviders  map[string]struct{}
		CustomBindings    map[string][]string
		AutomaticExcluded map[string]struct{}
	}{
		ClientApp:         in.ClientApp,
		SummaryModel:      s.compactionModel,
		Policy:            pol,
		AvailableModels:   s.availableModels,
		EnabledProviders:  in.Scope.EnabledProviders,
		ExcludedModels:    in.Scope.ExcludedModels,
		AllowedModels:     in.Scope.AllowedModels,
		GatewayProviders:  in.Scope.GatewayProviders,
		CustomBindings:    in.Scope.CustomBindings,
		AutomaticExcluded: mergeExcludedModels(s.excludedModelsForRequest(ctx), s.globalAutomaticExcludedModels(ctx)),
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func (s *Service) reuseCompactionCheckpoint(ctx context.Context, env, original *translate.RequestEnvelope, in compactionInput, pol compactionPolicy, res *compactionResult, trigger int) bool {
	if !s.compactionCheckpointEligible(in) {
		return false
	}
	log := observability.FromContext(ctx)
	checkpoint, found, err := s.compactionCheckpoints.Get(ctx, in.CredentialIdentity, in.SessionKey, in.Endpoint)
	if err != nil {
		log.Warn("Compaction checkpoint unavailable", "reason", "store_failure", "err", err)
		return false
	}
	if !found {
		log.Debug("Compaction checkpoint miss", "reason", "not_found")
		return false
	}
	policyDigest, err := s.compactionCheckpointPolicyDigest(ctx, in, pol)
	if err != nil || checkpoint.PolicyDigest != policyDigest || checkpoint.ExpiresAt.Before(time.Now()) {
		log.Info("Compaction checkpoint miss", "reason", "policy_or_expiry_mismatch")
		return false
	}
	prefixDigest, err := original.CompactionPrefixDigest(checkpoint.Boundary)
	if err != nil || checkpoint.PrefixDigest != prefixDigest || strings.TrimSpace(checkpoint.Summary) == "" {
		log.Info("Compaction checkpoint miss", "reason", "prefix_mismatch")
		return false
	}
	boundaries := original.CompactionBoundaries()
	chunk, err := original.CompactionChunk(checkpoint.Boundary, boundaries[len(boundaries)-1], checkpoint.Summary)
	if err != nil {
		log.Info("Compaction checkpoint miss", "reason", "unsafe_boundary", "err", err)
		return false
	}
	chunk.ClearOldToolResults(pol.ToolResultKeep)
	estimate := chunk.ContextOverflowTokenEstimate() + in.OutputReserve
	if estimate > in.MaxWindow {
		log.Info("Compaction checkpoint miss", "reason", "window_exceeded", "needed", estimate)
		return false
	}
	*env = *chunk
	res.Applied = true
	res.Summarized = true
	res.CheckpointReused = true
	res.SummaryModel = checkpoint.Model
	res.FinalEstimate = estimate
	log.Info("Compaction checkpoint reused", "needed_after", estimate, "trigger", trigger)
	return true
}

func (s *Service) saveCompactionCheckpoint(ctx context.Context, original *translate.RequestEnvelope, in compactionInput, pol compactionPolicy, summary, model string, keepRecent int) {
	if !s.compactionCheckpointEligible(in) {
		return
	}
	boundary := original.CompactionTailBoundary(keepRecent)
	if boundary <= 0 {
		return
	}
	prefixDigest, err := original.CompactionPrefixDigest(boundary)
	if err != nil {
		return
	}
	policyDigest, err := s.compactionCheckpointPolicyDigest(ctx, in, pol)
	if err != nil {
		return
	}
	checkpoint := compactioncheckpoint.Checkpoint{
		CredentialIdentity: in.CredentialIdentity,
		SessionKey:         in.SessionKey,
		Endpoint:           in.Endpoint,
		PrefixDigest:       prefixDigest,
		PolicyDigest:       policyDigest,
		Boundary:           boundary,
		Summary:            summary,
		Model:              model,
		ExpiresAt:          time.Now().Add(compactionCheckpointTTL),
	}
	if err := s.compactionCheckpoints.Upsert(ctx, checkpoint); err != nil {
		observability.FromContext(ctx).Warn("Compaction checkpoint unavailable", "reason", "store_failure", "err", err)
		return
	}
	observability.FromContext(ctx).Info("Compaction checkpoint saved", "boundary", boundary)
}
