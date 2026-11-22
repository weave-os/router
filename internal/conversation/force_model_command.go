package conversation

import (
	"context"
	"encoding/hex"
	"fmt"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

type ForceModelBinder interface {
	Binding(ctx context.Context, model, provider string) (binding, refusalReason string, refused bool)
}

type ForceModelResolver interface {
	ForceModelBinder
	Resolve(model string) (canonicalID, provider string, known bool, effort string)
	Suggest(input string) string
}

type ForceModelPinStore interface {
	Set(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, canonicalModel, provider, effort, forcedThreadKey string) error
	Clear(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte) error
}

type ForceModelControls interface {
	ClearLegacyPins(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte) error
	RecordCommandBoundary(ctx context.Context, installationID uuid.UUID, threadKey [sessionpin.SessionKeyLen]byte, commandID string)
}

type ForceModelCommand struct {
	Resolver ForceModelResolver
	Pins     ForceModelPinStore
	Controls ForceModelControls
}

type ForceModelRequest struct {
	Command              translate.ForceModelResult
	InstallationID       uuid.UUID
	ThreadSessionKey     [sessionpin.SessionKeyLen]byte
	ForceModelSessionKey [sessionpin.SessionKeyLen]byte
	CommandID            string
	UnforceCommand       string
}

type ForceModelRejection string

const (
	ForceModelUnknown  ForceModelRejection = "unknown_model"
	ForceModelExcluded ForceModelRejection = "excluded_model"
)

type ForceModelOutcome struct {
	Rejection    ForceModelRejection
	InputModel   string
	Suggestion   string
	Reason       string
	Pin          translate.CommandPin
	ShownModel   string
	ClearCommand string
	ParentOnly   bool
}

func (c ForceModelCommand) Apply(ctx context.Context, req ForceModelRequest) (ForceModelOutcome, error) {
	if req.Command.Clear {
		return c.clear(ctx, req)
	}
	return c.force(ctx, req)
}

func (c ForceModelCommand) clear(ctx context.Context, req ForceModelRequest) (ForceModelOutcome, error) {
	log := observability.FromContext(ctx)
	if err := c.Controls.ClearLegacyPins(ctx, req.InstallationID, req.ThreadSessionKey); err != nil {
		log.Error("/unforce-model: legacy pin cleanup failed", "err", err)
		return ForceModelOutcome{}, err
	}
	if err := c.Pins.Clear(ctx, req.InstallationID, req.ForceModelSessionKey); err != nil {
		log.Error("/unforce-model: session pin clear failed", "err", err)
		return ForceModelOutcome{}, err
	}
	out := ForceModelOutcome{Pin: translate.CommandPin{Transition: translate.PinCleared}}
	c.Controls.RecordCommandBoundary(ctx, req.InstallationID, req.ThreadSessionKey, req.CommandID)
	log.Debug("/unforce-model: session pin cleared",
		"force_model_session_key_hex", fmt.Sprintf("%x", req.ForceModelSessionKey),
		"role", routingpolicy.ForceModelSessionRole,
	)
	return out, nil
}

func (c ForceModelCommand) force(ctx context.Context, req ForceModelRequest) (ForceModelOutcome, error) {
	log := observability.FromContext(ctx)
	cmd := req.Command
	canonicalModel, provider, known, effortLevel := c.Resolver.Resolve(cmd.Model)
	if !known {
		suggestion := c.Resolver.Suggest(cmd.Model)
		log.Info("/force-model: rejected unknown model",
			"input_model", cmd.Model,
			"suggested_model", suggestion,
			"force_model_session_key_hex", fmt.Sprintf("%x", req.ForceModelSessionKey),
			"role", routingpolicy.ForceModelSessionRole,
		)
		return ForceModelOutcome{Rejection: ForceModelUnknown, InputModel: cmd.Model, Suggestion: suggestion}, nil
	}

	binding, refusalReason, refused := c.Resolver.Binding(ctx, canonicalModel, provider)
	if refused {
		log.Warn("/force-model: rejected excluded model",
			"input_model", cmd.Model,
			"canonical_model", canonicalModel,
			"provider", provider,
			"reason", refusalReason,
			"force_model_session_key_hex", fmt.Sprintf("%x", req.ForceModelSessionKey),
			"role", routingpolicy.ForceModelSessionRole,
		)
		return ForceModelOutcome{Rejection: ForceModelExcluded, Reason: refusalReason}, nil
	}

	if err := c.Controls.ClearLegacyPins(ctx, req.InstallationID, req.ThreadSessionKey); err != nil {
		log.Error("/force-model: legacy pin cleanup failed", "err", err)
		return ForceModelOutcome{}, err
	}
	forcedThreadKey := ""
	commandName := "force-model"
	if cmd.ParentOnly {
		forcedThreadKey = threadKeyHex(req.ThreadSessionKey)
		commandName = "force-parent"
	}
	if err := c.Pins.Set(ctx, req.ForceModelSessionKey, req.InstallationID, canonicalModel, binding, effortLevel, forcedThreadKey); err != nil {
		log.Error("/force-model: session pin upsert failed", "err", err)
		return ForceModelOutcome{}, err
	}
	shownModel := canonicalModel
	if effortLevel != "" {
		shownModel = canonicalModel + ":" + effortLevel
	}
	out := ForceModelOutcome{
		Pin:          translate.CommandPin{Transition: translate.PinApplied, Model: canonicalModel, Provider: binding, Effort: effortLevel},
		ShownModel:   shownModel,
		ClearCommand: req.UnforceCommand,
		ParentOnly:   cmd.ParentOnly,
	}
	c.Controls.RecordCommandBoundary(ctx, req.InstallationID, req.ThreadSessionKey, req.CommandID)
	log.Debug("/"+commandName+": session pin set",
		"forced_thread_key", forcedThreadKey,
		"input_model", cmd.Model,
		"canonical_model", canonicalModel,
		"provider", binding,
		"effort", effortLevel,
		"force_model_session_key_hex", fmt.Sprintf("%x", req.ForceModelSessionKey),
		"role", routingpolicy.ForceModelSessionRole,
	)
	return out, nil
}

func threadKeyHex(key [sessionpin.SessionKeyLen]byte) string {
	if key == ([sessionpin.SessionKeyLen]byte{}) {
		return ""
	}
	return hex.EncodeToString(key[:])
}
