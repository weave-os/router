package subscriptionlane

type Allowance int

const (
	AllowanceUnscoped Allowance = iota
	AllowanceNotSubscribed
	AllowanceCovered
	AllowanceSixHourSpent
	AllowanceWeeklySpent
	AllowanceBillingSpent
	allowanceCount
)

type AllowanceEvent int

const (
	PlanScoped AllowanceEvent = iota
	IncludedUsageCovered
	SixHourWindowSpent
	WeeklyWindowSpent
	BillingWindowSpent
	allowanceEventCount
)

var allowanceNames = [allowanceCount]string{
	AllowanceUnscoped:      "AllowanceUnscoped",
	AllowanceNotSubscribed: "AllowanceNotSubscribed",
	AllowanceCovered:       "AllowanceCovered",
	AllowanceSixHourSpent:  "AllowanceSixHourSpent",
	AllowanceWeeklySpent:   "AllowanceWeeklySpent",
	AllowanceBillingSpent:  "AllowanceBillingSpent",
}

var allowanceEventNames = [allowanceEventCount]string{
	PlanScoped:           "PlanScoped",
	IncludedUsageCovered: "IncludedUsageCovered",
	SixHourWindowSpent:   "SixHourWindowSpent",
	WeeklyWindowSpent:    "WeeklyWindowSpent",
	BillingWindowSpent:   "BillingWindowSpent",
}

type AllowanceTransition struct {
	Verdict Verdict
	To      Allowance
}

func toAllowance(a Allowance) AllowanceTransition {
	return AllowanceTransition{Verdict: Allowed, To: a}
}

var allowanceRejected = AllowanceTransition{Verdict: Rejected}

var allowanceTransitions = [allowanceEventCount][allowanceCount]AllowanceTransition{
	PlanScoped:           {toAllowance(AllowanceNotSubscribed), toAllowance(AllowanceNotSubscribed), toAllowance(AllowanceCovered), toAllowance(AllowanceSixHourSpent), toAllowance(AllowanceWeeklySpent), toAllowance(AllowanceBillingSpent)},
	IncludedUsageCovered: {allowanceRejected, toAllowance(AllowanceCovered), toAllowance(AllowanceCovered), allowanceRejected, allowanceRejected, allowanceRejected},
	SixHourWindowSpent:   {allowanceRejected, toAllowance(AllowanceSixHourSpent), allowanceRejected, toAllowance(AllowanceSixHourSpent), toAllowance(AllowanceWeeklySpent), toAllowance(AllowanceBillingSpent)},
	WeeklyWindowSpent:    {allowanceRejected, toAllowance(AllowanceWeeklySpent), allowanceRejected, toAllowance(AllowanceWeeklySpent), toAllowance(AllowanceWeeklySpent), toAllowance(AllowanceBillingSpent)},
	BillingWindowSpent:   {allowanceRejected, toAllowance(AllowanceBillingSpent), allowanceRejected, toAllowance(AllowanceBillingSpent), toAllowance(AllowanceBillingSpent), toAllowance(AllowanceBillingSpent)},
}

func (a Allowance) String() string { return allowanceNames[a] }

func (e AllowanceEvent) String() string { return allowanceEventNames[e] }

func (a Allowance) Covers() bool { return a == AllowanceCovered }

func (a Allowance) Spent() bool { return a >= AllowanceSixHourSpent }

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
