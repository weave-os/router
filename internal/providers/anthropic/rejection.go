package anthropic

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
)

const upstreamFieldRequired = "Field required"

var credentialRefusalPhrases = []string{
	"reached your specified api usage limits",
	"out of extra usage",
	"not scoped to a workspace",
}

var rejectedFieldMessage = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*): (.+)$`)

func classifyRejection(status int, errBody, prepared, sent []byte) providers.UpstreamRejection {
	if status != http.StatusBadRequest {
		return providers.UpstreamRejection{}
	}
	message := gjson.GetBytes(errBody, "error.message").String()
	lower := strings.ToLower(message)
	for _, phrase := range credentialRefusalPhrases {
		if strings.Contains(lower, phrase) {
			return providers.UpstreamRejection{Kind: providers.UpstreamRejectionCredential}
		}
	}
	match := rejectedFieldMessage.FindStringSubmatch(message)
	if match == nil {
		return providers.UpstreamRejection{}
	}
	path, reason := match[1], match[2]
	preparedValue, sentValue := gjson.GetBytes(prepared, path), gjson.GetBytes(sent, path)
	absentFromBoth := !preparedValue.Exists() && !sentValue.Exists()
	sentAsPrepared := preparedValue.Exists() && sentValue.Exists() && preparedValue.Raw == sentValue.Raw
	if (absentFromBoth && reason == upstreamFieldRequired) || sentAsPrepared {
		return providers.UpstreamRejection{Kind: providers.UpstreamRejectionField, Field: path}
	}
	return providers.UpstreamRejection{}
}
