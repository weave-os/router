package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/router/sessionpin"
)

func TestTokenizerCalibrationUnreadableColumnDecodesEmpty(t *testing.T) {
	assert.Equal(t, sessionpin.TokenizerCalibration{}, tokenizerCalibration(nil))
	assert.Equal(t, sessionpin.TokenizerCalibration{}, tokenizerCalibration([]byte(`{"observations":`)))
}

func TestDemotionCooldownsUnreadableColumnDecodesNone(t *testing.T) {
	assert.Nil(t, demotionCooldowns(nil))
	assert.Nil(t, demotionCooldowns([]byte(`{"m":"not-a-time"}`)))
	assert.Nil(t, demotionCooldowns([]byte(`{}`)))
	until := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got := demotionCooldowns([]byte(`{"m":"2026-01-02T03:04:05Z"}`))
	require.Len(t, got, 1)
	assert.True(t, got["m"].Equal(until))
}

func TestMarshalModelAliasesStoresNullForNoAliases(t *testing.T) {
	for _, aliases := range []map[string]string{nil, {}} {
		encoded, err := marshalModelAliases(aliases)
		require.NoError(t, err)
		assert.Nil(t, encoded)
	}
	encoded, err := marshalModelAliases(map[string]string{"a": "b"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":"b"}`, string(encoded))
}

func TestUUIDStringPtrExportsNullForNullUUID(t *testing.T) {
	assert.Nil(t, uuidStringPtr(pgtype.UUID{}))
	id := uuid.New()
	got := uuidStringPtr(pgtype.UUID{Bytes: id, Valid: true})
	require.NotNil(t, got)
	assert.Equal(t, id.String(), *got)
}

func TestServedCostUSDIsZeroForSubscriptionServedTurns(t *testing.T) {
	micros := int64(2_500_000)
	assert.Nil(t, servedCostUSD(nil, true))
	served := servedCostUSD(&micros, true)
	require.NotNil(t, served)
	assert.InDelta(t, 0.0, *served, 0)
	billed := servedCostUSD(&micros, false)
	require.NotNil(t, billed)
	assert.InDelta(t, 2.5, *billed, 1e-9)
}

func TestParseOptionalUUIDRejectsMalformedID(t *testing.T) {
	_, ok := parseOptionalUUID("not-a-uuid")
	assert.False(t, ok)
	id := uuid.New()
	parsed, ok := parseOptionalUUID(id.String())
	assert.True(t, ok)
	assert.Equal(t, id, parsed)
}

func TestSubscriberOwnerIDRejectsOrganizationOwner(t *testing.T) {
	orgID := uuid.NewString()
	_, err := subscriberOwnerID(billing.Owner{Kind: billing.OwnerKindOrganization, OrganizationID: orgID})
	require.ErrorIs(t, err, billing.ErrOwnerKindUnsupported)
	subscriberID := uuid.New()
	got, err := subscriberOwnerID(billing.Owner{Kind: billing.OwnerKindSubscriber, SubscriberID: subscriberID.String()})
	require.NoError(t, err)
	assert.Equal(t, subscriberID, got)
}

func TestSubscriptionOwnerPredicateFailsClosedWithoutOwner(t *testing.T) {
	_, _, err := subscriptionOwnerPredicate(auth.SubscriptionOwner{})
	require.ErrorIs(t, err, auth.ErrSubscriptionAccountNotFound)
	_, _, err = subscriptionOwnerPredicate(auth.SubscriptionOwner{SubscriberID: "malformed"})
	require.ErrorIs(t, err, auth.ErrSubscriptionAccountNotFound)
	subscriberID, apiKeyID, err := subscriptionOwnerPredicate(auth.SubscriptionOwner{APIKeyID: uuid.NewString()})
	require.NoError(t, err)
	assert.False(t, subscriberID.Valid)
	assert.True(t, apiKeyID.Valid)
}
