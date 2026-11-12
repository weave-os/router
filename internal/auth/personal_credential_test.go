package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

type recordingPersonalIssuer struct {
	created []auth.CreateAPIKeyParams
	rotated []string
}

func (r *recordingPersonalIssuer) CreateProjected(_ context.Context, key auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	r.created = append(r.created, key)
	return &auth.APIKey{ID: "personal-key", InstallationID: key.InstallationID, CredentialSubjectID: "subject-1", Scope: key.Scope}, nil
}

func (r *recordingPersonalIssuer) RotateProjected(_ context.Context, subjectID, previousKeyID string, replacement auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	r.rotated = append(r.rotated, subjectID+"/"+previousKeyID)
	return &auth.APIKey{ID: "rotated-key", InstallationID: replacement.InstallationID, CredentialSubjectID: subjectID, Scope: replacement.Scope}, nil
}

func TestIssueScopedAPIKey_PersonalIssuerOwnsRoutingKeys(t *testing.T) {
	repo := newStatefulAPIKeyRepo()
	issuer := &recordingPersonalIssuer{}
	svc := auth.NewService(nil, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).WithPersonalCredentialIssuer(issuer)

	key, raw, err := svc.IssueScopedAPIKey(context.Background(), "inst-1", auth.ScopeRouting, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "subject-1", key.CredentialSubjectID)
	require.Len(t, issuer.created, 1)
	hash, _, _ := auth.APITokenFingerprint(raw)
	assert.Equal(t, hash, issuer.created[0].KeyHash)
	assert.Equal(t, 0, repo.createCount())

	_, _, err = svc.IssueScopedAPIKey(context.Background(), "inst-1", auth.ScopeAnalyticsRead, nil, nil)
	require.NoError(t, err)
	assert.Len(t, issuer.created, 1)
	assert.Equal(t, 1, repo.createCount())
}

func setStoredCredentialSubject(t *testing.T, repo *statefulAPIKeyRepo, keyID, subjectID string) {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, key := range repo.keys {
		if key.ID == keyID {
			key.CredentialSubjectID = subjectID
			return
		}
	}
	require.FailNow(t, "stored API key not found", keyID)
}

func TestRotateAPIKey_PersonalKeyKeepsSubject(t *testing.T) {
	repo := newStatefulAPIKeyRepo()
	issuer := &recordingPersonalIssuer{}
	svc := auth.NewService(nil, repo, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now)
	seed, _, err := svc.IssueAPIKey(context.Background(), "inst-1", nil, nil)
	require.NoError(t, err)
	setStoredCredentialSubject(t, repo, seed.ID, "subject-1")

	_, _, err = svc.RotateAPIKey(context.Background(), "inst-1", seed.ID, nil)
	require.ErrorIs(t, err, auth.ErrPersonalCredentialRequired)

	svc.WithPersonalCredentialIssuer(issuer)
	rotated, _, err := svc.RotateAPIKey(context.Background(), "inst-1", seed.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, "subject-1", rotated.CredentialSubjectID)
	assert.Equal(t, []string{"subject-1/" + seed.ID}, issuer.rotated)
}
