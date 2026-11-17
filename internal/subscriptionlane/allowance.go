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

var allowanceTransitions = [allowanceEventCount][allowanceCount]Allowance{
	SixHourWindowSpent: {AllowanceSixHourSpent, AllowanceSixHourSpent, AllowanceWeeklySpent, AllowanceBillingSpent},
	WeeklyWindowSpent:  {AllowanceWeeklySpent, AllowanceWeeklySpent, AllowanceWeeklySpent, AllowanceBillingSpent},
	BillingWindowSpent: {AllowanceBillingSpent, AllowanceBillingSpent, AllowanceBillingSpent, AllowanceBillingSpent},
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

func StepAllowance(from Allowance, on AllowanceEvent) Allowance {
	return allowanceTransitions[on][from]
}

type AllowanceRule struct {
	From Allowance
	On   AllowanceEvent
	To   Allowance
}

func AllowanceRules() []AllowanceRule {
	rules := make([]AllowanceRule, 0, int(allowanceEventCount)*int(allowanceCount))
	for _, from := range Allowances() {
		for _, on := range AllowanceEvents() {
			rules = append(rules, AllowanceRule{From: from, On: on, To: StepAllowance(from, on)})
		}
	}
	return rules
}
