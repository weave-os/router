package serving

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
)

// admissionTimingRecord is the single per-admission latency line. It exists to size the
// deferred gateway target-state cache and carries durations only, never request content.
const admissionTimingRecord = "Serving admission timing"

const (
	attrAdmissionTotal             = "admission_total_ms"
	attrAdmissionTransaction       = "admission_tx_ms"
	attrAdmissionLockWait          = "admission_lock_wait_ms"
	attrAdmissionStateRead         = "admission_gcs_state_read_ms"
	attrAdmissionSelectionSetRead  = "admission_gcs_selection_set_read_ms"
	attrAdmissionSelectionSetReads = "admission_gcs_selection_set_reads"
	attrAdmissionDecide            = "admission_decide_ms"
	attrAdmissionProjection        = "admission_projection_queries_ms"
	attrAdmissionPersistent        = "admission_persistent"
	attrAdmissionOutcome           = "admission_outcome"
	attrAdmissionTarget            = "target"
	attrAdmissionActivation        = "activation_id"
)

// admissionOutcome classifies an admission without reproducing an error message, which
// could carry caller identity.
type admissionOutcome string

const (
	admissionAdmitted admissionOutcome = "admitted"
	admissionDenied   admissionOutcome = "denied"
	admissionFailed   admissionOutcome = "error"
)

// admissionTiming accumulates the monotonic durations of one admission attempt.
type admissionTiming struct {
	started      time.Time
	transaction  time.Duration
	lockWait     time.Duration
	decide       time.Duration
	projection   time.Duration
	registry     policyregistry.AdmissionTimings
	persistent   bool
	outcome      admissionOutcome
	target       policyregistry.ServingTarget
	activationID string
}

// emit writes the one record per admission, at Info so the measurement is collected at the
// gateway's production log level.
func (t admissionTiming) emit(ctx context.Context) {
	observability.FromContext(ctx).Info(admissionTimingRecord, t.attributes()...)
}

func (t admissionTiming) attributes() []any {
	return []any{
		attrAdmissionTotal, milliseconds(time.Since(t.started)),
		attrAdmissionTransaction, milliseconds(t.transaction),
		attrAdmissionLockWait, milliseconds(t.lockWait),
		attrAdmissionStateRead, milliseconds(t.registry.StateRead),
		attrAdmissionSelectionSetRead, milliseconds(t.registry.SelectionSetRead),
		attrAdmissionSelectionSetReads, t.registry.SelectionSetReads,
		attrAdmissionDecide, milliseconds(t.decide),
		attrAdmissionProjection, milliseconds(t.projection),
		attrAdmissionPersistent, t.persistent,
		attrAdmissionOutcome, string(t.outcome),
		attrAdmissionTarget, string(t.target),
		attrAdmissionActivation, t.activationID,
	}
}

// milliseconds keeps sub-millisecond resolution, which the projection queries need.
func milliseconds(elapsed time.Duration) float64 {
	return float64(elapsed.Microseconds()) / 1000
}
