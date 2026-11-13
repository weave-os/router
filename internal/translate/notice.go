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

const (
	NoticeModelSelection NoticeKind = "model_selection"
	NoticeFundingWarning NoticeKind = "funding_warning"
	NoticeSlowStart      NoticeKind = "slow_start"
	// NoticeSubscriptionReconnect points the user at the dashboard to connect
	// or reconnect a subscription seat.
	NoticeSubscriptionReconnect NoticeKind = "subscription_reconnect"
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
	ResetNotices()
	CommitNotice() error
	ArmOutputProgress(func()) bool
	Seal()
	Committed() bool
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
	beforeContent          func() (RouterNotice, bool)
	deferred               RouterNotice
}

// DeferIfEager holds notice until this attempt commits or starts provider
// content, so an attempt that fails first never shows it. A later call replaces
// it. Non-eager streams buffer notices already, so it reports false there.
func (d *NoticeDelivery) DeferIfEager(notice RouterNotice) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.capabilities.Eager {
		return false
	}
	d.deferred = notice
	return true
}

// SetBeforeProviderContent registers a notice decided when the attempt's first
// provider content arrives. It runs just before that content is written, so it
// can never precede the provider commit.
func (d *NoticeDelivery) SetBeforeProviderContent(fn func() (RouterNotice, bool)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.beforeContent = fn
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
		providerContent, err := d.beforeEvent(event)
		if err != nil {
			return 0, err
		}
		if _, d.failure = d.sink.Write(d.pending[:consumed]); d.failure != nil {
			return 0, d.failure
		}
		if err := d.afterEvent(event, consumed, providerContent); err != nil {
			return 0, err
		}
	}
	return consumedBytes, nil
}

// beforeEvent emits notices due before a pending SSE record. Callers hold d.mu.
func (d *NoticeDelivery) beforeEvent(event []byte) (bool, error) {
	providerContent := noticeProviderContent(d.capabilities.Format, event)
	if providerContent && !d.providerContentStarted && !d.terminal {
		d.failure = d.emitBeforeContent()
	}
	return providerContent, d.failure
}

// afterEvent records a written SSE record and emits notices due after it.
// Callers hold d.mu.
func (d *NoticeDelivery) afterEvent(event []byte, consumed int, providerContent bool) error {
	if providerContent {
		d.providerContentStarted = true
	}
	if noticeTerminal(d.capabilities.Format, event) {
		d.terminal = true
	}
	if d.failure = d.emitDeferredOnCommit(); d.failure != nil {
		return d.failure
	}
	d.pending = d.pending[consumed:]
	for _, notice := range d.queued {
		if d.terminal || d.providerContentStarted {
			break
		}
		if d.failure = d.emit(notice); d.failure != nil {
			return d.failure
		}
	}
	d.queued = nil
	return nil
}

// emitDeferredOnCommit shows a deferred marker once its attempt has committed,
// even when that attempt then fails before any provider content.
func (d *NoticeDelivery) emitDeferredOnCommit() error {
	if d.deferred.Text == "" || d.terminal || !d.sink.Committed() {
		return nil
	}
	deferred := d.deferred
	d.deferred = RouterNotice{}
	return d.emitOnce(deferred)
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
	d.deferred = RouterNotice{}
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

func (d *NoticeDelivery) emitBeforeContent() error {
	deferred := d.deferred
	d.deferred = RouterNotice{}
	fn := d.beforeContent
	d.beforeContent = nil
	if !d.capabilities.SupportsNotices() {
		return nil
	}
	if err := d.emitOnce(deferred); err != nil || fn == nil {
		return err
	}
	notice, ok := fn()
	if !ok {
		return nil
	}
	return d.emitOnce(notice)
}

func (d *NoticeDelivery) emitOnce(notice RouterNotice) error {
	if notice.ID == "" || notice.Text == "" {
		return nil
	}
	if _, exists := d.seen[notice.ID]; exists {
		return nil
	}
	d.seen[notice.ID] = struct{}{}
	return d.emit(notice)
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
