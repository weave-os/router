package dispatch

import (
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/telemetry"
)

type TranslationCompatibilityMode string

const (
	TranslationCompatibilityOff     TranslationCompatibilityMode = "off"
	TranslationCompatibilityShadow  TranslationCompatibilityMode = "shadow"
	TranslationCompatibilityEnforce TranslationCompatibilityMode = "enforce"
)

type Config struct {
	Rescue                       RescueConfig
	ArmDemotion                  ArmDemotionPolicy
	Throttling                   Throttling
	Executor                     *Executor
	Plans                        *policy.PlanResolver
	Deployment                   policy.DeploymentPolicyConfig
	TranslationCompatibilityMode TranslationCompatibilityMode
	TranslationMetrics           telemetry.TranslationMetrics
}
