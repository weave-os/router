package trafficcapture

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionUpstream Direction = "upstream"
)

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

type Recorder interface {
	Record(Exchange) error
}

type scopeKey struct{}

type Scope struct {
	recorder Recorder
	id       string
	attempt  atomic.Uint64
}

var exchangeSequence atomic.Uint64

func NewID() string {
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), exchangeSequence.Add(1))
}

func WithRecorder(ctx context.Context, recorder Recorder, exchangeID string) context.Context {
	return context.WithValue(ctx, scopeKey{}, &Scope{recorder: recorder, id: exchangeID})
}

func FromContext(ctx context.Context) *Scope {
	scope, _ := ctx.Value(scopeKey{}).(*Scope)
	return scope
}

func (s *Scope) Recorder() Recorder { return s.recorder }

func (s *Scope) ExchangeID() string { return s.id }

func (s *Scope) NextAttempt() (string, uint64) {
	attempt := s.attempt.Add(1)
	return fmt.Sprintf("%s-%d", s.id, attempt), attempt
}
