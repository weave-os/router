package auth

import (
	"context"

	lru "github.com/hashicorp/golang-lru/v2"

	"weave-os/router/internal/observability"
)

const blindExperimentSessionArmCacheSize = 100_000

// BlindExperimentSessionRepository reads installation-wide session-mode
// configuration and records the first arm served to each session.
type BlindExperimentSessionRepository interface {
	GetForInstallation(ctx context.Context, installationID string) (BlindExperimentRecord, error)
	ClaimSessionArm(ctx context.Context, installationID, sessionSubjectKey string, arm BlindExperimentArm) (BlindExperimentArm, error)
}

// WithBlindExperimentSessions stores repo and allocates the in-process LRU of
// frozen session arms.
func (s *Service) WithBlindExperimentSessions(repo BlindExperimentSessionRepository) *Service {
	// lru.New only errors for a non-positive size; the cache size is a positive constant.
	arms, _ := lru.New[string, BlindExperimentArm](blindExperimentSessionArmCacheSize)
	s.blindExperimentSessions = repo
	s.blindExperimentSessionArms = arms
	return s
}

// WithBlindExperimentSession applies the session-mode draw for a
// credential-scoped client session. The first arm recorded for a session is
// kept, so configuration changes only affect new sessions.
func (s *Service) WithBlindExperimentSession(ctx context.Context, installationID, sessionSubjectKey string) context.Context {
	if sessionSubjectKey == "" {
		return ctx
	}
	state, found := ctx.Value(BlindExperimentContextKey{}).(BlindExperimentState)
	if !found && UserIDFrom(ctx) == "" {
		state, found = s.installationSessionExperiment(ctx, installationID)
	}
	if !found {
		return ctx
	}
	sessionState := state.ForSession(sessionSubjectKey)
	if sessionState.SessionSubjectKey == "" {
		return ctx
	}
	sessionState.Arm = s.frozenSessionArm(ctx, installationID, sessionSubjectKey, sessionState.Arm)
	return context.WithValue(ctx, BlindExperimentContextKey{}, sessionState)
}

func (s *Service) installationSessionExperiment(ctx context.Context, installationID string) (BlindExperimentState, bool) {
	if s.blindExperimentSessions == nil || installationID == "" {
		return BlindExperimentState{}, false
	}
	generation := s.blindExperimentCache.InstallationGeneration(installationID)
	cacheKey := "installation:" + installationID
	state, cached := s.blindExperimentCache.GetAtGeneration(installationID, cacheKey, generation)
	if !cached {
		record, err := s.blindExperimentSessions.GetForInstallation(ctx, installationID)
		if err != nil {
			observability.FromContext(ctx).Warn("Failed to load blind router experiment for an unattributed session", "installation_id", installationID, "err", err)
			return BlindExperimentState{}, false
		}
		if record.CohortExperimentID == "" {
			state = resolveBlindExperiment(record, "")
		}
		s.blindExperimentCache.SetAtGeneration(installationID, cacheKey, state, generation)
	}
	return state, state.Active && state.Mode == BlindExperimentModeSession
}

func (s *Service) frozenSessionArm(ctx context.Context, installationID, sessionSubjectKey string, drawn BlindExperimentArm) BlindExperimentArm {
	if s.blindExperimentSessions == nil || installationID == "" {
		return drawn
	}
	cacheKey := installationID + "\x00" + sessionSubjectKey
	if arm, found := s.blindExperimentSessionArms.Get(cacheKey); found {
		return arm
	}
	arm, err := s.blindExperimentSessions.ClaimSessionArm(ctx, installationID, sessionSubjectKey, drawn)
	if err != nil {
		observability.FromContext(ctx).Warn("Failed to record blind router experiment session arm; using the current draw", "installation_id", installationID, "err", err)
		return drawn
	}
	s.blindExperimentSessionArms.Add(cacheKey, arm)
	return arm
}
