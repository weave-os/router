package entitlement

import "time"

const (
	sixHourWindow = 6 * time.Hour
	weeklyWindow  = 7 * 24 * time.Hour
)

const SixHourBurstFactor = 4

func SixHourWindowAt(at time.Time) Period {
	utc := at.UTC()
	start := time.Date(utc.Year(), utc.Month(), utc.Day(), utc.Hour()-utc.Hour()%6, 0, 0, 0, time.UTC)
	return Period{Kind: PeriodKindSixHour, Start: start, End: start.Add(sixHourWindow)}
}

func SixHourWindowsIn(billing Period) int64 {
	if !billing.End.After(billing.Start) {
		return 0
	}
	first := SixHourWindowAt(billing.Start).Start
	last := SixHourWindowAt(billing.End.Add(-time.Nanosecond)).Start
	return int64(last.Sub(first)/sixHourWindow) + 1
}

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

func WeeklyAllowanceUsdMicros(nominalMonthlyUsdMicros int64, billing, window Period) int64 {
	if WeeklyWindowAt(billing, window.Start) != window {
		return 0
	}
	return evenShareUsdMicros(nominalMonthlyUsdMicros, WeeklyWindowsIn(billing), windowIndex(billing.Start, window.Start, weeklyWindow))
}

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

func windowIndex(anchor, at time.Time, size time.Duration) int64 {
	if at.Before(anchor) {
		return -1
	}
	return int64(at.Sub(anchor) / size)
}

func (p Period) Covers(at time.Time) bool {
	utc := at.UTC()
	return !utc.Before(p.Start) && utc.Before(p.End)
}
