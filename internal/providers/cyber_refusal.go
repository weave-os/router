package providers

import (
	"bytes"
	"errors"
	"net/http"
)

const CyberPolicyErrorCode = "cyber_policy"

const CyberPolicyRefusalMessage = "This content was flagged for possible cybersecurity risk."

const cyberPolicyRefusalPhrase = "flagged for possible cybersecurity risk"

func ContainsCyberPolicyRefusal(b []byte) bool {
	lower := bytes.ToLower(b)
	return bytes.Contains(lower, []byte(cyberPolicyRefusalPhrase)) ||
		bytes.Contains(lower, []byte(`"`+CyberPolicyErrorCode+`"`))
}

func IsUpstreamCyberPolicyRefusal(err error) bool {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	return ContainsCyberPolicyRefusal(buffered.Body)
}

func CyberPolicyRefusalError() *UpstreamErrorResponse {
	return &UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body: []byte(`{"error":{"message":"` + CyberPolicyRefusalMessage +
			`","type":"invalid_request_error","code":"` + CyberPolicyErrorCode + `"}}`),
	}
}
