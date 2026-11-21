package sessionpin

type StrategyColumns struct {
	PolicyGroup               string
	ConsecutiveDowngradeVotes int
	ConsecutiveUpgradeVotes   int
}

type StrategyColumnAccessor interface {
	Columns(Pin) StrategyColumns
	WithColumns(Pin, StrategyColumns) Pin
}
