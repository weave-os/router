package hmm

import "weave-os/router/internal/router/continuity"

type Config = continuity.AuthoritativeConfig

type UpgradeShadowConfig = continuity.UpgradeShadowConfig

func DefaultConfig() Config { return continuity.DefaultAuthoritativeConfig() }
