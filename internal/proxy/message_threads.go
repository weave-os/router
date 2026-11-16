package proxy

import (
	"io"
	"net/http"
)

const messageThreadUnsupportedBody = `{"type":"error","error":{"type":"invalid_request_error","message":"thread: continuing a message thread is not supported through Weave Router; resend the full conversation without thread.","details":{"error_code":"thread_unsupported_request"}}}`

func writeMessageThreadUnsupported(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, err := io.WriteString(w, messageThreadUnsupportedBody)
	return err
}
