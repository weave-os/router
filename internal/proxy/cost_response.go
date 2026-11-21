package proxy

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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

type routerCostCalculator func(inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int, speed catalog.Speed, mods catalog.UsageModifiers) routerResponseCost

func routerCostCalculatorFor(model, provider string, fast bool) routerCostCalculator {
	pricing, ok := servedPricing(provider, model, fast)
	if !ok {
		return nil
	}
	return func(inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int, speed catalog.Speed, mods catalog.UsageModifiers) routerResponseCost {
		served := pricing
		if speed == catalog.SpeedFast && !fast {
			served, _ = servedPricing(provider, model, true)
		}
		return routerResponseCostFromPricing(served, provider, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens, mods)
	}
}

func routerResponseCostFromPricing(pricing catalog.Pricing, provider string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int, mods catalog.UsageModifiers) routerResponseCost {
	inputUSD := catalog.EffectiveInputCost(inputTokens, cacheCreationTokens, cacheReadTokens, pricing, provider, mods)
	outputUSD := catalog.EffectiveOutputCost(catalog.PromptTokens(inputTokens, cacheCreationTokens, cacheReadTokens, provider), outputTokens, pricing, mods)
	return routerResponseCost{
		TotalUSD:            roundUSD(inputUSD + outputUSD),
		InputUSD:            roundUSD(inputUSD),
		OutputUSD:           roundUSD(outputUSD),
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreationTokens,
	}
}

func roundUSD(v float64) float64 {
	return math.Round(v*1e10) / 1e10
}

func setRouterCostHeaders(h http.Header, cost routerResponseCost) {
	h.Set(requestcontext.HeaderRouterCostUSD, strconv.FormatFloat(cost.TotalUSD, 'f', -1, 64))
	h.Set(requestcontext.HeaderRouterCostInputUSD, strconv.FormatFloat(cost.InputUSD, 'f', -1, 64))
	h.Set(requestcontext.HeaderRouterCostOutputUSD, strconv.FormatFloat(cost.OutputUSD, 'f', -1, 64))
	h.Set(requestcontext.HeaderRouterCacheReadTokens, strconv.Itoa(cost.CacheReadTokens))
	h.Set(requestcontext.HeaderRouterCacheCreationTokens, strconv.Itoa(cost.CacheCreationTokens))
}

type streamCostWriter struct {
	inner              http.ResponseWriter
	pending            bytes.Buffer
	framing            sse.Scanner
	calculate          routerCostCalculator
	inputIncludesCache bool
	facts              usage.Facts
	source             func() usage.Facts
	requestCacheTTL1h  func() bool
}

func newStreamCostWriter(inner http.ResponseWriter) *streamCostWriter {
	return &streamCostWriter{inner: inner}
}

func (w *streamCostWriter) Header() http.Header { return w.inner.Header() }

func (w *streamCostWriter) WriteHeader(status int) {
	sse.PrepareJSONResponse(w.inner)
	w.inner.WriteHeader(status)
}

func (w *streamCostWriter) Flush() {
	if flusher, ok := w.inner.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *streamCostWriter) SetCostCalculator(calculator routerCostCalculator, inputIncludesCache bool, requestCacheTTL1h func() bool) {
	w.facts = usage.Facts{}
	w.calculate = calculator
	w.inputIncludesCache = inputIncludesCache
	w.requestCacheTTL1h = requestCacheTTL1h
}

func (w *streamCostWriter) Write(p []byte) (int, error) {
	if w.pending.Len() == 0 {
		trimmed := bytes.TrimSpace(p)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			return sse.WriteJSONChunk(w.inner, p)
		}
	}
	w.pending.Write(p)
	for {
		event, consumed := w.framing.Next(w.pending.Bytes())
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
	if w.source == nil {
		switch string(eventType) {
		case "message_start":
			w.facts.Merge(usage.ParseAnthropic(gjson.GetBytes(data, "message.usage")))
		case "message_delta":
			w.facts.Merge(usage.ParseAnthropic(gjson.GetBytes(data, "usage")))
		}
	}
	if string(eventType) != "message_delta" || w.calculate == nil {
		return event
	}
	stopReason := gjson.GetBytes(data, "delta.stop_reason")
	if stopReason.String() == "" || stopReason.String() == "null" || !gjson.GetBytes(data, "usage").IsObject() {
		return event
	}
	facts := w.facts
	if w.source != nil {
		facts = w.source()
	}
	inputTokens := facts.Input.Value
	if w.source == nil && w.inputIncludesCache {
		inputTokens = facts.PromptTokens()
	}
	cacheCreation, cacheRead := facts.CacheWrite.Value, facts.CacheRead.Value
	mods := catalog.UsageModifiers{CacheCreation1h: facts.CacheWrite1h.Value, InferenceGeo: facts.InferenceGeo}
	if !facts.CacheSplitReported() && cacheCreation > 0 && w.requestCacheTTL1h != nil && w.requestCacheTTL1h() {
		mods.CacheCreation1h = cacheCreation
	}
	cost, err := json.Marshal(w.calculate(inputTokens, facts.BilledOutputTokens(), cacheCreation, cacheRead, facts.Speed, mods))
	if err != nil {
		return event
	}
	annotated, err := sjson.SetRawBytes(data, "usage.weave_cost", cost)
	if err != nil {
		return event
	}
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
