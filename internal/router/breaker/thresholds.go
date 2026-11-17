package breaker

import "time"

const (
	defaultStrikes = 3
	defaultWindow  = 2 * time.Minute

	defaultOrgConfirmations = 2

	defaultRecoveryBase = 30 * time.Second
	defaultRecoveryCap  = 10 * time.Minute

	probeLease = 15 * time.Minute
)

func Defaults() Config {
	return Config{
		Strikes:          defaultStrikes,
		Window:           defaultWindow,
		OrgConfirmations: defaultOrgConfirmations,
		RecoveryBase:     defaultRecoveryBase,
		RecoveryCap:      defaultRecoveryCap,
	}
}
