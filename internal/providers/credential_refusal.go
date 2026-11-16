package providers

import (
	"errors"
	"net/http"
	"strings"
)

var credentialRefusalPhrases = []string{
	"reached your specified api usage limits",
	"out of extra usage",
	"not scoped to a workspace",
}

func IsUpstreamCredentialRefusal(err error) bool {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(string(buffered.Body))
	for _, phrase := range credentialRefusalPhrases {
		if strings.Contains(body, phrase) {
			return true
		}
	}
	return false
}
