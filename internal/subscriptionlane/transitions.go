package subscriptionlane

type Verdict int

const (
	unspecified Verdict = iota
	Allowed
	Rejected
)

type Transition struct {
	Verdict Verdict
	To      State
}

func to(s State) Transition { return Transition{Verdict: Allowed, To: s} }

var rejected = Transition{Verdict: Rejected}

const pendingStateCount = CreditsDepleted + 1

var transitions = [eventCount][pendingStateCount]Transition{
	CreditsDepletedCovered:      {to(CreditsDepleted), to(CreditsDepleted), to(CreditsDepleted), to(CreditsDepleted)},
	CreditsDepletedUncovered:    {to(RefusedInsufficientCredits), to(RefusedInsufficientCredits), to(RefusedInsufficientCredits), to(RefusedInsufficientCredits)},
	KeySpendCapCovered:          {to(SpendCapReached), to(SpendCapReached), to(SpendCapReached), to(CreditsDepleted)},
	KeySpendCapUncovered:        {to(RefusedKeySpendCap), to(RefusedKeySpendCap), to(RefusedKeySpendCap), to(RefusedKeySpendCap)},
	OrgMonthlySpendCapCovered:   {to(SpendCapReached), to(SpendCapReached), to(SpendCapReached), to(CreditsDepleted)},
	OrgMonthlySpendCapUncovered: {to(RefusedOrgMonthlySpendLimit), to(RefusedOrgMonthlySpendLimit), to(RefusedOrgMonthlySpendLimit), to(RefusedOrgMonthlySpendLimit)},
	DelegatedSpendCapCovered:    {to(SpendCapReached), to(SpendCapReached), to(SpendCapReached), to(CreditsDepleted)},
	DelegatedSpendCapUncovered:  {to(RefusedDelegatedSpendCap), to(RefusedDelegatedSpendCap), to(RefusedDelegatedSpendCap), to(RefusedDelegatedSpendCap)},
	UserSpendLimitCovered:       {to(SpendCapReached), to(SpendCapReached), to(SpendCapReached), to(CreditsDepleted)},
	UserSpendLimitUncovered:     {to(RefusedUserSpendLimit), to(RefusedUserSpendLimit), to(RefusedUserSpendLimit), to(RefusedUserSpendLimit)},
	LinkedPlanPreferred:         {to(LinkedFirst), to(LinkedFirst), to(SpendCapReached), to(CreditsDepleted)},
	LinkedPlanReleased:          {to(Funded), to(Funded), to(SpendCapReached), to(CreditsDepleted)},
	SubscriptionServed:          {to(ServedOnSubscription), to(ServedOnSubscription), to(ServedOnSubscription), to(ServedOnSubscription)},
	CustomerKeyServed:           {to(ServedOnCustomerKey), to(ServedOnCustomerKey), to(ServedOnCustomerKey), to(ServedOnCustomerKey)},
	OrganizationCreditsServed:   {to(ServedOnOrganizationCredits), to(ServedOnOrganizationCredits), rejected, rejected},
	SubscriptionUnservable:      {to(RefusedSubscriptionUnavailableForTurn), to(RefusedSubscriptionUnavailableForTurn), to(RefusedSpendCapReached), to(RefusedCreditsExhausted)},
	SubscriptionPoolExhausted:   {to(RefusedSubscriptionPoolExhausted), to(RefusedSubscriptionPoolExhausted), to(RefusedSpendCapReached), to(RefusedCreditsExhausted)},
	SubscriptionPoolUnavailable: {to(RefusedSubscriptionPoolUnavailable), to(RefusedSubscriptionPoolUnavailable), to(RefusedSubscriptionPoolUnavailable), to(RefusedSubscriptionPoolUnavailable)},
	SubscriptionPoolThrottled:   {to(RefusedSubscriptionPoolThrottled), to(RefusedSubscriptionPoolThrottled), to(RefusedSubscriptionPoolThrottled), to(RefusedSubscriptionPoolThrottled)},
	CallerPlanThrottled:         {to(RefusedSubscriptionPoolThrottled), to(RefusedSubscriptionPoolThrottled), to(RefusedSubscriptionPoolThrottled), to(RefusedSubscriptionPoolThrottled)},
	ModelOutsidePlan:            {to(RefusedProductIneligible), to(RefusedProductIneligible), to(RefusedProductIneligible), to(RefusedProductIneligible)},
	ClientAppRefused:            {to(RefusedSubscriptionClientApp), to(RefusedSubscriptionClientApp), to(RefusedSubscriptionClientApp), to(RefusedSubscriptionClientApp)},
	ClaudeEnterpriseUnservable:  {to(RefusedClaudeEnterpriseOnly), to(RefusedClaudeEnterpriseOnly), to(RefusedClaudeEnterpriseOnly), to(RefusedClaudeEnterpriseOnly)},
}

func Lookup(from State, on Event) Transition {
	if from.Terminal() {
		return rejected
	}
	return transitions[on][from]
}

func Step(from State, on Event) (State, bool) {
	next := Lookup(from, on)
	if next.Verdict != Allowed {
		return from, false
	}
	return next.To, true
}

func Permits(from State, on Event) bool {
	return Lookup(from, on).Verdict == Allowed
}

type Rule struct {
	From       State
	On         Event
	Transition Transition
}

func Rules() []Rule {
	rules := make([]Rule, 0, int(stateCount)*int(eventCount))
	for _, from := range States() {
		for _, on := range Events() {
			rules = append(rules, Rule{From: from, On: on, Transition: Lookup(from, on)})
		}
	}
	return rules
}
