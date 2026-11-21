package ingress

import (
	"io"
	"net/http"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/translate"
)

const messageThreadUnsupportedBody = `{"type":"error","error":{"type":"invalid_request_error","message":"thread: continuing a message thread is not supported through Weave Router; resend the full conversation without thread.","details":{"error_code":"thread_unsupported_request"}}}`

func (Anthropic) StripMessageThread(body []byte, h http.Header) (stripped []byte, continued bool, err error) {
	switch gjson.GetBytes(body, "thread.type").String() {
	case translate.MessageThreadContinue:
		return body, true, nil
	case translate.MessageThreadCreate:
		if body, err = sjson.DeleteBytes(body, "thread"); err != nil {
			return body, false, err
		}
		translate.StripMessageThreadsBeta(h)
	}
	return body, false, nil
}

func WriteMessageThreadUnsupported(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, err := io.WriteString(w, messageThreadUnsupportedBody)
	return err
}
