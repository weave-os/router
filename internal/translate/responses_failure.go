package translate

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
)

const responsesContextOverflowCode = "context_length_exceeded"

func responsesErrorEventFailure(data []byte) (errType, msg string) {
	nested := gjson.GetBytes(data, "error")
	errType = nested.Get("code").String()
	if errType == "" {
		errType = nested.Get("type").String()
	}
	msg = nested.Get("message").String()
	if errType == "" {
		errType = gjson.GetBytes(data, "code").String()
	}
	if msg == "" {
		msg = gjson.GetBytes(data, "message").String()
	}
	return errType, msg
}

func responsesSSEFailure(b []byte) (errType, msg string, found bool) {
	rest := b
	for {
		event, n := splitBufferedResponsesEvent(rest)
		if n == 0 {
			return "", "", false
		}
		rest = rest[n:]
		_, data := sse.ParseEvent(event)
		if len(data) == 0 || !gjson.ValidBytes(data) {
			continue
		}
		switch gjson.GetBytes(data, "type").String() {
		case "error":
			errType, msg = responsesErrorEventFailure(data)
			return errType, msg, true
		case "response.failed":
			errType, msg = responsesFailureFromResponse(gjson.GetBytes(data, "response"))
			return errType, msg, true
		case "response.incomplete":
			resp := gjson.GetBytes(data, "response")
			if responsesTerminalIsFailure(resp) {
				errType, msg = responsesFailureFromResponse(resp)
				return errType, msg, true
			}
		}
	}
}

func responsesFailureStatus(errType string) int {
	if errType == responsesContextOverflowCode {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func bufferedContextOverflow(b []byte, body func(errType, msg string) []byte) error {
	errType, msg, found := responsesSSEFailure(b)
	if !found || responsesFailureStatus(errType) != http.StatusBadRequest {
		return nil
	}
	return &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: body(errType, msg)}
}

func splitBufferedResponsesEvent(buf []byte) (event []byte, n int) {
	if event, n = sse.SplitNext(buf); n != 0 {
		return event, n
	}
	if len(bytes.TrimSpace(buf)) == 0 {
		return nil, 0
	}
	return buf, len(buf)
}
