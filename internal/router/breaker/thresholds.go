package breaker

import "time"

// The router's overload thresholds. They are fixed in code on purpose: one
// shared circuit per model pools every organization's evidence, so a
// per-deployment knob would only let replicas disagree about the same
// provider. Change them here, with a test, when production evidence says so.
const (
	// defaultStrikes overloaded dispatch operations (same-binding retries
	// count once) within defaultWindow open a model's circuit.
	defaultStrikes = 3
	defaultWindow  = 2 * time.Minute
	// defaultOrgConfirmations is how many overloads an organization must see
	// itself before an open shared circuit withdraws the model from it.
	defaultOrgConfirmations = 2
	// defaultRecoveryBase is the first open period before one request probes
	// the model; each failed probe doubles it up to defaultRecoveryCap.
	defaultRecoveryBase = 30 * time.Second
	defaultRecoveryCap  = 10 * time.Minute
	// probeLease bounds how long a claimed probe may stay unresolved before
	// another request may claim it; it covers a long streamed turn.
	probeLease = 15 * time.Minute
)

// Defaults is the production configuration.
func Defaults() Config {
	return Config{
		Strikes:          defaultStrikes,
		Window:           defaultWindow,
		OrgConfirmations: defaultOrgConfirmations,
		RecoveryBase:     defaultRecoveryBase,
		RecoveryCap:      defaultRecoveryCap,
	}
}
