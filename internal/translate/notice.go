package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/sse"
)

type NoticeKind string

const (
	NoticeModelSelection NoticeKind = "model_selection"
	NoticeFundingWarning NoticeKind = "funding_warning"
)

type NoticeProvenance string

const NoticeFromRouter NoticeProvenance = "weave_router"

type NoticePresentation string

const (
	NoticeText   NoticePresentation = "text"
	NoticeHidden NoticePresentation = "hidden"
)

type NoticeOutcome string

const (
	NoticeDelivered  NoticeOutcome = "delivered"
	NoticeQueued     NoticeOutcome = "queued"
	NoticeSuppressed NoticeOutcome = "suppressed"
	NoticeDuplicate  NoticeOutcome = "duplicate"
)

// RouterNotice describes router-owned content, independently of its client encoding.
type RouterNotice struct {
	ID         string
	Provenance NoticeProvenance
	Kind       NoticeKind
	Text       string
	AttemptID  string
	Model      string
}

func NewRouterNotice(kind NoticeKind, text, model, attemptID string) RouterNotice {
	return RouterNotice{ID: uuid.NewString(), Provenance: NoticeFromRouter, Kind: kind, Text: text, Model: model, AttemptID: attemptID}
}

type NoticeCapabilities struct {
	Format       Format
	Presentation NoticePresentation
	Streaming    bool
	Eager        bool
}

func (c NoticeCapabilities) SupportsNotices() bool {
	if c.Presentation != NoticeText || (!c.Streaming && c.Format != FormatOpenAIResponses) {
		return false
	}
	switch c.Format {
	case FormatAnthropic, FormatOpenAI, FormatOpenAIResponses, FormatGemini:
		return true
	default:
		return false
	}
}

// NoticeSink is the client-protocol integration contract. Implementations own
// wire lifecycle and retry continuation, including independent prelude commitment.
type NoticeSink interface {
	http.ResponseWriter
	http.Flusher
	EmitNotice(RouterNotice) error
	EmitFailure(TerminalFailure, bool) error
	ResetNotices()
	ResetAttempt()
	CommitNotice() error
	ArmOutputProgress(func()) bool
	Seal()
}

var ErrInvalidNotice = errors.New("router notice requires identity, provenance, kind and text")

// NoticeDelivery serializes notices and complete client SSE records. Its output
// must sit below conversion and above the provider-commit buffer: notice writes
// can then be committed independently of provider output.
type NoticeDelivery struct {
	mu                     sync.Mutex
	ctx                    context.Context
	sink                   NoticeSink
	capabilities           NoticeCapabilities
	pending                []byte
	scanner                sse.Scanner
	queued                 []RouterNotice
	seen                   map[string]struct{}
	terminal               bool
	terminalFailure        bool
	chatDone               bool
	observation            *ProviderStreamObservation
	providerContentStarted bool
	failure                error
}

func NewNoticeDelivery(ctx context.Context, sink NoticeSink, capabilities NoticeCapabilities) *NoticeDelivery {
	return &NoticeDelivery{ctx: ctx, sink: sink, capabilities: capabilities, seen: make(map[string]struct{}), observation: NewProviderStreamObservation(capabilities.Format)}
}

func (d *NoticeDelivery) Header() http.Header { return d.sink.Header() }
func (d *NoticeDelivery) WriteHeader(status int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sink.WriteHeader(status)
}
func (d *NoticeDelivery) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pending) == 0 {
		d.sink.Flush()
	}
}
func (d *NoticeDelivery) Deliver(notice RouterNotice) (NoticeOutcome, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.err(); err != nil {
		return NoticeSuppressed, err
	}
	if notice.ID == "" || notice.Provenance != NoticeFromRouter || notice.Kind == "" || notice.Text == "" {
		return NoticeSuppressed, ErrInvalidNotice
	}
	if !d.capabilities.SupportsNotices() || d.terminal || d.providerContentStarted {
		return NoticeSuppressed, nil
	}
	if _, exists := d.seen[notice.ID]; exists {
		return NoticeDuplicate, nil
	}
	d.seen[notice.ID] = struct{}{}
	if len(d.pending) > 0 {
		d.queued = append(d.queued, notice)
		return NoticeQueued, nil
	}
	d.failure = d.emit(notice)
	return NoticeDelivered, d.failure
}
func (d *NoticeDelivery) err() error {
	if d.failure != nil {
		return d.failure
	}
	return d.ctx.Err()
}
func (d *NoticeDelivery) Write(payload []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.err(); err != nil {
		return 0, err
	}
	// Preserve JSON text while preventing reflected HTML on passthrough paths.
	consumedBytes := len(payload)
	var escaped bytes.Buffer
	json.HTMLEscape(&escaped, payload)
	if !d.capabilities.Streaming || d.sink.Header().Get("Content-Type") == "application/json" {
		d.sink.Header().Set("Content-Type", "application/json")
		_, d.failure = d.sink.Write(escaped.Bytes())
		return consumedBytes, d.failure
	}
	d.pending = append(d.pending, escaped.Bytes()...)
	for {
		event, consumed := d.scanner.Next(d.pending)
		if consumed == 0 {
			break
		}
		if err := d.writeRecord(event, d.pending[:consumed]); err != nil {
			return 0, err
		}
		d.pending = d.pending[consumed:]
		for _, notice := range d.queued {
			if d.terminal || d.providerContentStarted {
				break
			}
			d.failure = d.emit(notice)
			if d.failure != nil {
				return 0, d.failure
			}
		}
		d.queued = nil
	}
	return consumedBytes, nil
}

// ResetAttempt drops an abandoned partial record, retaining client-visible
// lifecycle and notice identities across pre-provider-commit retries.
func (d *NoticeDelivery) ResetAttempt(retainNotices bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !retainNotices {
		d.sink.ResetNotices()
		d.seen = make(map[string]struct{})
		d.terminal = false
		d.terminalFailure = false
		d.chatDone = false
		d.providerContentStarted = false
	}
	d.sink.ResetAttempt()
	d.failure = nil
	d.pending = nil
	d.scanner.Reset()
	d.observation = NewProviderStreamObservation(d.capabilities.Format)
	for _, notice := range d.queued {
		delete(d.seen, notice.ID)
	}
	d.queued = nil
}

func (d *NoticeDelivery) ArmOutputProgress(mark func()) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sink.ArmOutputProgress(mark)
}

func (d *NoticeDelivery) Seal() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sink.Seal()
}

func (d *NoticeDelivery) emit(notice RouterNotice) error {
	if err := d.sink.EmitNotice(notice); err != nil {
		return err
	}
	if d.capabilities.Eager {
		return d.sink.CommitNotice()
	}
	return nil
}

// Finalize drains a complete final record even when upstream omitted its blank
// delimiter. Invalid tails cannot supply a terminal or corrupt a failure frame.
func (d *NoticeDelivery) Finalize() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.finalize()
}

func (d *NoticeDelivery) finalize() error {
	if err := d.err(); err != nil {
		return err
	}
	pending := d.pending
	d.pending = nil
	d.scanner.Reset()
	if len(pending) > 0 {
		_, payload := sse.ParseEvent(pending)
		if json.Valid(payload) || bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
			record := append(bytes.TrimRight(pending, "\r\n"), '\n', '\n')
			if err := d.writeRecord(record, record); err != nil {
				return err
			}
		} else {
			d.observation.ObserveEvent(pending)
		}
	}
	if (d.capabilities.Format == FormatOpenAI || d.capabilities.Format == FormatGemini) && d.observation.Facts().State == StreamTerminal && d.observation.EOF() == nil {
		d.terminal = true
	}
	d.queued = nil
	return nil
}

// DeliverFailure accepts a render-ready failure after the request owner has
// exhausted retries and resolved HTTP versus SSE delivery.
func (d *NoticeDelivery) DeliverFailure(failure TerminalFailure, streaming bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// A protocol validator can reject a provider record after a visible notice.
	// Abandon that record while allowing the owner to deliver its final failure.
	if d.failure != nil {
		d.failure = nil
		d.pending = nil
		d.scanner.Reset()
	}
	if err := d.finalize(); err != nil {
		return err
	}
	if d.terminal {
		return nil
	}
	d.terminal = true
	d.terminalFailure = true
	d.failure = d.sink.EmitFailure(failure, streaming)
	return d.failure
}

func (d *NoticeDelivery) writeRecord(event, record []byte) error {
	if d.terminal && (d.terminalFailure || !chatTerminalTrailer(d.capabilities.Format, event)) {
		return nil
	}
	_, payload := sse.ParseEvent(event)
	chatDone := d.capabilities.Format == FormatOpenAI && bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]"))
	if chatDone && d.chatDone {
		return nil
	}
	_, d.failure = d.sink.Write(record)
	if d.failure != nil {
		return d.failure
	}
	if noticeProviderContent(d.capabilities.Format, event) {
		d.providerContentStarted = true
	}
	d.chatDone = d.chatDone || chatDone
	d.observation.ObserveEvent(event)
	d.terminalFailure = d.terminalFailure || d.observation.Facts().State == StreamFailed
	if d.terminalFailure || noticeTerminal(d.capabilities.Format, event) {
		d.terminal = true
	}
	return nil
}

func chatTerminalTrailer(format Format, event []byte) bool {
	if format != FormatOpenAI {
		return false
	}
	_, payload := sse.ParseEvent(event)
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return true
	}
	return gjson.ValidBytes(payload) && gjson.GetBytes(payload, "choices").IsArray() && len(gjson.GetBytes(payload, "choices").Array()) == 0 && gjson.GetBytes(payload, "usage").Exists()
}
