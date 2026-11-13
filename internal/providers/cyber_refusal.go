package providers

import (
	"bytes"
	"errors"
	"net/http"
)

// CyberPolicyErrorCode is the error code OpenAI sets when its cybersecurity
// classifier declines a request ("Trusted Access for Cyber" gate). It appears
// as error.code on a rejected /v1/responses call and inside the terminal
// error / response.failed event of an already-accepted stream.
const CyberPolicyErrorCode = "cyber_policy"

// CyberPolicyRefusalMessage is the refusal sentence OpenAI returns. Matched as
// well as the code because the streamed error event carries the message only.
const CyberPolicyRefusalMessage = "This content was flagged for possible cybersecurity risk."

// cyberPolicyRefusalPhrase is the stable fragment of CyberPolicyRefusalMessage;
// the rest of the sentence carries a program link OpenAI has already reworded.
const cyberPolicyRefusalPhrase = "flagged for possible cybersecurity risk"

// ContainsCyberPolicyRefusal reports whether b — a buffered error body or a
// streamed SSE frame — carries OpenAI's cybersecurity-policy refusal. The
// broader invalid_prompt / content-filter codes are deliberately not matched:
// those decline content a different model would decline too, so retrying them
// elsewhere buys nothing.
func ContainsCyberPolicyRefusal(b []byte) bool {
	lower := bytes.ToLower(b)
	return bytes.Contains(lower, []byte(cyberPolicyRefusalPhrase)) ||
		bytes.Contains(lower, []byte(`"`+CyberPolicyErrorCode+`"`))
}

// IsUpstreamCyberPolicyRefusal reports whether err is a buffered upstream
// rejection carrying the cyber-policy refusal: the non-streaming shape, and the
// shape CyberPolicyRefusalError gives a refusal withheld from a stream.
func IsUpstreamCyberPolicyRefusal(err error) bool {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	return ContainsCyberPolicyRefusal(buffered.Body)
}

// CyberPolicyRefusalError renders a refusal observed on a 200 stream as a
// buffered upstream error, so the dispatch chain classifies it exactly like the
// non-streaming rejection: HTTP 400 keeps it out of IsRetryable, and the
// envelope is what the client sees if no rescue serves the turn.
func CyberPolicyRefusalError() *UpstreamErrorResponse {
	return &UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body: []byte(`{"error":{"message":"` + CyberPolicyRefusalMessage +
			`","type":"invalid_request_error","code":"` + CyberPolicyErrorCode + `"}}`),
	}
}
