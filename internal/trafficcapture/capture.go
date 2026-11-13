// Package trafficcapture defines the request-scoped contract for local HTTP capture.
package trafficcapture

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// Direction identifies which side of the router handled an HTTP exchange.
type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionUpstream Direction = "upstream"
)

// Request contains the HTTP request as observed at a router boundary.
type Request struct {
	Method           string              `json:"method"`
	URL              string              `json:"url"`
	Host             string              `json:"host,omitempty"`
	Proto            string              `json:"proto"`
	Header           map[string][]string `json:"headers"`
	ContentLength    int64               `json:"content_length"`
	TransferEncoding []string            `json:"transfer_encoding,omitempty"`
	Body             []byte              `json:"body"`
	BodySpool        *BodySpool          `json:"-"`
	BodyComplete     bool                `json:"body_complete"`
}

// Response contains the HTTP response observed at a router boundary.
type Response struct {
	Proto            string              `json:"proto"`
	StatusCode       int                 `json:"status_code"`
	Header           map[string][]string `json:"headers"`
	ContentLength    int64               `json:"content_length"`
	TransferEncoding []string            `json:"transfer_encoding,omitempty"`
	Body             []byte              `json:"body"`
	BodySpool        *BodySpool          `json:"-"`
	BodyComplete     bool                `json:"body_complete"`
}

// Exchange is one complete or interrupted HTTP exchange. Byte slices are
// encoded as base64 by encoding/json so arbitrary streaming payloads survive.
type Exchange struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	ParentID      string    `json:"parent_id,omitempty"`
	Attempt       uint64    `json:"attempt,omitempty"`
	Direction     Direction `json:"direction"`
	StartedAt     time.Time `json:"started_at"`
	DurationMS    int64     `json:"duration_ms"`
	Request       Request   `json:"request"`
	Response      *Response `json:"response,omitempty"`
	Complete      bool      `json:"complete"`
	Error         string    `json:"error,omitempty"`
}

// Recorder persists an exchange. Implementations must be safe for concurrent calls
// and consume any BodySpool values before Record returns.
type Recorder interface {
	Record(Exchange) error
}

type scopeKey struct{}

// Scope associates provider attempts with one inbound exchange.
type Scope struct {
	recorder Recorder
	id       string
	attempt  atomic.Uint64
}

var exchangeSequence atomic.Uint64

// NewID returns a process-unique, time-sortable identifier without changing
// the request's HTTP headers or adding a third-party dependency.
func NewID() string {
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), exchangeSequence.Add(1))
}

// WithRecorder associates recorder and exchangeID with ctx for this request.
func WithRecorder(ctx context.Context, recorder Recorder, exchangeID string) context.Context {
	return context.WithValue(ctx, scopeKey{}, &Scope{recorder: recorder, id: exchangeID})
}

// FromContext returns the capture scope attached to ctx, if capture is enabled.
func FromContext(ctx context.Context) *Scope {
	scope, _ := ctx.Value(scopeKey{}).(*Scope)
	return scope
}

// Recorder returns the configured recorder.
func (s *Scope) Recorder() Recorder { return s.recorder }

// ExchangeID returns the inbound exchange identifier.
func (s *Scope) ExchangeID() string { return s.id }

// NextAttempt allocates an ID and sequence number for the next upstream call.
func (s *Scope) NextAttempt() (string, uint64) {
	attempt := s.attempt.Add(1)
	return fmt.Sprintf("%s-%d", s.id, attempt), attempt
}
