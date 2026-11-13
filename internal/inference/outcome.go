package inference

// ExecutionOutcome records the policy-selected target and the target that
// ultimately served the operation. The executor owns response streaming; the
// outcome carries only bounded provenance needed by callers and telemetry.
type ExecutionOutcome struct {
	PolicySelectedTarget Target
	ServedTarget         Target
	AttemptCount         int
	FallbackUsed         bool
	ResponseCommitted    bool
}
