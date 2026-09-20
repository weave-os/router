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

// Covers reports whether at falls inside the half-open interval [Start, End).
func (p Period) Covers(at time.Time) bool {
	utc := at.UTC()
	return !utc.Before(p.Start) && utc.Before(p.End)
}
