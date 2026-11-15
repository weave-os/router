package policy

import (
	"errors"

	"weave-os/router/internal/router"
)

type ClassifierErrorCode string

const ClassifierWarmingCode ClassifierErrorCode = "qwen_warming"

var ErrClassifierWarming = errors.New("qwen classifier is warming")

// ClassifierWarmingMessage is the client-facing text for ErrClassifierWarming.
const ClassifierWarmingMessage = "Qwen classifier is warming; run wv router prepare-model " + string(router.ModelQwen) + " before retrying"
