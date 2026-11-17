package providers

import (
	"errors"
	"net/http"
	"strings"
)

type UpstreamRejectionKind string

const (
	UpstreamRejectionCredential UpstreamRejectionKind = "credential"
	UpstreamRejectionField      UpstreamRejectionKind = "field"
)

type UpstreamRejection struct {
	Kind  UpstreamRejectionKind
	Field string
}

func badRequestRejection(err error) (*UpstreamErrorResponse, bool) {
	var upstreamErr *UpstreamErrorResponse
	if !errors.As(err, &upstreamErr) || upstreamErr.Status != http.StatusBadRequest {
		return nil, false
	}
	return upstreamErr, true
}

func IsUpstreamCredentialRefusal(err error) bool {
	upstreamErr, ok := badRequestRejection(err)
	return ok && upstreamErr.Rejection.Kind == UpstreamRejectionCredential
}

func UpstreamRejectedField(err error) string {
	upstreamErr, ok := badRequestRejection(err)
	if !ok || upstreamErr.Rejection.Kind != UpstreamRejectionField {
		return ""
	}
	return upstreamErr.Rejection.Field
}

var capabilityRejectionPhrases = []string{
	"not a multimodal model",
	"does not support image",
	"image input is not supported",
	"image input is unsupported",
	"does not support vision",
	"multimodal input is not supported",
}

func IsUpstreamCapabilityRejection(err error) bool {
	upstreamErr, ok := badRequestRejection(err)
	if !ok {
		return false
	}
	body := strings.ToLower(string(upstreamErr.Body))
	for _, phrase := range capabilityRejectionPhrases {
		if strings.Contains(body, phrase) {
			return true
		}
	}
	return false
}
