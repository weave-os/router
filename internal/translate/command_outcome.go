package translate

// CommandKind identifies router control operations independently of their prose.
type CommandKind string

const (
	CommandForceModel     CommandKind = "force_model"
	CommandRouterSession  CommandKind = "router_session"
	CommandRouterModels   CommandKind = "router_models"
	CommandRouterFeedback CommandKind = "router_feedback"
	CommandLoopStop       CommandKind = "loop_stop"
)

type CommandDisposition string

const (
	CommandAccepted CommandDisposition = "accepted"
	CommandRejected CommandDisposition = "rejected"
)

type PinTransition string

const (
	PinUnchanged PinTransition = "unchanged"
	PinApplied   PinTransition = "applied"
	PinCleared   PinTransition = "cleared"
)

type CommandPin struct {
	Transition PinTransition `json:"transition"`
	Model      string        `json:"model,omitempty"`
	Provider   string        `json:"provider,omitempty"`
	Effort     string        `json:"effort,omitempty"`
	Group      string        `json:"group,omitempty"`
}

// CommandOutcome records one command application. It is historical evidence,
// not a snapshot of the current server pin. Only persistence owners decide Pin.
type CommandOutcome struct {
	Version     int                `json:"version"`
	ID          string             `json:"id"`
	Command     CommandKind        `json:"command"`
	Disposition CommandDisposition `json:"disposition"`
	Pin         CommandPin         `json:"pin"`
}
