package subscriptionlane

import "weave-os/router/internal/refusal"

type State int

const (
	Funded State = iota
	LinkedFirst
	SpendCapReached
	CreditsDepleted
	ServedOnSubscription
	ServedOnCustomerKey
	ServedOnOrganizationCredits
	RefusedInsufficientCredits
	RefusedKeySpendCap
	RefusedOrgMonthlySpendLimit
	RefusedDelegatedSpendCap
	RefusedUserSpendLimit
	RefusedCreditsExhausted
	RefusedSpendCapReached
	RefusedSubscriptionPoolExhausted
	RefusedSubscriptionUnavailableForTurn
	RefusedSubscriptionPoolUnavailable
	RefusedSubscriptionPoolThrottled
	stateCount
)

type Event int

const (
	CreditsDepletedCovered Event = iota
	CreditsDepletedUncovered
	KeySpendCapCovered
	KeySpendCapUncovered
	OrgMonthlySpendCapCovered
	OrgMonthlySpendCapUncovered
	DelegatedSpendCapCovered
	DelegatedSpendCapUncovered
	UserSpendLimitCovered
	UserSpendLimitUncovered
	LinkedPlanPreferred
	LinkedPlanReleased
	SubscriptionServed
	CustomerKeyServed
	OrganizationCreditsServed
	SubscriptionUnservable
	SubscriptionPoolExhausted
	SubscriptionPoolUnavailable
	SubscriptionPoolThrottled
	CallerPlanThrottled
	eventCount
)

type Outcome int

const (
	Pending Outcome = iota
	Success
	Refused
)

type stateSpec struct {
	name    string
	outcome Outcome
	refusal refusal.Kind
}

var states = [stateCount]stateSpec{
	Funded:                                {name: "Funded"},
	LinkedFirst:                           {name: "LinkedFirst"},
	SpendCapReached:                       {name: "SpendCapReached"},
	CreditsDepleted:                       {name: "CreditsDepleted"},
	ServedOnSubscription:                  {name: "ServedOnSubscription", outcome: Success},
	ServedOnCustomerKey:                   {name: "ServedOnCustomerKey", outcome: Success},
	ServedOnOrganizationCredits:           {name: "ServedOnOrganizationCredits", outcome: Success},
	RefusedInsufficientCredits:            {name: "RefusedInsufficientCredits", outcome: Refused, refusal: refusal.InsufficientCredits},
	RefusedKeySpendCap:                    {name: "RefusedKeySpendCap", outcome: Refused, refusal: refusal.KeySpendCapReached},
	RefusedOrgMonthlySpendLimit:           {name: "RefusedOrgMonthlySpendLimit", outcome: Refused, refusal: refusal.OrgMonthlySpendLimitReached},
	RefusedDelegatedSpendCap:              {name: "RefusedDelegatedSpendCap", outcome: Refused, refusal: refusal.DelegatedSpendCapReached},
	RefusedUserSpendLimit:                 {name: "RefusedUserSpendLimit", outcome: Refused, refusal: refusal.UserSpendLimitReached},
	RefusedCreditsExhausted:               {name: "RefusedCreditsExhausted", outcome: Refused, refusal: refusal.CreditsExhausted},
	RefusedSpendCapReached:                {name: "RefusedSpendCapReached", outcome: Refused, refusal: refusal.SpendCapReached},
	RefusedSubscriptionPoolExhausted:      {name: "RefusedSubscriptionPoolExhausted", outcome: Refused, refusal: refusal.SubscriptionPoolExhausted},
	RefusedSubscriptionUnavailableForTurn: {name: "RefusedSubscriptionUnavailableForTurn", outcome: Refused, refusal: refusal.SubscriptionUnavailableForTurn},
	RefusedSubscriptionPoolUnavailable:    {name: "RefusedSubscriptionPoolUnavailable", outcome: Refused, refusal: refusal.SubscriptionPoolUnavailable},
	RefusedSubscriptionPoolThrottled:      {name: "RefusedSubscriptionPoolThrottled", outcome: Refused, refusal: refusal.SubscriptionPoolThrottled},
}

var eventNames = [eventCount]string{
	CreditsDepletedCovered:      "CreditsDepletedCovered",
	CreditsDepletedUncovered:    "CreditsDepletedUncovered",
	KeySpendCapCovered:          "KeySpendCapCovered",
	KeySpendCapUncovered:        "KeySpendCapUncovered",
	OrgMonthlySpendCapCovered:   "OrgMonthlySpendCapCovered",
	OrgMonthlySpendCapUncovered: "OrgMonthlySpendCapUncovered",
	DelegatedSpendCapCovered:    "DelegatedSpendCapCovered",
	DelegatedSpendCapUncovered:  "DelegatedSpendCapUncovered",
	UserSpendLimitCovered:       "UserSpendLimitCovered",
	UserSpendLimitUncovered:     "UserSpendLimitUncovered",
	LinkedPlanPreferred:         "LinkedPlanPreferred",
	LinkedPlanReleased:          "LinkedPlanReleased",
	SubscriptionServed:          "SubscriptionServed",
	CustomerKeyServed:           "CustomerKeyServed",
	OrganizationCreditsServed:   "OrganizationCreditsServed",
	SubscriptionUnservable:      "SubscriptionUnservable",
	SubscriptionPoolExhausted:   "SubscriptionPoolExhausted",
	SubscriptionPoolUnavailable: "SubscriptionPoolUnavailable",
	SubscriptionPoolThrottled:   "SubscriptionPoolThrottled",
	CallerPlanThrottled:         "CallerPlanThrottled",
}

func (s State) String() string { return states[s].name }

func (s State) Outcome() Outcome { return states[s].outcome }

func (s State) Terminal() bool { return s.Outcome() != Pending }

func (s State) Refusal() (refusal.Kind, bool) {
	spec := states[s]
	return spec.refusal, spec.outcome == Refused
}

func (e Event) String() string { return eventNames[e] }

func States() []State {
	all := make([]State, stateCount)
	for i := range all {
		all[i] = State(i)
	}
	return all
}

func Events() []Event {
	all := make([]Event, eventCount)
	for i := range all {
		all[i] = Event(i)
	}
	return all
}
