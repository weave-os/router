package turnrecord

type FailureOwner string

const (
	FailureOwnerProvider FailureOwner = "provider"
	FailureOwnerRouter   FailureOwner = "router"
	FailureOwnerClient   FailureOwner = "client"
	FailureOwnerUnknown  FailureOwner = "unknown"
)
