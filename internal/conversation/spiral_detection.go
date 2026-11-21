package conversation

import (
	"crypto/sha256"
	"encoding/hex"

	"weave-os/router/internal/translate"
)

const (
	spiralMinToolCalls          = 12
	spiralErrStreakThreshold    = 3
	spiralSameFileEditThreshold = 5
	spiralRepeatWindow          = 12
	spiralRepeatMinCalls        = 20
	spiralRepeatFracThreshold   = 0.34
	spiralMonologueThreshold    = 4
	spiralPingPongThreshold     = 6
	spiralNoProgressThreshold   = 15
)

type SpiralReason string

const (
	SpiralReasonErrStreak      SpiralReason = "err_streak"
	spiralReasonSameFileThrash SpiralReason = "same_file_thrash"
	spiralReasonRepetition     SpiralReason = "repetition"
	spiralReasonMonologue      SpiralReason = "monologue"
	SpiralReasonPingPong       SpiralReason = "ping_pong"
	spiralReasonNoProgress     SpiralReason = "no_progress"
)

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

type SpiralSignals struct {
	ErrStats           translate.ToolResultErrorStats
	MaxSameFileEdits   int
	SameFilePathHash   string
	RepeatFrac         float64
	MonologueLen       int
	ToolCallCount      int
	MessageCount       int
	PingPongLen        int
	StepsSinceProgress int
	EditAttempted      bool
}

func ComputeSpiralSignals(env *translate.RequestEnvelope, messageCount int) SpiralSignals {
	sigs := env.AssistantToolCallSignatures()
	s := SpiralSignals{
		ErrStats:      env.ToolResultErrors(),
		MonologueLen:  env.TrailingAssistantMonologue(),
		ToolCallCount: len(sigs),
		MessageCount:  messageCount,
	}

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

var editToolNames = map[string]struct{}{
	"Edit": {}, "Write": {}, "MultiEdit": {}, "NotebookEdit": {},
}
