// Package conversation is the Conversation Lifecycle bounded context: it
// detects when an agent session is stuck (tool-call cycles, repeated
// no-progress dispatches, repeated narration, spirals, long struggles) from
// the inbound conversation alone. It performs no I/O; durable budgets and
// events go through the store ports declared here.
package conversation
