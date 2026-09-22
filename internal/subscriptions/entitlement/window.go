package entitlement

import "time"

const (
	sixHourWindow = 6 * time.Hour
	weeklyWindow  = 7 * 24 * time.Hour
)

// SixHourBurstFactor multiplies a window's even share of the plan allowance.
//
// An even share alone makes the short window the only binding limit: a
// subscriber would have to spend the share in every window of the period,
// including the ones they sleep through, to reach the allowance they bought.
// Bursting above the share lets one heavy session draw on capacity the
// subscriber is not using elsewhere, while the weekly window below keeps the
// period's pacing.
const SixHourBurstFactor = 4

// SixHourWindowAt returns the fixed UTC six-hour window containing at. Windows
// are anchored to 00:00, 06:00, 12:00, and 18:00 UTC so every subscriber shares
// the same boundaries regardless of when their billing period started.
func SixHourWindowAt(at time.Time) Period {
	utc := at.UTC()
	start := time.Date(utc.Year(), utc.Month(), utc.Day(), utc.Hour()-utc.Hour()%6, 0, 0, 0, time.UTC)
	return Period{Kind: PeriodKindSixHour, Start: start, End: start.Add(sixHourWindow)}
}

// SixHourWindowsIn counts the fixed UTC windows the billing period intersects.
// A period rarely starts or ends on a window boundary, so the partial windows
// at both ends count as whole ones: they each enforce a cap of their own.
func SixHourWindowsIn(billing Period) int64 {
	if !billing.End.After(billing.Start) {
		return 0
	}
	first := SixHourWindowAt(billing.Start).Start
	last := SixHourWindowAt(billing.End.Add(-time.Nanosecond)).Start
	return int64(last.Sub(first)/sixHourWindow) + 1
}

// SixHourAllowanceUsdMicros is the cap one fixed UTC window carries: the plan's
// nominal monthly allowance spread evenly over the windows the billing period
// intersects, then multiplied by SixHourBurstFactor. The integer remainder of
// the even share goes to the earliest windows, one micro each, so a given
// window's cap does not depend on when it is read.
//
// Because of the burst factor the caps of a period sum past the allowance;
// bounding the period is the weekly window's job, not this one's. A cap never
// exceeds the whole nominal allowance, which is the most any single window
// could legitimately serve.
//
// The cap derives from the nominal allowance rather than the period allowance
// the entitlement carries, so a plan change mid-window immediately grants the
// target plan's whole window cap while the usage already booked in that window
// keeps counting against it.
func SixHourAllowanceUsdMicros(nominalMonthlyUsdMicros int64, billing, window Period) int64 {
	anchor := SixHourWindowAt(billing.Start).Start
	share := evenShareUsdMicros(nominalMonthlyUsdMicros, SixHourWindowsIn(billing), windowIndex(anchor, window.Start, sixHourWindow))
	if share == 0 {
		return 0
	}
	if burst := share * SixHourBurstFactor; burst < nominalMonthlyUsdMicros {
		return burst
	}
	return nominalMonthlyUsdMicros
}

// WeeklyWindowAt returns the seven-day window of the billing period containing
// at, or the zero Period when at falls outside the period.
//
// Weeks are anchored to the billing period start rather than to a calendar
// weekday so every subscriber gets whole weeks of their own allowance and the
// last one ends with the period instead of spilling into the next.
func WeeklyWindowAt(billing Period, at time.Time) Period {
	if !billing.Covers(at) {
		return Period{}
	}
	index := windowIndex(billing.Start, at.UTC(), weeklyWindow)
	start := billing.Start.Add(time.Duration(index) * weeklyWindow)
	end := start.Add(weeklyWindow)
	if end.After(billing.End) {
		end = billing.End
	}
	return Period{Kind: PeriodKindWeekly, Start: start, End: end}
}

// WeeklyWindowsIn counts the seven-day windows the billing period holds. The
// final one is short whenever the period is not a whole number of weeks.
func WeeklyWindowsIn(billing Period) int64 {
	if !billing.End.After(billing.Start) {
		return 0
	}
	span := billing.End.Sub(billing.Start)
	windows := int64(span / weeklyWindow)
	if span%weeklyWindow != 0 {
		windows++
	}
	return windows
}

// WeeklyAllowanceUsdMicros is the cap one weekly window carries: the plan's
// nominal monthly allowance spread evenly over the weeks of the billing
// period, with the integer remainder going to the earliest weeks one micro
// each. The caps therefore sum back to the nominal allowance, which makes the
// weekly window the limit that paces the period: a subscriber can burst within
// a week but cannot pull a later week's capacity forward.
//
// Like the six-hour cap it derives from the nominal allowance, so a mid-period
// plan change grants the target plan's whole week immediately.
func WeeklyAllowanceUsdMicros(nominalMonthlyUsdMicros int64, billing, window Period) int64 {
	if WeeklyWindowAt(billing, window.Start) != window {
		return 0
	}
	return evenShareUsdMicros(nominalMonthlyUsdMicros, WeeklyWindowsIn(billing), windowIndex(billing.Start, window.Start, weeklyWindow))
}

// evenShareUsdMicros divides the allowance over count windows, handing the
// remainder to the earliest ones so the shares sum back to the allowance. A
// negative index names a window outside the period, which carries no share.
func evenShareUsdMicros(allowanceUsdMicros, count, index int64) int64 {
	if count <= 0 || allowanceUsdMicros <= 0 || index < 0 || index >= count {
		return 0
	}
	share := allowanceUsdMicros / count
	if index < allowanceUsdMicros%count {
		share++
	}
	return share
}

// windowIndex places a moment in the sequence of equally sized windows laid
// out from anchor, or reports -1 when it falls before the first one.
func windowIndex(anchor, at time.Time, size time.Duration) int64 {
	if at.Before(anchor) {
		return -1
	}
	return int64(at.Sub(anchor) / size)
}

// Covers reports whether at falls inside the half-open interval [Start, End).
func (p Period) Covers(at time.Time) bool {
	utc := at.UTC()
	return !utc.Before(p.Start) && utc.Before(p.End)
}
