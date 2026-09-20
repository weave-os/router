package entitlement

import "time"

const sixHourWindow = 6 * time.Hour

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
// intersects. The integer remainder goes to the earliest windows, one micro
// each, so the caps of a period always sum back to the nominal allowance and a
// given window's cap does not depend on when it is read.
//
// The cap derives from the nominal allowance rather than the period allowance
// the entitlement carries, so a plan change mid-window immediately grants the
// target plan's whole window cap while the usage already booked in that window
// keeps counting against it.
func SixHourAllowanceUsdMicros(nominalMonthlyUsdMicros int64, billing, window Period) int64 {
	windows := SixHourWindowsIn(billing)
	if windows <= 0 || nominalMonthlyUsdMicros <= 0 {
		return 0
	}
	first := SixHourWindowAt(billing.Start).Start
	if window.Start.Before(first) {
		return 0
	}
	index := int64(window.Start.Sub(first) / sixHourWindow)
	if index >= windows {
		return 0
	}
	allowance := nominalMonthlyUsdMicros / windows
	if index < nominalMonthlyUsdMicros%windows {
		allowance++
	}
	return allowance
}

// Covers reports whether at falls inside the half-open interval [Start, End).
func (p Period) Covers(at time.Time) bool {
	utc := at.UTC()
	return !utc.Before(p.Start) && utc.Before(p.End)
}
