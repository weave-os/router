package floor

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/escalationdashboard"
	"weave-os/router/internal/router/llmescalation"
)

var ErrEscalationJudgeUnavailable = errors.New("escalation judge is unavailable")

const escalationDashboardSnapshotTTL = 15 * time.Minute

type Admin struct {
	Dashboard           escalationdashboard.Store
	Sessions            llmescalation.Store
	Configuration       llmescalation.ConfigurationStore
	InvalidateSelection func(string)
	SwitchyardJudge     llmescalation.Judge
	QwenJudge           llmescalation.Judge
	SwitchyardActive    bool
	Clock               auth.Clock
}

func (a *Admin) switchyardJudgeReady() bool { return a.SwitchyardJudge != nil }

func (a *Admin) qwenJudgeReady() bool { return a.QwenJudge != nil }

func (a *Admin) invalidateSelection(installationID string) {
	if a.InvalidateSelection != nil {
		a.InvalidateSelection(installationID)
	}
}

func (a *Admin) EscalationDashboard(ctx context.Context, filter escalationdashboard.Filter) (escalationdashboard.Snapshot, error) {
	if a.Dashboard == nil {
		return escalationdashboard.Snapshot{}, ErrEscalationJudgeUnavailable
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		return escalationdashboard.Snapshot{}, errors.New("invalid escalation dashboard page")
	}
	now := a.Clock.Now().UTC()
	pageStart := int32(0)
	var stored escalationdashboard.StoredSnapshot
	var err error
	if filter.Cursor == "" {
		filter.CapturedAt = now
		filter.ExpiresAt = now.Add(escalationDashboardSnapshotTTL)
		stored, err = a.Dashboard.CreateSnapshot(ctx, filter)
	} else {
		var snapshotID string
		snapshotID, pageStart, err = escalationdashboard.DecodeCursor(filter.Cursor)
		if err == nil {
			stored, err = a.Dashboard.SnapshotPage(ctx, snapshotID, pageStart, filter.Limit, now)
		}
	}
	if err != nil {
		return escalationdashboard.Snapshot{}, err
	}
	if pageStart > int32(stored.MatchingSessions) {
		return escalationdashboard.Snapshot{}, fmt.Errorf("%w: page starts beyond the snapshot", escalationdashboard.ErrInvalidCursor)
	}
	stored.PageStart = int(pageStart)
	pageEnd := int64(pageStart) + int64(len(stored.Sessions))
	if pageEnd < int64(stored.MatchingSessions) {
		stored.NextCursor, err = escalationdashboard.EncodeCursor(stored.SnapshotID, int32(pageEnd))
		if err != nil {
			return escalationdashboard.Snapshot{}, err
		}
	}
	if pageStart > 0 {
		previousStart := pageStart - filter.Limit
		if previousStart < 0 {
			previousStart = 0
		}
		stored.PreviousCursor, err = escalationdashboard.EncodeCursor(stored.SnapshotID, previousStart)
		if err != nil {
			return escalationdashboard.Snapshot{}, err
		}
	}
	stored.HasMore = stored.NextCursor != ""
	return stored.Snapshot, nil
}

type LLMEscalationSnapshot struct {
	Sessions []llmescalation.Session `json:"sessions"`
	Summary  llmescalation.Summary   `json:"summary"`
	HasMore  bool                    `json:"has_more"`
}

type LLMEscalationDetail struct {
	Session     llmescalation.Session `json:"session"`
	Checkpoints []llmescalation.Job   `json:"checkpoints"`
}

func (a *Admin) ListLLMEscalationSessions(ctx context.Context, installationID string, limit, offset int32) (LLMEscalationSnapshot, error) {
	if a.Sessions == nil {
		return LLMEscalationSnapshot{}, ErrEscalationJudgeUnavailable
	}
	if offset < 0 || limit < 1 || limit > 200 {
		return LLMEscalationSnapshot{}, errors.New("invalid escalation page")
	}
	sessions, err := a.Sessions.ListSessions(ctx, installationID, limit+1, offset)
	if err != nil {
		return LLMEscalationSnapshot{}, err
	}
	hasMore := len(sessions) > int(limit)
	if hasMore {
		sessions = sessions[:int(limit)]
	}
	summary, err := a.Sessions.Summary(ctx, installationID)
	if err != nil {
		return LLMEscalationSnapshot{}, err
	}
	return LLMEscalationSnapshot{Sessions: sessions, Summary: summary, HasMore: hasMore}, nil
}

func (a *Admin) GetLLMEscalationSession(ctx context.Context, installationID, encodedScope string) (LLMEscalationDetail, bool, error) {
	if a.Sessions == nil {
		return LLMEscalationDetail{}, false, ErrEscalationJudgeUnavailable
	}
	decoded, err := hex.DecodeString(encodedScope)
	if err != nil || len(decoded) != 32 {
		return LLMEscalationDetail{}, false, errors.New("invalid escalation scope")
	}
	var scope [32]byte
	copy(scope[:], decoded)
	session, jobs, found, err := a.Sessions.GetSession(ctx, installationID, scope)
	return LLMEscalationDetail{Session: session, Checkpoints: jobs}, found, err
}

func (a *Admin) EscalationSelection(ctx context.Context, installationID string) (llmescalation.Selection, error) {
	if a.Configuration == nil {
		return llmescalation.Selection{}, ErrEscalationJudgeUnavailable
	}
	selection, err := a.Configuration.GetSelection(ctx, installationID)
	if err != nil {
		return selection, err
	}
	return a.withEscalationReadiness(selection), nil
}

func (a *Admin) UpdateEscalationSelection(ctx context.Context, installationID string, update llmescalation.SelectionUpdate) (llmescalation.Selection, error) {
	if a.Configuration == nil {
		return llmescalation.Selection{}, ErrEscalationJudgeUnavailable
	}
	usesJudge := update.Active == flags.EscalationClassifierSwitchyard || update.Shadow == flags.EscalationClassifierSwitchyard
	if usesJudge && !a.switchyardJudgeReady() {
		return llmescalation.Selection{}, fmt.Errorf("%w: deployment capability is disabled", ErrEscalationJudgeUnavailable)
	}
	usesQwen := update.Active == flags.EscalationClassifierLLM || update.Shadow == flags.EscalationClassifierLLM
	if usesQwen && !a.qwenJudgeReady() {
		return llmescalation.Selection{}, fmt.Errorf("%w: Qwen deployment capability is disabled", ErrEscalationJudgeUnavailable)
	}
	if update.Active == flags.EscalationClassifierSwitchyard && !a.SwitchyardActive {
		return llmescalation.Selection{}, fmt.Errorf("%w: active rollout is disabled", ErrEscalationJudgeUnavailable)
	}
	selection, err := a.Configuration.SetSelection(ctx, installationID, update)
	if err != nil {
		return selection, err
	}
	a.invalidateSelection(installationID)
	return a.withEscalationReadiness(selection), nil
}

func (a *Admin) withEscalationReadiness(selection llmescalation.Selection) llmescalation.Selection {
	selection.Ready = true
	if selection.Active == flags.EscalationClassifierSwitchyard || selection.Shadow == flags.EscalationClassifierSwitchyard {
		selection.Ready = a.switchyardJudgeReady()
	}
	if selection.Active == flags.EscalationClassifierLLM || selection.Shadow == flags.EscalationClassifierLLM {
		selection.Ready = selection.Ready && a.qwenJudgeReady()
	}
	if !selection.Ready {
		selection.UnavailableReason = "deployment capability is disabled"
	} else if selection.Active == flags.EscalationClassifierSwitchyard && !a.SwitchyardActive {
		selection.Ready = false
		selection.UnavailableReason = "active rollout is disabled"
	}
	return selection
}
