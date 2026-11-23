package ingress

import (
	"net/http"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

const CodexModelCatalogHeader = "X-Weave-Codex-Model-Catalog"

func CodexModelSwitched(header http.Header, conversion translate.ResponsesConversion) bool {
	if header.Get(requestcontext.CodexNativeModelPinHeader) != "1" {
		return false
	}
	return conversion.CodexModelSwitch ||
		(conversion.CodexModelInstructions && header.Get(CodexModelCatalogHeader) == "1")
}
