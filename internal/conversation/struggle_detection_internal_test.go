package conversation

import (
	"testing"
	"time"
)

func TestStruggleReasons_HealthySessionNeverFires(t *testing.T) {
	// A short, quick session: well under both operating points.
	if reasons := StruggleReasons(5, 2*time.Minute); len(reasons) != 0 {
		t.Fatalf("healthy session fired %v", reasons)
	}
}

func TestStruggleReasons_TurnsWithoutWallNeverFires(t *testing.T) {
	// Burst of turns in seconds — a fast agentic loop is not a struggle, and a
	// wall-clock floor is what separates the two.
	if reasons := StruggleReasons(struggleLateTurns+50, 30*time.Second); len(reasons) != 0 {
		t.Fatalf("fast high-turn session fired %v", reasons)
	}
}

func TestStruggleReasons_WallWithoutTurnsNeverFires(t *testing.T) {
	// A session idle for hours between a handful of turns is a user thinking,
	// not a model grinding.
	if reasons := StruggleReasons(3, 4*time.Hour); len(reasons) != 0 {
		t.Fatalf("idle low-turn session fired %v", reasons)
	}
}

func TestStruggleReasons_ZeroWallNeverFires(t *testing.T) {
	// FirstPinnedAt is zero on a missing pin, which the caller maps to a zero
	// duration. That must never read as "infinitely old".
	if reasons := StruggleReasons(struggleLateTurns+10, 0); len(reasons) != 0 {
		t.Fatalf("zero-wall session fired %v", reasons)
	}
}

func TestStruggleReasons_EarlyOperatingPoint(t *testing.T) {
	reasons := StruggleReasons(StruggleEarlyTurns, StruggleEarlyWall)
	if len(reasons) != 1 || reasons[0] != StruggleReasonEarly {
		t.Fatalf("reasons = %v, want [%s]", reasons, StruggleReasonEarly)
	}
}

func TestStruggleReasons_LateOperatingPointSupersedesEarly(t *testing.T) {
	// A session crossing both points reports only the later one, so the two
	// escalation stages map to distinct rows rather than double-reporting.
	reasons := StruggleReasons(struggleLateTurns, struggleLateWall)
	if len(reasons) != 1 || reasons[0] != StruggleReasonLate {
		t.Fatalf("reasons = %v, want [%s]", reasons, StruggleReasonLate)
	}
}

func TestStruggleReasons_LateTurnsButEarlyWallStaysEarly(t *testing.T) {
	// Enough turns for the late point but not enough elapsed time: the late
	// point requires BOTH, so this must degrade to early, not fire late.
	reasons := StruggleReasons(struggleLateTurns, StruggleEarlyWall)
	if len(reasons) != 1 || reasons[0] != StruggleReasonEarly {
		t.Fatalf("reasons = %v, want [%s]", reasons, StruggleReasonEarly)
	}
}

func TestStruggleReasons_JustBelowEarlyThresholds(t *testing.T) {
	if reasons := StruggleReasons(StruggleEarlyTurns-1, StruggleEarlyWall); len(reasons) != 0 {
		t.Fatalf("one turn below the floor fired %v", reasons)
	}
	if reasons := StruggleReasons(StruggleEarlyTurns, StruggleEarlyWall-time.Second); len(reasons) != 0 {
		t.Fatalf("one second below the floor fired %v", reasons)
	}
}
