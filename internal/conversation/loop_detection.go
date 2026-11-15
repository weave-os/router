package conversation

import (
	"context"
	"strings"

	"weave-os/router/internal/translate"
)

// LoopEscalationStore persists cyclic-loop detections (one row per
// session+role); CountLoopEscalationEvents enforces the once-per-session budget.
type LoopEscalationStore interface {
	InsertLoopEscalationEvent(ctx context.Context, p LoopEscalationEvent) error
	CountLoopEscalationEvents(ctx context.Context, sessionKey []byte, role string) (count int64, err error)
}

// LoopEscalationEvent mirrors one router.loop_escalation_events row.
type LoopEscalationEvent struct {
	InstallationID   string
	SessionKey       []byte
	Role             string
	LoopingModel     string
	Action           string
	EscalationTarget string
	LoopTool         string
	LoopInputHash    string
	RepeatCount      int32
	DistinctRatio    float64
	WindowSize       int32
}

// Cyclic-loop-detection knobs. This catches a WIDER cycle — re-reading a few
// files for dozens of turns (seen post-#332: gpt-5.5/haiku re-read x45+ over
// 400 turns).
const (
	cyclicLoopWindowSize       = 30
	cyclicLoopMinCalls         = 24
	cyclicLoopMaxDistinctRatio = 0.4
)

// editToolNames are tool calls that constitute real progress; their presence in
// the window means the agent is changing the repo, not stuck re-reading.
var editToolNames = map[string]struct{}{
	"Edit": {}, "Write": {}, "MultiEdit": {}, "NotebookEdit": {},
}

func isCodexPollingCall(args string) bool {
	return strings.HasPrefix(args, "exec:") &&
		strings.Contains(args, "write_stdin") &&
		strings.Contains(args, `\"chars\":\"\"`)
}

// DetectCyclicToolCallLoop reports a wide re-read cycle: cyclicLoopMinCalls+
// calls with distinct-signature ratio below cyclicLoopMaxDistinctRatio and no
// edit/write call in the window (the #271 false-positive guard — a healthy
// Explore reads many distinct files; a stuck agent re-reads the same few).
func DetectCyclicToolCallLoop(env *translate.RequestEnvelope) (looped bool, top translate.ToolCallSig, topCount int, distinctRatio float64, total int) {
	sigs := env.AssistantToolCallSignatures()
	if len(sigs) < cyclicLoopMinCalls {
		return false, translate.ToolCallSig{}, 0, 0, 0
	}
	start := 0
	if len(sigs) > cyclicLoopWindowSize {
		start = len(sigs) - cyclicLoopWindowSize
	}
	window := sigs[start:]
	args := env.AssistantToolCallArgsPreview(start, 200)
	counts := make(map[string]int, len(window))
	keys := make(map[string]translate.ToolCallSig, len(window))
	nonPollingCount := 0
	for i, s := range window {
		if i < len(args) && isCodexPollingCall(args[i]) {
			continue
		}
		nonPollingCount++
		if _, isEdit := editToolNames[s.Name]; isEdit {
			// Real progress in the window — not a stuck loop.
			return false, translate.ToolCallSig{}, 0, 0, len(window)
		}
		key := s.Name + "\x00" + s.InputHash
		counts[key]++
		keys[key] = s
	}
	if nonPollingCount < cyclicLoopMinCalls {
		return false, translate.ToolCallSig{}, 0, 0, len(window)
	}
	distinctRatio = float64(len(counts)) / float64(nonPollingCount)
	if distinctRatio >= cyclicLoopMaxDistinctRatio {
		return false, translate.ToolCallSig{}, 0, distinctRatio, len(window)
	}
	for k, c := range counts {
		if c > topCount {
			topCount, top = c, keys[k]
		}
	}
	return true, top, topCount, distinctRatio, len(window)
}
