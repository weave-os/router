package proxy

import (
	"io"
	"net/http"
)

// messageThreadUnsupportedBody carries the error_code Claude Code matches to
// resend the turn with its full history and stay stateless for the session.
const messageThreadUnsupportedBody = `{"type":"error","error":{"type":"invalid_request_error","message":"thread: continuing a message thread is not supported through Weave Router; resend the full conversation without thread.","details":{"error_code":"thread_unsupported_request"}}}`

// writeMessageThreadUnsupported answers a message-thread continue, whose body
// holds only the turns after previous_message_id: the router routes, pins, and
// translates on the full transcript, so it never serves a partial one.
func writeMessageThreadUnsupported(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, err := io.WriteString(w, messageThreadUnsupportedBody)
	return err
}
