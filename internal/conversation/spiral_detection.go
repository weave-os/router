package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// Shadow-mode spiral detector: flags sessions death-marching (error grind,
// same-file thrash, fuzzy repetition, monologue) on their routed model.
// LOG ONLY — records one durable event per signal class per session, no
// routing change. Events are joined offline against outcomes to pick fire
// rate/precision/lead-time before any escalation is armed (~35% of failures
// have no behavioral tell per the offline trajectory audit, so live
// measurement decides the operating point, not benchmarks).
//
// Thresholds from an offline audit of 186 bake-off trajectories + 8.5k
// outcome shards: err streak AUC 0.73 pooled (0.86 on deepseek-v4-flash);
// same-file thrash (>=5 edits) present in 9/20 costliest death marches;
// repeat-window fraction >=~0.3 over last 12 calls: +8-11pp recall over a
// depth cap at matched FPR; monologue is industry-convergent (OpenHands)
// but unmeasured here — shadow mode measures it.
//
// Unlike the cyclic-loop detector (exact-signature, no-edit windows), these
// tolerate "rhyming" spirals — same file, slightly different args.
const (
	// Arming floor: no signal evaluated before this many tool calls exist.
	// Early-session intervention is the documented false-positive mode.
	spiralMinToolCalls = 12
	// spiralErrStreakThreshold: consecutive errored tool_results at the tail.
	spiralErrStreakThreshold = 3
	// spiralSameFileEditThreshold: edits targeting one file path.
	spiralSameFileEditThreshold = 5
	// Repetition: fraction of the last spiralRepeatWindow signatures that
	// are duplicates, evaluated once spiralRepeatMinCalls is reached.
	spiralRepeatWindow        = 12
	spiralRepeatMinCalls      = 20
	spiralRepeatFracThreshold = 0.34
	// Consecutive assistant messages with no tool activity since the last
	// real user input. Set above the text-only nudge threshold to avoid
	// double-reporting.
	spiralMonologueThreshold = 4
	// Trailing A/B/A/B run length between exactly two distinct signatures;
	// 6 = three round trips: two is a legitimate read-edit-read-edit rhythm.
	spiralPingPongThreshold = 6
	// Tool calls since the last non-errored edit; only meaningful once the
	// session has attempted an edit (pure Explore phases make none).
	spiralNoProgressThreshold = 15
)

// SpiralReason values are persisted verbatim in spiral_shadow_events.reason and
// the telemetry spiral_reasons column, so renaming one breaks offline joins.
type SpiralReason string

// Spiral signal-class taxonomy. One event per (session, role, reason).
const (
	SpiralReasonErrStreak      SpiralReason = "err_streak"
	spiralReasonSameFileThrash SpiralReason = "same_file_thrash"
	spiralReasonRepetition     SpiralReason = "repetition"
	spiralReasonMonologue      SpiralReason = "monologue"
	SpiralReasonPingPong       SpiralReason = "ping_pong"
	spiralReasonNoProgress     SpiralReason = "no_progress"
)

// SpiralReasonStrings returns nil for nil; pass an empty slice to persist an
// empty list.
func SpiralReasonStrings(reasons []SpiralReason) []string {
	if reasons == nil {
		return nil
	}
	values := make([]string, len(reasons))
	for i, reason := range reasons {
		values[i] = string(reason)
	}
	return values
}

// SpiralShadowStore persists shadow spiral detections (router.spiral_shadow_events).
// CountSpiralShadowEvents enforces the once-per-(session, reason) budget across replicas.
type SpiralShadowStore interface {
	InsertSpiralShadowEvent(ctx context.Context, p SpiralShadowEvent) error
	CountSpiralShadowEvents(ctx context.Context, sessionKey []byte, role, reason string) (count int64, err error)
}

// SpiralShadowEvent mirrors one router.spiral_shadow_events row. All signal
// values are recorded regardless of which reason fired, so thresholds can
// be re-tuned offline without re-running traffic.
type SpiralShadowEvent struct {
	InstallationID   string
	SessionKey       []byte
	Role             string
	RoutedModel      string
	TurnType         string
	Reason           string
	ErrStreak        int32
	ErroredResults   int32
	ToolResults      int32
	MaxSameFileEdits int32
	SameFilePathHash string
	RepeatFrac       float64
	MonologueLen     int32
	ToolCallCount    int32
	MessageCount     int32
	// PingPongLen is the trailing A/B/A/B alternation length between exactly
	// two tool-call signatures.
	PingPongLen int32
	// StepsSinceProgress is the number of tool calls made since the last
	// non-errored edit/write result; zero when no edit was ever attempted.
	StepsSinceProgress int32
}

// SpiralSignals is the per-turn signal snapshot, a pure function of the
// request body (ComputeSpiralSignals).
type SpiralSignals struct {
	ErrStats         translate.ToolResultErrorStats
	MaxSameFileEdits int
	SameFilePathHash string
	RepeatFrac       float64
	MonologueLen     int
	ToolCallCount    int
	MessageCount     int
	PingPongLen      int
	// stepsSinceProgress is only meaningful once editAttempted is set: a
	// session that never tried to edit has nothing to have stalled on.
	StepsSinceProgress int
	EditAttempted      bool
}

// ComputeSpiralSignals derives the spiral signal snapshot from the inbound
// envelope. Pure and stateless — the full history arrives on every turn.
func ComputeSpiralSignals(env *translate.RequestEnvelope, messageCount int) SpiralSignals {
	sigs := env.AssistantToolCallSignatures()
	s := SpiralSignals{
		ErrStats:      env.ToolResultErrors(),
		MonologueLen:  env.TrailingAssistantMonologue(),
		ToolCallCount: len(sigs),
		MessageCount:  messageCount,
	}

	// Path recorded as a hash only — confirms "same file" offline without
	// persisting customer file names.
	pathCounts := make(map[string]int)
	for _, fp := range env.AssistantToolCallFilePaths() {
		if _, isEdit := editToolNames[fp.Name]; !isEdit {
			continue
		}
		pathCounts[fp.Path]++
		if pathCounts[fp.Path] > s.MaxSameFileEdits {
			s.MaxSameFileEdits = pathCounts[fp.Path]
			h := sha256.Sum256([]byte(fp.Path))
			s.SameFilePathHash = hex.EncodeToString(h[:8])
		}
	}

	// Catches rhyming re-grind that simple exact-repeat checks miss.
	if len(sigs) >= spiralRepeatWindow {
		window := sigs[len(sigs)-spiralRepeatWindow:]
		counts := make(map[string]int, len(window))
		for _, sig := range window {
			counts[sig.Name+"\x00"+sig.InputHash]++
		}
		repeated := 0
		for _, c := range counts {
			if c > 1 {
				repeated += c
			}
		}
		s.RepeatFrac = float64(repeated) / float64(len(window))
	}

	s.PingPongLen = trailingPingPongLen(sigs)
	s.StepsSinceProgress, s.EditAttempted = stepsSinceProgress(env.AssistantToolCallOutcomes())
	return s
}

// trailingPingPongLen counts the trailing A/B alternation between exactly two
// distinct signatures; a repeated single signature is the repetition signal's job.
func trailingPingPongLen(sigs []translate.ToolCallSig) int {
	if len(sigs) < 4 {
		return 0
	}
	key := func(i int) string { return sigs[i].Name + "\x00" + sigs[i].InputHash }
	last, prev := key(len(sigs)-1), key(len(sigs)-2)
	if last == prev {
		return 0
	}
	run := 2
	for i := len(sigs) - 3; i >= 0; i-- {
		// Two back must repeat: ...A B A B with last=B, prev=A.
		if key(i) != key(i+2) {
			break
		}
		run++
	}
	if run < 4 {
		return 0
	}
	return run
}

// stepsSinceProgress counts tool calls since the last non-errored edit, and
// reports whether an edit was ever attempted. An unresolved in-flight edit
// counts as progress so we don't fire on the turn before its result arrives.
func stepsSinceProgress(outcomes []translate.ToolCallOutcome) (steps int, editAttempted bool) {
	lastProgress := -1
	for i, o := range outcomes {
		if _, isEdit := editToolNames[o.Name]; !isEdit {
			continue
		}
		editAttempted = true
		if !o.Resolved || !o.Errored {
			lastProgress = i
		}
	}
	if !editAttempted {
		return 0, false
	}
	return len(outcomes) - 1 - lastProgress, true
}

// SpiralReasons returns the signal classes whose thresholds the snapshot
// crosses. Empty below the arming floor.
func SpiralReasons(s SpiralSignals) []SpiralReason {
	if s.ToolCallCount < spiralMinToolCalls {
		return nil
	}
	var reasons []SpiralReason
	if s.ErrStats.TrailingErrStreak >= spiralErrStreakThreshold {
		reasons = append(reasons, SpiralReasonErrStreak)
	}
	if s.MaxSameFileEdits >= spiralSameFileEditThreshold {
		reasons = append(reasons, spiralReasonSameFileThrash)
	}
	if s.ToolCallCount >= spiralRepeatMinCalls && s.RepeatFrac >= spiralRepeatFracThreshold {
		reasons = append(reasons, spiralReasonRepetition)
	}
	if s.MonologueLen >= spiralMonologueThreshold {
		reasons = append(reasons, spiralReasonMonologue)
	}
	if s.PingPongLen >= spiralPingPongThreshold {
		reasons = append(reasons, SpiralReasonPingPong)
	}
	if s.EditAttempted && s.StepsSinceProgress >= spiralNoProgressThreshold {
		reasons = append(reasons, spiralReasonNoProgress)
	}
	return reasons
}

// spiralFiredCache de-dupes shadow fires per (session, role, reason) per
// replica so the durable budget query runs once per ~hour, not every turn.
// Cross-replica dupes are still possible before the durable count lands;
// offline analysis de-dupes by (session_key, reason).
const (
	spiralFiredCacheSize = 8192
	spiralFiredCacheTTL  = time.Hour
)

// SpiralTracker remembers which SpiralFiredKey values already fired on this
// replica.
type SpiralTracker struct {
	fired *lru.LRU[string, struct{}]
}

// NewSpiralTracker returns an empty replica-local fire cache.
func NewSpiralTracker() *SpiralTracker {
	return &SpiralTracker{
		fired: lru.NewLRU[string, struct{}](spiralFiredCacheSize, nil, spiralFiredCacheTTL),
	}
}

// Fired reports whether key was marked on this replica within the cache TTL.
func (t *SpiralTracker) Fired(key string) bool {
	_, seen := t.fired.Get(key)
	return seen
}

// MarkFired records a fire on this replica only; CountSpiralShadowEvents
// enforces the budget across replicas.
func (t *SpiralTracker) MarkFired(key string) {
	t.fired.Add(key, struct{}{})
}

// SpiralFiredKey identifies one (session, role, reason) fire.
func SpiralFiredKey(sessionKey [sessionpin.SessionKeyLen]byte, role, reason string) string {
	return string(sessionKey[:]) + "\x00" + role + "\x00" + reason
}
