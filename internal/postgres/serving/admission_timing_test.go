package serving

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
)

// forbiddenAdmissionAttributes are the identity-bearing fields the timing record must
// never carry; the measurement exists to size a cache, not to profile a caller.
var forbiddenAdmissionAttributes = []string{"installation_id", "api_key_id", "key_id", "credential_identity", "subject_id", "conversation_digest", "client_session_id", "session_id", "binding", "err"}

func decodeRecords(t *testing.T, raw *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw.String()), "\n") {
		if line == "" {
			continue
		}
		record := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		records = append(records, record)
	}
	return records
}

func TestAdmissionTimingRecordCarriesDurationsWithoutIdentity(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)))
	timing := admissionTiming{
		started:      time.Now().Add(-25 * time.Millisecond),
		transaction:  20 * time.Millisecond,
		lockWait:     3 * time.Millisecond,
		decide:       8 * time.Millisecond,
		projection:   4 * time.Millisecond,
		registry:     policyregistry.AdmissionTimings{StateRead: 5 * time.Millisecond, SelectionSetRead: 2500 * time.Microsecond, SelectionSetReads: 2},
		persistent:   true,
		outcome:      admissionDenied,
		target:       policyregistry.TargetStable,
		activationID: "activation-1",
	}

	timing.emit(ctx)

	records := decodeRecords(t, &logs)
	require.Len(t, records, 1)
	record := records[0]
	assert.Equal(t, admissionTimingRecord, record["msg"])
	assert.Equal(t, slog.LevelInfo.String(), record["level"])
	assert.Equal(t, 20.0, record[attrAdmissionTransaction])
	assert.Equal(t, 3.0, record[attrAdmissionLockWait])
	assert.Equal(t, 5.0, record[attrAdmissionStateRead])
	assert.Equal(t, 2.5, record[attrAdmissionSelectionSetRead])
	assert.Equal(t, 2.0, record[attrAdmissionSelectionSetReads])
	assert.Equal(t, 8.0, record[attrAdmissionDecide])
	assert.Equal(t, 4.0, record[attrAdmissionProjection])
	assert.Equal(t, true, record[attrAdmissionPersistent])
	assert.Equal(t, string(admissionDenied), record[attrAdmissionOutcome])
	assert.Equal(t, string(policyregistry.TargetStable), record[attrAdmissionTarget])
	assert.Equal(t, "activation-1", record[attrAdmissionActivation])
	total, ok := record[attrAdmissionTotal].(float64)
	require.True(t, ok)
	assert.GreaterOrEqual(t, total, 25.0)
	for _, attribute := range forbiddenAdmissionAttributes {
		assert.NotContains(t, record, attribute)
	}
}
