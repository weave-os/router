package billing

import "errors"

var ErrInsufficientCredits = errors.New("billing: insufficient credits")

var ErrBalanceRowMissing = errors.New("billing: balance row missing")

var ErrBalanceFloorExceeded = errors.New("billing: balance floor exceeded")

var ErrUserMonthlySpendLimitReached = errors.New("billing: engineer monthly spend limit reached")

var ErrSpendLimitCheckUnavailable = errors.New("billing: spend limit check unavailable")
