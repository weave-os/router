package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"weave-os/router/internal/sse"
)

type NoticeKind string

const NoticeModelSelection NoticeKind = "model_selection"

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
	ResetNotices()
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
	providerContentStarted bool
	failure                error
}

func NewNoticeDelivery(ctx context.Context, sink NoticeSink, capabilities NoticeCapabilities) *NoticeDelivery {
	return &NoticeDelivery{ctx: ctx, sink: sink, capabilities: capabilities, seen: make(map[string]struct{})}
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
	if !d.capabilities.Streaming {
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
		_, d.failure = d.sink.Write(d.pending[:consumed])
		if d.failure != nil {
			return 0, d.failure
		}
		if noticeProviderContent(d.capabilities.Format, event) {
			d.providerContentStarted = true
		}
		if noticeTerminal(d.capabilities.Format, event) {
			d.terminal = true
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
		d.providerContentStarted = false
	}
	d.pending = nil
	d.scanner.Reset()
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
