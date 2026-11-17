package inference

type ExecutionOutcome struct {
	PolicySelectedTarget Target
	ServedTarget         Target
	AttemptCount         int
	FallbackUsed         bool
	ResponseCommitted    bool
}
