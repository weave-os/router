package billing

import "errors"

var ErrInsufficientCredits = errors.New("billing: insufficient credits")

var ErrBalanceRowMissing = errors.New("billing: balance row missing")

// ErrBalanceFloorExceeded means a served request's debit would cross the hard
// organization credit floor and needs reconciliation.
var ErrBalanceFloorExceeded = errors.New("billing: balance floor exceeded")

// ErrUserMonthlySpendLimitReached is returned when the engineer's current
// UTC-month spend meets their effective monthly limit. Mapped to HTTP 402.
var ErrUserMonthlySpendLimitReached = errors.New("billing: engineer monthly spend limit reached")

var ErrSpendLimitCheckUnavailable = errors.New("billing: spend limit check unavailable")
