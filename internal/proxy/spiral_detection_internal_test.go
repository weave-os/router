package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"weave-os/router/internal/translate"
)

// buildSpiralBody constructs an Anthropic-format request body from a compact
// turn script. Each entry is one of:
//
//	"call:<tool>:<args-json>"  -> assistant tool_use turn
//	"result:ok" / "result:err" -> user tool_result turn (is_error on err)
//	"result:text:<content>"    -> user tool_result turn with string content
//	"text:<content>"           -> assistant text-only turn
//	"user:<content>"           -> real user text turn
func buildSpiralBody(t *testing.T, turns []string) []byte {
	t.Helper()
	var msgs []map[string]any
	callIdx := 0
	for _, turn := range turns {
		parts := strings.SplitN(turn, ":", 3)
		switch parts[0] {
		case "call":
			callIdx++
			msgs = append(msgs, map[string]any{
				"role": "assistant",
				"content": []map[string]any{{
					"type":  "tool_use",
					"id":    fmt.Sprintf("toolu_%d", callIdx),
					"name":  parts[1],
					"input": json.RawMessage(parts[2]),
				}},
			})
		case "result":
			block := map[string]any{
				"type":        "tool_result",
				"tool_use_id": fmt.Sprintf("toolu_%d", callIdx),
				"content":     "ok",
			}
			switch parts[1] {
			case "err":
				block["is_error"] = true
				block["content"] = "command failed"
			case "text":
				block["content"] = parts[2]
			}
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": []map[string]any{block},
			})
		case "text":
			msgs = append(msgs, map[string]any{
				"role":    "assistant",
				"content": []map[string]any{{"type": "text", "text": parts[1]}},
			})
		case "user":
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": parts[1],
			})
		default:
			t.Fatalf("unknown turn directive %q", turn)
		}
	}
	body, err := json.Marshal(map[string]any{
		"model":    "claude-sonnet-4-6",
		"messages": msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func parseSpiralEnv(t *testing.T, turns []string) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic(buildSpiralBody(t, turns))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// readGrind appends n distinct read calls with ok results — healthy filler
// that satisfies the arming floor without tripping any signal.
func readGrind(n, seed int) []string {
	var turns []string
	for i := 0; i < n; i++ {
		turns = append(turns,
			fmt.Sprintf(`call:Read:{"file_path":"/src/f%d_%d.go"}`, seed, i),
			"result:ok",
		)
	}
	return turns
}

func TestSpiralSignals_HealthySessionBelowThresholds(t *testing.T) {
	env := parseSpiralEnv(t, readGrind(15, 0))
	sig := computeSpiralSignals(env, 30)
	if reasons := spiralReasons(sig); len(reasons) != 0 {
		t.Fatalf("healthy session fired %v (signals %+v)", reasons, sig)
	}
}

func TestSpiralSignals_BelowArmingFloorNeverFires(t *testing.T) {
	// Heavy error streak, but only 5 tool calls — under spiralMinToolCalls.
	var turns []string
	for i := 0; i < 5; i++ {
		turns = append(turns, fmt.Sprintf(`call:Bash:{"command":"make test%d"}`, i), "result:err")
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 10)
	if sig.errStats.TrailingErrStreak != 5 {
		t.Fatalf("err streak = %d, want 5", sig.errStats.TrailingErrStreak)
	}
	if reasons := spiralReasons(sig); len(reasons) != 0 {
		t.Fatalf("under-floor session fired %v", reasons)
	}
}

func TestSpiralSignals_ErrStreak(t *testing.T) {
	turns := readGrind(10, 0)
	for i := 0; i < spiralErrStreakThreshold; i++ {
		turns = append(turns, fmt.Sprintf(`call:Bash:{"command":"pytest -k t%d"}`, i), "result:err")
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 30)
	reasons := spiralReasons(sig)
	if len(reasons) != 1 || reasons[0] != spiralReasonErrStreak {
		t.Fatalf("reasons = %v, want [%s] (signals %+v)", reasons, spiralReasonErrStreak, sig)
	}
}

func TestSpiralSignals_ErrStreakResetByHealthyResult(t *testing.T) {
	turns := readGrind(10, 0)
	// Errors interleaved with successes — never a streak.
	for i := 0; i < 6; i++ {
		turns = append(turns,
			fmt.Sprintf(`call:Bash:{"command":"pytest -k u%d"}`, i), "result:err",
			fmt.Sprintf(`call:Read:{"file_path":"/src/x%d.go"}`, i), "result:ok",
		)
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 40)
	if sig.errStats.TrailingErrStreak != 0 {
		t.Fatalf("trailing streak = %d, want 0", sig.errStats.TrailingErrStreak)
	}
	if sig.errStats.Errored != 6 {
		t.Fatalf("errored = %d, want 6", sig.errStats.Errored)
	}
	for _, r := range spiralReasons(sig) {
		if r == spiralReasonErrStreak {
			t.Fatal("err_streak fired without a trailing streak")
		}
	}
}

func TestSpiralSignals_ErrorMarkerWithoutFlag(t *testing.T) {
	turns := readGrind(10, 0)
	for i := 0; i < spiralErrStreakThreshold; i++ {
		turns = append(turns,
			fmt.Sprintf(`call:Bash:{"command":"pytest -k m%d"}`, i),
			"result:text:============ FAILED tests/test_app.py::test_x ============",
		)
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 30)
	if sig.errStats.TrailingErrStreak != spiralErrStreakThreshold {
		t.Fatalf("marker-only streak = %d, want %d", sig.errStats.TrailingErrStreak, spiralErrStreakThreshold)
	}
}

func TestSpiralSignals_SameFileThrash(t *testing.T) {
	turns := readGrind(8, 0)
	for i := 0; i < spiralSameFileEditThreshold; i++ {
		// Same file, different args each time — exactly the rhyming-spiral shape.
		turns = append(turns,
			fmt.Sprintf(`call:Edit:{"file_path":"/src/core.py","old_string":"a%d","new_string":"b%d"}`, i, i),
			"result:ok",
		)
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 30)
	if sig.maxSameFileEdits != spiralSameFileEditThreshold {
		t.Fatalf("maxSameFileEdits = %d, want %d", sig.maxSameFileEdits, spiralSameFileEditThreshold)
	}
	if sig.sameFilePathHash == "" {
		t.Fatal("sameFilePathHash empty")
	}
	found := false
	for _, r := range spiralReasons(sig) {
		if r == spiralReasonSameFileThrash {
			found = true
		}
	}
	if !found {
		t.Fatalf("same_file_thrash missing from %v", spiralReasons(sig))
	}
}

func TestSpiralSignals_HealthyRefactorManyFilesNoThrash(t *testing.T) {
	// A long refactor edits MANY DISTINCT files — must not fire.
	turns := readGrind(8, 0)
	for i := 0; i < 12; i++ {
		turns = append(turns,
			fmt.Sprintf(`call:Edit:{"file_path":"/src/mod%d.py","old_string":"a","new_string":"b"}`, i),
			"result:ok",
		)
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 50)
	if reasons := spiralReasons(sig); len(reasons) != 0 {
		t.Fatalf("healthy refactor fired %v (signals %+v)", reasons, sig)
	}
}

func TestSpiralSignals_Repetition(t *testing.T) {
	// 12 distinct calls to clear the min-calls floor, then a tail where the
	// same 3 calls cycle — high recent repeat fraction.
	turns := readGrind(12, 0)
	for i := 0; i < 4; i++ {
		for j := 0; j < 3; j++ {
			turns = append(turns,
				fmt.Sprintf(`call:Bash:{"command":"cycle-%d"}`, j),
				"result:ok",
			)
		}
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 60)
	if sig.repeatFrac != 1.0 {
		t.Fatalf("repeatFrac = %v, want 1.0", sig.repeatFrac)
	}
	found := false
	for _, r := range spiralReasons(sig) {
		if r == spiralReasonRepetition {
			found = true
		}
	}
	if !found {
		t.Fatalf("repetition missing from %v", spiralReasons(sig))
	}
}

func TestSpiralSignals_Monologue(t *testing.T) {
	turns := readGrind(12, 0)
	for i := 0; i < spiralMonologueThreshold; i++ {
		turns = append(turns, fmt.Sprintf("text:Let me reconsider the approach, attempt %d.", i))
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 40)
	if sig.monologueLen != spiralMonologueThreshold {
		t.Fatalf("monologueLen = %d, want %d", sig.monologueLen, spiralMonologueThreshold)
	}
	found := false
	for _, r := range spiralReasons(sig) {
		if r == spiralReasonMonologue {
			found = true
		}
	}
	if !found {
		t.Fatalf("monologue missing from %v", spiralReasons(sig))
	}
}

func TestSpiralSignals_MonologueResetByUserTurn(t *testing.T) {
	turns := readGrind(12, 0)
	turns = append(turns, "text:thinking...", "text:still thinking...")
	turns = append(turns, "user:try the other module")
	turns = append(turns, "text:on it")
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 40)
	if sig.monologueLen != 1 {
		t.Fatalf("monologueLen = %d, want 1 (user turn resets)", sig.monologueLen)
	}
}

func TestSpiralSignals_PingPong(t *testing.T) {
	// Strict A/B/A/B tail: the agent flips between the same two actions,
	// each individually below the repetition window floor.
	turns := readGrind(12, 0)
	for i := 0; i < spiralPingPongThreshold/2; i++ {
		turns = append(turns,
			`call:Read:{"file_path":"/src/a.go"}`, "result:ok",
			`call:Edit:{"file_path":"/src/a.go","old_string":"x","new_string":"y"}`, "result:ok",
		)
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 60)
	if sig.pingPongLen != spiralPingPongThreshold {
		t.Fatalf("pingPongLen = %d, want %d", sig.pingPongLen, spiralPingPongThreshold)
	}
	found := false
	for _, r := range spiralReasons(sig) {
		if r == spiralReasonPingPong {
			found = true
		}
	}
	if !found {
		t.Fatalf("ping_pong missing from %v", spiralReasons(sig))
	}
}

func TestSpiralSignals_PingPongNeedsTwoDistinctActions(t *testing.T) {
	// One signature repeated is repetition, not ping-pong: alternation must
	// mean the agent is oscillating between two things.
	turns := readGrind(12, 0)
	for i := 0; i < 8; i++ {
		turns = append(turns, `call:Bash:{"command":"make test"}`, "result:ok")
	}
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 60)
	if sig.pingPongLen != 0 {
		t.Fatalf("pingPongLen = %d, want 0 for a single repeated signature", sig.pingPongLen)
	}
}

func TestSpiralSignals_PingPongBrokenByThirdAction(t *testing.T) {
	turns := readGrind(12, 0)
	for i := 0; i < 3; i++ {
		turns = append(turns,
			`call:Read:{"file_path":"/src/a.go"}`, "result:ok",
			`call:Read:{"file_path":"/src/b.go"}`, "result:ok",
		)
	}
	turns = append(turns, `call:Bash:{"command":"make build"}`, "result:ok")
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 60)
	if sig.pingPongLen != 0 {
		t.Fatalf("pingPongLen = %d, want 0 once a third action lands at the tail", sig.pingPongLen)
	}
}

func TestSpiralSignals_NoProgressAfterFailedEdits(t *testing.T) {
	turns := []string{
		`call:Edit:{"file_path":"/src/a.go","old_string":"x","new_string":"y"}`, "result:err",
	}
	turns = append(turns, readGrind(spiralNoProgressThreshold-1, 1)...)
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 60)
	if !sig.editAttempted {
		t.Fatal("editAttempted = false after an Edit call")
	}
	if sig.stepsSinceProgress != spiralNoProgressThreshold {
		t.Fatalf("stepsSinceProgress = %d, want %d", sig.stepsSinceProgress, spiralNoProgressThreshold)
	}
	found := false
	for _, r := range spiralReasons(sig) {
		if r == spiralReasonNoProgress {
			found = true
		}
	}
	if !found {
		t.Fatalf("no_progress missing from %v", spiralReasons(sig))
	}
}

func TestSpiralSignals_NoProgressResetBySuccessfulEdit(t *testing.T) {
	turns := []string{
		`call:Edit:{"file_path":"/src/a.go","old_string":"x","new_string":"y"}`, "result:err",
	}
	turns = append(turns, readGrind(spiralNoProgressThreshold, 1)...)
	turns = append(turns,
		`call:Edit:{"file_path":"/src/a.go","old_string":"x","new_string":"z"}`, "result:ok",
	)
	turns = append(turns, readGrind(2, 2)...)
	env := parseSpiralEnv(t, turns)
	sig := computeSpiralSignals(env, 60)
	if sig.stepsSinceProgress != 2 {
		t.Fatalf("stepsSinceProgress = %d, want 2 (counted from the landed edit)", sig.stepsSinceProgress)
	}
	for _, r := range spiralReasons(sig) {
		if r == spiralReasonNoProgress {
			t.Fatalf("no_progress fired after a successful edit (signals %+v)", sig)
		}
	}
}

func TestSpiralSignals_LongExploreIsNotNoProgress(t *testing.T) {
	// An Explore phase makes no edits at all; steps-since-progress has no
	// baseline to measure against and must stay silent.
	env := parseSpiralEnv(t, readGrind(spiralNoProgressThreshold*2, 3))
	sig := computeSpiralSignals(env, 80)
	if sig.editAttempted {
		t.Fatal("editAttempted = true without any edit call")
	}
	for _, r := range spiralReasons(sig) {
		if r == spiralReasonNoProgress {
			t.Fatalf("no_progress fired on an edit-free session (signals %+v)", sig)
		}
	}
}
