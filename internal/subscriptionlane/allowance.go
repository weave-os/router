package subscriptionlane

type Allowance int

const (
	AllowanceUnspent Allowance = iota
	AllowanceSixHourSpent
	AllowanceWeeklySpent
	AllowanceBillingSpent
	allowanceCount
)

type AllowanceEvent int

const (
	SixHourWindowSpent AllowanceEvent = iota
	WeeklyWindowSpent
	BillingWindowSpent
	allowanceEventCount
)

var allowanceNames = [allowanceCount]string{
	AllowanceUnspent:      "AllowanceUnspent",
	AllowanceSixHourSpent: "AllowanceSixHourSpent",
	AllowanceWeeklySpent:  "AllowanceWeeklySpent",
	AllowanceBillingSpent: "AllowanceBillingSpent",
}

var allowanceEventNames = [allowanceEventCount]string{
	SixHourWindowSpent: "SixHourWindowSpent",
	WeeklyWindowSpent:  "WeeklyWindowSpent",
	BillingWindowSpent: "BillingWindowSpent",
}

type AllowanceTransition struct {
	Verdict Verdict
	To      Allowance
}

func toAllowance(a Allowance) AllowanceTransition {
	return AllowanceTransition{Verdict: Allowed, To: a}
}

var allowanceTransitions = [allowanceEventCount][allowanceCount]AllowanceTransition{
	SixHourWindowSpent: {toAllowance(AllowanceSixHourSpent), toAllowance(AllowanceSixHourSpent), toAllowance(AllowanceWeeklySpent), toAllowance(AllowanceBillingSpent)},
	WeeklyWindowSpent:  {toAllowance(AllowanceWeeklySpent), toAllowance(AllowanceWeeklySpent), toAllowance(AllowanceWeeklySpent), toAllowance(AllowanceBillingSpent)},
	BillingWindowSpent: {toAllowance(AllowanceBillingSpent), toAllowance(AllowanceBillingSpent), toAllowance(AllowanceBillingSpent), toAllowance(AllowanceBillingSpent)},
}

func (a Allowance) String() string { return allowanceNames[a] }

func (e AllowanceEvent) String() string { return allowanceEventNames[e] }

func Allowances() []Allowance {
	all := make([]Allowance, allowanceCount)
	for i := range all {
		all[i] = Allowance(i)
	}
	return all
}

func AllowanceEvents() []AllowanceEvent {
	all := make([]AllowanceEvent, allowanceEventCount)
	for i := range all {
		all[i] = AllowanceEvent(i)
	}
	return all
}

func LookupAllowance(from Allowance, on AllowanceEvent) AllowanceTransition {
	return allowanceTransitions[on][from]
}

func StepAllowance(from Allowance, on AllowanceEvent) (Allowance, bool) {
	t := LookupAllowance(from, on)
	if t.Verdict != Allowed {
		return from, false
	}
	return t.To, true
}

type AllowanceRule struct {
	From       Allowance
	On         AllowanceEvent
	Transition AllowanceTransition
}

func AllowanceRules() []AllowanceRule {
	rules := make([]AllowanceRule, 0, int(allowanceEventCount)*int(allowanceCount))
	for _, from := range Allowances() {
		for _, on := range AllowanceEvents() {
			rules = append(rules, AllowanceRule{From: from, On: on, Transition: LookupAllowance(from, on)})
		}
	}
	return rules
}
