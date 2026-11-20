package subscription_enrollment_check_test

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
)

type seatReassignmentFixture struct {
	ctx            context.Context
	tx             pgx.Tx
	installationID uuid.UUID
	previousOwner  uuid.UUID
	signedInMember uuid.UUID
}

func newSeatReassignmentFixture(t *testing.T) seatReassignmentFixture {
	t.Helper()
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROUTER_TEST_DATABASE_URL is required for the seat reassignment test")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, parsed.Hostname(), "use an ephemeral loopback database")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	fixture := seatReassignmentFixture{ctx: ctx, tx: tx, installationID: uuid.New(), previousOwner: uuid.New(), signedInMember: uuid.New()}
	fixture.insertInstallation(t, fixture.installationID)
	for _, subject := range []uuid.UUID{fixture.previousOwner, fixture.signedInMember} {
		_, err = tx.Exec(ctx, "INSERT INTO router.credential_subjects (id, projection_complete) VALUES ($1, TRUE)", subject)
		require.NoError(t, err)
		_, err = tx.Exec(ctx,
			"INSERT INTO router.credential_subject_installations (subject_id, installation_id, access_enabled) VALUES ($1, $2, TRUE)",
			subject, fixture.installationID)
		require.NoError(t, err)
	}
	return fixture
}

func (f seatReassignmentFixture) insertInstallation(t *testing.T, installationID uuid.UUID) {
	t.Helper()
	_, err := f.tx.Exec(f.ctx,
		"INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, 'seat-reassignment-fixture')",
		installationID, installationID.String())
	require.NoError(t, err)
}

func (f seatReassignmentFixture) enroll(subject uuid.UUID, verifiedSignIn bool) (*auth.SubscriptionAccount, auth.SubscriptionUpsertKind, error) {
	return postgres.NewSubscriptionAccountRepo(f.tx).UpsertSubscriptionAccount(f.ctx, auth.CreateSubscriptionAccountParams{
		Owner:             auth.SubscriptionOwner{InstallationID: f.installationID.String(), SubscriberID: subject.String()},
		Provider:          auth.SubscriptionProviderClaude,
		ExternalAccountID: "claude-seat-" + f.installationID.String(),
		RefreshToken:      []byte("synthetic-refresh"),
		VerifiedSignIn:    verifiedSignIn,
	})
}

func TestVerifiedSignInReassignsSeatServingOnlyThisInstallation(t *testing.T) {
	fixture := newSeatReassignmentFixture(t)
	misfiled, _, err := fixture.enroll(fixture.previousOwner, false)
	require.NoError(t, err)

	_, _, err = fixture.enroll(fixture.signedInMember, false)
	require.Error(t, err, "a client-supplied token never moves another member's seat")

	moved, kind, err := fixture.enroll(fixture.signedInMember, true)
	require.NoError(t, err)
	require.Equal(t, auth.SubscriptionUpsertReassigned, kind)
	require.Equal(t, misfiled.ID, moved.ID, "the seat moves instead of being duplicated")
	require.Equal(t, fixture.signedInMember.String(), moved.SubscriberID)

	_, kind, err = fixture.enroll(fixture.signedInMember, true)
	require.NoError(t, err)
	require.Equal(t, auth.SubscriptionUpsertUpdated, kind, "the new owner signing in again is an ordinary update")
}

func TestVerifiedSignInKeepsSeatServingAnotherInstallation(t *testing.T) {
	fixture := newSeatReassignmentFixture(t)
	seat, _, err := fixture.enroll(fixture.previousOwner, false)
	require.NoError(t, err)
	otherInstallationID := uuid.New()
	fixture.insertInstallation(t, otherInstallationID)
	_, err = fixture.tx.Exec(fixture.ctx,
		"INSERT INTO router.model_router_subscription_account_installations (installation_id, subscription_account_id) VALUES ($1, $2)",
		otherInstallationID, seat.ID)
	require.NoError(t, err)

	_, _, err = fixture.enroll(fixture.signedInMember, true)
	require.Error(t, err, "a seat serving another installation never changes owner")

	kept, kind, err := fixture.enroll(fixture.previousOwner, false)
	require.NoError(t, err)
	require.Equal(t, auth.SubscriptionUpsertUpdated, kind)
	require.Equal(t, seat.ID, kept.ID)
	require.Equal(t, fixture.previousOwner.String(), kept.SubscriberID)
}
