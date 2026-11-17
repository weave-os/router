package subscriptionlane

type Gate int

const (
	BalanceGate Gate = iota
	KeySpendCapGate
	OrgMonthlySpendCapGate
	DelegatedSpendCapGate
	UserSpendLimitGate
	gateCount
)

type gateEvents struct {
	covered   Event
	uncovered Event
}

var gates = [gateCount]gateEvents{
	BalanceGate:            {covered: CreditsDepletedCovered, uncovered: CreditsDepletedUncovered},
	KeySpendCapGate:        {covered: KeySpendCapCovered, uncovered: KeySpendCapUncovered},
	OrgMonthlySpendCapGate: {covered: OrgMonthlySpendCapCovered, uncovered: OrgMonthlySpendCapUncovered},
	DelegatedSpendCapGate:  {covered: DelegatedSpendCapCovered, uncovered: DelegatedSpendCapUncovered},
	UserSpendLimitGate:     {covered: UserSpendLimitCovered, uncovered: UserSpendLimitUncovered},
}

func (g Gate) Tripped(subscriptionCovers bool) Event {
	if subscriptionCovers {
		return gates[g].covered
	}
	return gates[g].uncovered
}

func Gates() []Gate {
	all := make([]Gate, gateCount)
	for i := range all {
		all[i] = Gate(i)
	}
	return all
}
