package proxy

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"workweave/router/internal/router/catalog"
	"workweave/router/internal/sse"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// responseCostBuffer holds non-streaming response bytes until usage has been
// extracted and cost headers can be written before the body reaches the client.
type responseCostBuffer struct {
	inner       http.ResponseWriter
	body        bytes.Buffer
	status      int
	writeHeader bool
	wrote       bool
	flushed     bool
}

func newResponseCostBuffer(inner http.ResponseWriter) *responseCostBuffer {
	return &responseCostBuffer{inner: inner, status: http.StatusOK}
}

func (b *responseCostBuffer) Header() http.Header { return b.inner.Header() }

func (b *responseCostBuffer) WriteHeader(status int) {
	if b.writeHeader {
		return
	}
	b.wrote = true
	b.status = status
	b.writeHeader = true
}

func (b *responseCostBuffer) Write(p []byte) (int, error) {
	if !b.writeHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

// FlushToClient commits the buffered status and body after cost headers have
// been populated. It is safe to call more than once.
func (b *responseCostBuffer) FlushToClient() error {
	if b.flushed || !b.wrote {
		return nil
	}
	b.flushed = true
	b.inner.WriteHeader(b.status)
	if _, err := b.inner.Write(b.body.Bytes()); err != nil {
		return err
	}
	if flusher, ok := b.inner.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

type routerResponseCost struct {
	TotalUSD            float64 `json:"usd"`
	InputUSD            float64 `json:"input_usd"`
	OutputUSD           float64 `json:"output_usd"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
}

type routerCostCalculator func(inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int) routerResponseCost

func routerCostCalculatorFor(model, provider string, fast bool) routerCostCalculator {
	pricing, ok := servedPricing(provider, model, fast)
	if !ok {
		return nil
	}
	return func(inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int) routerResponseCost {
		return routerResponseCostFromPricing(pricing, provider, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)
	}
}

func routerResponseCostFromPricing(pricing catalog.Pricing, provider string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int) routerResponseCost {
	inputUSD := catalog.EffectiveInputCost(inputTokens, cacheCreationTokens, cacheReadTokens, pricing.InputUSDPer1M, pricing, provider)
	outputUSD := catalog.EffectiveOutputCost(outputTokens, pricing.OutputUSDPer1M)
	return routerResponseCost{
		TotalUSD:            roundUSD(inputUSD + outputUSD),
		InputUSD:            roundUSD(inputUSD),
		OutputUSD:           roundUSD(outputUSD),
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreationTokens,
	}
}

// roundUSD trims binary float noise so "0.0000495" is not rendered as
// "0.000049500000000000004"; ten decimals keeps sub-micro-dollar precision.
func roundUSD(v float64) float64 {
	return math.Round(v*1e10) / 1e10
}

func setRouterCostHeaders(h http.Header, cost routerResponseCost) {
	h.Set(HeaderRouterCostUSD, strconv.FormatFloat(cost.TotalUSD, 'f', -1, 64))
	h.Set(HeaderRouterCostInputUSD, strconv.FormatFloat(cost.InputUSD, 'f', -1, 64))
	h.Set(HeaderRouterCostOutputUSD, strconv.FormatFloat(cost.OutputUSD, 'f', -1, 64))
	h.Set(HeaderRouterCacheReadTokens, strconv.Itoa(cost.CacheReadTokens))
	h.Set(HeaderRouterCacheCreationTokens, strconv.Itoa(cost.CacheCreationTokens))
}

// streamCostWriter adds the final turn's authoritative cost to the Anthropic
// message_delta usage object while preserving all other SSE events verbatim.
type streamCostWriter struct {
	inner     http.ResponseWriter
	pending   bytes.Buffer
	calculate routerCostCalculator
	// Translated Anthropic usage reports fresh input, while the bound pricing
	// calculator expects OpenAI/Gemini input to include cached tokens.
	inputIncludesCache   bool
	messageStartInput    int
	messageStartRead     int
	messageStartCreation int
}

func newStreamCostWriter(inner http.ResponseWriter) *streamCostWriter {
	return &streamCostWriter{inner: inner}
}

func (w *streamCostWriter) Header() http.Header { return w.inner.Header() }

func (w *streamCostWriter) WriteHeader(status int) { w.inner.WriteHeader(status) }

func (w *streamCostWriter) Flush() {
	if flusher, ok := w.inner.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *streamCostWriter) SetCostCalculator(calculator routerCostCalculator, inputIncludesCache bool) {
	w.calculate = calculator
	w.inputIncludesCache = inputIncludesCache
}

func (w *streamCostWriter) Write(p []byte) (int, error) {
	// Error fallback rendering writes a plain JSON envelope rather than an SSE
	// event. Pass it through immediately instead of retaining it indefinitely
	// while waiting for an SSE boundary that will never arrive.
	if w.pending.Len() == 0 {
		trimmed := bytes.TrimSpace(p)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			return w.inner.Write(p)
		}
	}
	w.pending.Write(p)
	for {
		event, consumed := sse.SplitNext(w.pending.Bytes())
		if consumed == 0 {
			break
		}
		event = w.annotateEvent(event)
		if _, err := w.inner.Write(append(event, '\n', '\n')); err != nil {
			return 0, err
		}
		w.pending.Next(consumed)
	}
	return len(p), nil
}

func (w *streamCostWriter) annotateEvent(event []byte) []byte {
	eventType, data := sse.ParseEvent(event)
	if string(eventType) == "message_start" {
		usage := gjson.GetBytes(data, "message.usage")
		w.messageStartInput = int(usage.Get("input_tokens").Int())
		w.messageStartRead = int(usage.Get("cache_read_input_tokens").Int())
		w.messageStartCreation = int(usage.Get("cache_creation_input_tokens").Int())
		return event
	}
	if string(eventType) != "message_delta" || w.calculate == nil {
		return event
	}
	stopReason := gjson.GetBytes(data, "delta.stop_reason")
	if !stopReason.Exists() || stopReason.String() == "" || stopReason.String() == "null" {
		return event
	}
	usage := gjson.GetBytes(data, "usage")
	if !usage.Exists() {
		return event
	}
	inputTokens := int(usage.Get("input_tokens").Int())
	outputTokens := int(usage.Get("output_tokens").Int())
	cacheCreation := int(usage.Get("cache_creation_input_tokens").Int())
	cacheRead := int(usage.Get("cache_read_input_tokens").Int())
	if inputTokens == 0 {
		inputTokens = w.messageStartInput
	}
	if cacheCreation == 0 {
		cacheCreation = w.messageStartCreation
	}
	if cacheRead == 0 {
		cacheRead = w.messageStartRead
	}
	if w.inputIncludesCache {
		inputTokens += cacheCreation + cacheRead
	}
	cost, err := json.Marshal(w.calculate(inputTokens, outputTokens, cacheCreation, cacheRead))
	if err != nil {
		return event
	}
	annotated, err := sjson.SetRawBytes(data, "usage.weave_cost", cost)
	if err != nil {
		return event
	}
	// Replace only the data payload, preserving event fields and line endings
	// supplied by the upstream. This keeps the annotation transparent to SSE
	// clients that rely on optional fields such as id/retry.
	dataField := bytes.Index(event, []byte("data:"))
	if dataField < 0 {
		return event
	}
	payloadStart := dataField + len("data:")
	for payloadStart < len(event) && (event[payloadStart] == ' ' || event[payloadStart] == '\t') {
		payloadStart++
	}
	payloadEnd := len(event)
	if lineEnd := bytes.IndexByte(event[payloadStart:], '\n'); lineEnd >= 0 {
		payloadEnd = payloadStart + lineEnd
	}
	result := make([]byte, 0, len(event)-payloadEnd+payloadStart+len(annotated))
	result = append(result, event[:payloadStart]...)
	result = append(result, annotated...)
	result = append(result, event[payloadEnd:]...)
	return result
}
