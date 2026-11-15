package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
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

type spiralReason string

const (
	spiralReasonErrStreak      spiralReason = "err_streak"
	spiralReasonSameFileThrash spiralReason = "same_file_thrash"
	spiralReasonRepetition     spiralReason = "repetition"
	spiralReasonMonologue      spiralReason = "monologue"
	spiralReasonPingPong       spiralReason = "ping_pong"
	spiralReasonNoProgress     spiralReason = "no_progress"
)

func spiralReasonStrings(reasons []spiralReason) []string {
	if reasons == nil {
		return nil
	}
	values := make([]string, len(reasons))
	for i, reason := range reasons {
		values[i] = string(reason)
	}
	return values
}

type SpiralShadowStore interface {
	InsertSpiralShadowEvent(ctx context.Context, p SpiralShadowEvent) error
	CountSpiralShadowEvents(ctx context.Context, sessionKey []byte, role, reason string) (count int64, err error)
}

type SpiralShadowEvent struct {
	InstallationID     string
	SessionKey         []byte
	Role               string
	RoutedModel        string
	TurnType           string
	Reason             string
	ErrStreak          int32
	ErroredResults     int32
	ToolResults        int32
	MaxSameFileEdits   int32
	SameFilePathHash   string
	RepeatFrac         float64
	MonologueLen       int32
	ToolCallCount      int32
	MessageCount       int32
	PingPongLen        int32
	StepsSinceProgress int32
}

type spiralSignals struct {
	errStats           translate.ToolResultErrorStats
	maxSameFileEdits   int
	sameFilePathHash   string
	repeatFrac         float64
	monologueLen       int
	toolCallCount      int
	messageCount       int
	pingPongLen        int
	stepsSinceProgress int
	editAttempted      bool
}

func computeSpiralSignals(env *translate.RequestEnvelope, messageCount int) spiralSignals {
	sigs := env.AssistantToolCallSignatures()
	s := spiralSignals{
		errStats:      env.ToolResultErrors(),
		monologueLen:  env.TrailingAssistantMonologue(),
		toolCallCount: len(sigs),
		messageCount:  messageCount,
	}

	pathCounts := make(map[string]int)
	for _, fp := range env.AssistantToolCallFilePaths() {
		if _, isEdit := editToolNames[fp.Name]; !isEdit {
			continue
		}
		pathCounts[fp.Path]++
		if pathCounts[fp.Path] > s.maxSameFileEdits {
			s.maxSameFileEdits = pathCounts[fp.Path]
			h := sha256.Sum256([]byte(fp.Path))
			s.sameFilePathHash = hex.EncodeToString(h[:8])
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
		s.repeatFrac = float64(repeated) / float64(len(window))
	}

	s.pingPongLen = trailingPingPongLen(sigs)
	s.stepsSinceProgress, s.editAttempted = stepsSinceProgress(env.AssistantToolCallOutcomes())
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

func spiralReasons(s spiralSignals) []spiralReason {
	if s.toolCallCount < spiralMinToolCalls {
		return nil
	}
	var reasons []spiralReason
	if s.errStats.TrailingErrStreak >= spiralErrStreakThreshold {
		reasons = append(reasons, spiralReasonErrStreak)
	}
	if s.maxSameFileEdits >= spiralSameFileEditThreshold {
		reasons = append(reasons, spiralReasonSameFileThrash)
	}
	if s.toolCallCount >= spiralRepeatMinCalls && s.repeatFrac >= spiralRepeatFracThreshold {
		reasons = append(reasons, spiralReasonRepetition)
	}
	if s.monologueLen >= spiralMonologueThreshold {
		reasons = append(reasons, spiralReasonMonologue)
	}
	if s.pingPongLen >= spiralPingPongThreshold {
		reasons = append(reasons, spiralReasonPingPong)
	}
	if s.editAttempted && s.stepsSinceProgress >= spiralNoProgressThreshold {
		reasons = append(reasons, spiralReasonNoProgress)
	}
	return reasons
}

const (
	spiralFiredCacheSize = 8192
	spiralFiredCacheTTL  = time.Hour
)

type spiralTracker struct {
	fired *lru.LRU[string, struct{}]
}

func newSpiralTracker() *spiralTracker {
	return &spiralTracker{
		fired: lru.NewLRU[string, struct{}](spiralFiredCacheSize, nil, spiralFiredCacheTTL),
	}
}

func spiralFiredKey(sessionKey [sessionpin.SessionKeyLen]byte, role, reason string) string {
	return string(sessionKey[:]) + "\x00" + role + "\x00" + reason
}

func (s *Service) handleSpiralShadow(
	ctx context.Context,
	sig spiralSignals,
	reasons []spiralReason,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	routedModel string,
	turnType string,
	trainingAllowed bool,
	capture ContentCaptureMode,
) {
	if !turnSignalCaptureAllowed(trainingAllowed, capture) {
		return
	}
	log := observability.FromContext(ctx)
	for _, reason := range reasons {
		reasonText := string(reason)
		key := spiralFiredKey(sessionKey, role, reasonText)
		if _, seen := s.spiralTracker.fired.Get(key); seen {
			continue
		}

		if s.spiralShadowStore != nil && installationID != uuid.Nil {
			count, err := s.spiralShadowStore.CountSpiralShadowEvents(ctx, sessionKey[:], role, reasonText)
			if err != nil {
				log.Error("spiral-shadow: budget lookup failed", "err", err)
			} else if count > 0 {
				s.spiralTracker.fired.Add(key, struct{}{})
				continue
			}
		}

		log.Info("router.spiral_shadow",
			"reason", reason,
			"routed_model", routedModel,
			"turn_type", turnType,
			"err_streak", sig.errStats.TrailingErrStreak,
			"errored_results", sig.errStats.Errored,
			"tool_results", sig.errStats.Total,
			"max_same_file_edits", sig.maxSameFileEdits,
			"same_file_path_hash", sig.sameFilePathHash,
			"repeat_frac", sig.repeatFrac,
			"monologue_len", sig.monologueLen,
			"tool_call_count", sig.toolCallCount,
			"message_count", sig.messageCount,
			"ping_pong_len", sig.pingPongLen,
			"steps_since_progress", sig.stepsSinceProgress,
			"session_key_prefix", shortSessionKey(sessionKey),
			"role", role,
		)

		if s.spiralShadowStore != nil && installationID != uuid.Nil {
			event := SpiralShadowEvent{
				InstallationID:     installationID.String(),
				SessionKey:         sessionKey[:],
				Role:               role,
				RoutedModel:        routedModel,
				TurnType:           turnType,
				Reason:             reasonText,
				ErrStreak:          int32(sig.errStats.TrailingErrStreak),
				ErroredResults:     int32(sig.errStats.Errored),
				ToolResults:        int32(sig.errStats.Total),
				MaxSameFileEdits:   int32(sig.maxSameFileEdits),
				SameFilePathHash:   sig.sameFilePathHash,
				RepeatFrac:         sig.repeatFrac,
				MonologueLen:       int32(sig.monologueLen),
				ToolCallCount:      int32(sig.toolCallCount),
				MessageCount:       int32(sig.messageCount),
				PingPongLen:        int32(sig.pingPongLen),
				StepsSinceProgress: int32(sig.stepsSinceProgress),
			}
			if err := s.spiralShadowStore.InsertSpiralShadowEvent(context.Background(), event); err != nil {
				log.Error("spiral-shadow: event insert failed", "err", err)
				continue
			}
		}
		s.spiralTracker.fired.Add(key, struct{}{})
	}
}
