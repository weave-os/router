package translate

import (
	"strings"

	"weave-os/router/internal/router/catalog"
)

func usesGemini3Contract(model string) bool {
	return strings.HasPrefix(model, "gemini-3") || catalog.RequiresSignedToolHistory(model)
}
