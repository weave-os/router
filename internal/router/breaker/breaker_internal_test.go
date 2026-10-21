package breaker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// One-off strikes from many organizations' gateways are dropped once they
// age out, so a long-lived replica does not accumulate circuits.
func TestAgedOutClosedCircuitsAreSwept(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	r := New(Config{
		Strikes:          3,
		Window:           2 * time.Minute,
		OrgConfirmations: 2,
		RecoveryBase:     30 * time.Second,
		RecoveryCap:      2 * time.Minute,
		ProbeLease:       5 * time.Minute,
	}, func() time.Time { return now })
	r.RecordOverload("org-a", "model-a", true)
	r.RecordOverload("org-b", "model-b", true)
	now = now.Add(time.Minute)
	r.RecordOverload("org-b", "model-b", true)
	now = now.Add(time.Minute + time.Second)

	r.RecordOverload("org-c", "model-c", true)

	_, staleKept := r.circuits[circuitKey("org-a", "model-a", true)]
	_, liveKept := r.circuits[circuitKey("org-b", "model-b", true)]
	assert.False(t, staleKept, "a closed circuit whose strikes all aged out is dropped")
	assert.True(t, liveKept, "a closed circuit with a strike inside the window is kept")
}
