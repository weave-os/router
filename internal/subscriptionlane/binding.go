package subscriptionlane

type Binding int

const (
	Unbound Binding = iota
	IncludedLane
	ExhaustedSet
	SubscriptionAttempt
	OwnPlanPick
	EnterpriseSeat
	EnterpriseCapHandover
	bindingCount
)

type BindingEvent int

const (
	IncludedLaneEntered BindingEvent = iota
	IncludedLaneExhausted
	EnterpriseSeatBound
	EnterpriseCapRefused
	OwnPlanPicked
	SubscriptionAttemptStarted
	bindingEventCount
)

var bindingNames = [bindingCount]string{
	Unbound:               "Unbound",
	IncludedLane:          "IncludedLane",
	ExhaustedSet:          "ExhaustedSet",
	SubscriptionAttempt:   "SubscriptionAttempt",
	OwnPlanPick:           "OwnPlanPick",
	EnterpriseSeat:        "EnterpriseSeat",
	EnterpriseCapHandover: "EnterpriseCapHandover",
}

var bindingEventNames = [bindingEventCount]string{
	IncludedLaneEntered:        "IncludedLaneEntered",
	IncludedLaneExhausted:      "IncludedLaneExhausted",
	EnterpriseSeatBound:        "EnterpriseSeatBound",
	EnterpriseCapRefused:       "EnterpriseCapRefused",
	OwnPlanPicked:              "OwnPlanPicked",
	SubscriptionAttemptStarted: "SubscriptionAttemptStarted",
}

type BindingTransition struct {
	Verdict Verdict
	To      Binding
}

func bind(b Binding) BindingTransition { return BindingTransition{Verdict: Allowed, To: b} }

var unbindable = BindingTransition{Verdict: Rejected}

var bindingTransitions = [bindingEventCount][bindingCount]BindingTransition{
	IncludedLaneEntered:        {bind(IncludedLane), unbindable, unbindable, unbindable, unbindable, unbindable, unbindable},
	IncludedLaneExhausted:      {unbindable, bind(ExhaustedSet), unbindable, unbindable, unbindable, unbindable, unbindable},
	EnterpriseSeatBound:        {bind(EnterpriseSeat), unbindable, bind(EnterpriseSeat), unbindable, unbindable, unbindable, unbindable},
	EnterpriseCapRefused:       {unbindable, unbindable, unbindable, unbindable, unbindable, bind(EnterpriseCapHandover), unbindable},
	OwnPlanPicked:              {bind(OwnPlanPick), unbindable, bind(OwnPlanPick), unbindable, unbindable, unbindable, bind(OwnPlanPick)},
	SubscriptionAttemptStarted: {bind(SubscriptionAttempt), unbindable, bind(SubscriptionAttempt), unbindable, unbindable, unbindable, bind(SubscriptionAttempt)},
}

type admission struct {
	subscription        Verdict
	customerKey         Verdict
	organizationCredits Verdict
}

var (
	paidOpen  = admission{subscription: Allowed, customerKey: Allowed, organizationCredits: Allowed}
	planBound = admission{subscription: Allowed, customerKey: Rejected, organizationCredits: Rejected}
)

var admissions = [bindingCount]admission{
	Unbound:               paidOpen,
	IncludedLane:          planBound,
	ExhaustedSet:          paidOpen,
	SubscriptionAttempt:   planBound,
	OwnPlanPick:           planBound,
	EnterpriseSeat:        planBound,
	EnterpriseCapHandover: paidOpen,
}

func (b Binding) String() string { return bindingNames[b] }

func (e BindingEvent) String() string { return bindingEventNames[e] }

func LookupBinding(from Binding, on BindingEvent) BindingTransition {
	return bindingTransitions[on][from]
}

func StepBinding(from Binding, on BindingEvent) (Binding, bool) {
	next := LookupBinding(from, on)
	if next.Verdict != Allowed {
		return from, false
	}
	return next.To, true
}

func (b Binding) Admission(on Event) Verdict {
	switch on {
	case SubscriptionServed:
		return admissions[b].subscription
	case CustomerKeyServed:
		return admissions[b].customerKey
	case OrganizationCreditsServed:
		return admissions[b].organizationCredits
	default:
		return Rejected
	}
}

func (b Binding) Admits(on Event) bool { return b.Admission(on) == Allowed }

func Commit(lane State, binding Binding, on Event) (State, bool) {
	if !binding.Admits(on) {
		return lane, false
	}
	return Step(lane, on)
}

func Bindings() []Binding {
	all := make([]Binding, bindingCount)
	for i := range all {
		all[i] = Binding(i)
	}
	return all
}

func BindingEvents() []BindingEvent {
	all := make([]BindingEvent, bindingEventCount)
	for i := range all {
		all[i] = BindingEvent(i)
	}
	return all
}

type BindingRule struct {
	From       Binding
	On         BindingEvent
	Transition BindingTransition
}

func BindingRules() []BindingRule {
	rules := make([]BindingRule, 0, int(bindingCount)*int(bindingEventCount))
	for _, from := range Bindings() {
		for _, on := range BindingEvents() {
			rules = append(rules, BindingRule{From: from, On: on, Transition: LookupBinding(from, on)})
		}
	}
	return rules
}
