package policyregistry

import (
	"context"
	"errors"

	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions/entitlement"
)

var ErrRoutingModelChanged = errors.New("start a new conversation to change routing model")

var ErrRoutingModelSessionRequired = errors.New("experimental routing models require a conversation ID; send Session-Id")

func ExperimentalRoutingModelsPermitted(target ServingTarget, plan entitlement.Plan) bool {
	return target == TargetInternal && (plan == "" || plan == entitlement.PlanBoost)
}

func (c *AdmissionDecisionCache) AdmitRoutingModel(ctx context.Context, installationID, apiKeyID, sessionID string, model router.RoutingModel, decide AdmissionDecision) (AdmissionScope, SessionReleaseBinding, error) {
	if !router.IsRoutingModel(string(model)) {
		return AdmissionScope{}, SessionReleaseBinding{}, errors.New("invalid routing model admission")
	}
	bind := func(ctx context.Context, admission SerializedAdmission) (SessionReleaseBinding, error) {
		if ExperimentalRoutingModelsPermitted(admission.Projection.Target, admission.Projection.Plan) {
			if sessionID == "" && model != router.ModelAutomatic {
				return SessionReleaseBinding{}, ErrRoutingModelSessionRequired
			}
			if previous := admission.Previous; previous != nil && previous.Target == TargetInternal && previous.RoutingModel != "" && previous.RoutingModel != model {
				return SessionReleaseBinding{}, ErrRoutingModelChanged
			}
		}
		binding, err := decide(ctx, admission)
		if err != nil {
			return SessionReleaseBinding{}, err
		}
		if ExperimentalRoutingModelsPermitted(binding.Target, binding.Plan) && sessionID != "" {
			binding.RoutingModel = model
		}
		return binding, nil
	}
	return c.admit(ctx, installationID, apiKeyID, sessionID, model, bind)
}
