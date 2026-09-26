package policyregistry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func TestServingControllerFailureAuditIncludesProposalAndKnownTarget(t *testing.T) {
	for _, test := range []struct {
		name            string
		message         string
		prepare         bool
		rollbackScope   bool
		missingProposal bool
		mutate          func(*servingMemoryStore)
	}{
		{name: "proposal read", prepare: true, missingProposal: true, message: "Failed to read immutable proposal for serving preparation"},
		{name: "target read", prepare: true, message: "Failed to read authoritative target for serving preparation", mutate: func(store *servingMemoryStore) { store.readErr = errors.New("storage unavailable") }},
		{name: "evidence validation", prepare: true, message: "Serving destination validation blocked preparation", mutate: func(store *servingMemoryStore) { delete(store.artifacts, artifactRef("evidence")) }},
		{name: "rollback source", rollbackScope: true, message: "Serving rollback source validation rejected"},
		{name: "state write", message: "Serving activation CAS failed; keep the proposal for outcome reconciliation", mutate: func(store *servingMemoryStore) { store.casErr = errors.New("storage unavailable") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _, set := controllerFixture(t)
			snapshot := policyregistry.ServingStateSnapshot{}
			if test.rollbackScope {
				snapshot, _ = storedActivateFixture(t, store, snapshot, set, servingEpoch)
				snapshot, _ = storedActivateFixture(t, store, snapshot, variantSet(t, store, set, "worker-0002"), servingEpoch)
				store.states[set.Target] = snapshot
			}
			proposal := storedProposal(t, store, snapshot, set, servingEpoch)
			if test.rollbackScope {
				proposal.Scope = policyregistry.ChangeRollback
				release := *store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
				release.RouterImageDigest = "sha256:" + strings.Repeat("9", 64)
				proposal.SourceCandidate = foldCandidate(t, store, store.publish(t, policyregistry.ServingReleases, release))
			}
			ref := store.publishArtifact(t, policyregistry.ServingProposal, proposal)
			if test.missingProposal {
				delete(store.objects, ref)
			}
			if test.mutate != nil {
				test.mutate(store)
			}
			clock := func() time.Time { return servingEpoch }
			var audit bytes.Buffer
			validator := preparedValidator(func(context.Context, policyregistry.PreparedSelection) error { return nil })
			controller, err := policyregistry.NewServingController(store, validator, clock, slog.New(slog.NewJSONHandler(&audit, nil)))
			require.NoError(t, err)
			if test.prepare {
				_, err = controller.Prepare(context.Background(), ref)
			} else {
				_, err = controller.Activate(context.Background(), ref, "workflow")
			}
			require.Error(t, err)
			var entry map[string]any
			require.NoError(t, json.NewDecoder(&audit).Decode(&entry))
			require.Equal(t, test.message, entry["msg"])
			require.Equal(t, ref.SHA256, entry["proposal_sha256"])
			if !test.missingProposal {
				require.Equal(t, string(proposal.Target), entry["target"])
				require.Equal(t, proposal.Actor, entry["operator"])
			}
			if !test.prepare {
				require.Equal(t, "workflow", entry["workflow_actor"])
			}
			require.Equal(t, snapshot.Generation, store.states[set.Target].Generation)
		})
	}
}
