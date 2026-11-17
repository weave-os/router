package proxy

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/sse"
	"weave-os/router/internal/telemetry"
)

const anthropicRefusalStopReason = "refusal"

const refusalScanCap = 64 * 1024

const refusalScanOverlap = 32

type refusalObserver struct {
	inner    http.ResponseWriter
	buf      []byte
	refused  bool
	category string
}

func newRefusalObserver(inner http.ResponseWriter) *refusalObserver {
	return &refusalObserver{inner: inner}
}

func (o *refusalObserver) Header() http.Header { return o.inner.Header() }

func (o *refusalObserver) WriteHeader(status int) {
	sse.PrepareJSONResponse(o.inner)
	o.inner.WriteHeader(status)
}

func (o *refusalObserver) Write(p []byte) (int, error) {
	o.observe(p)
	return sse.WriteJSONChunk(o.inner, p)
}

func (o *refusalObserver) observe(p []byte) {
	if !o.refused && len(o.buf) < refusalScanCap {
		prevLen := len(o.buf)
		room := refusalScanCap - len(o.buf)
		if room > len(p) {
			room = len(p)
		}
		o.buf = append(o.buf, p[:room]...)
		scanFrom := prevLen - refusalScanOverlap
		if scanFrom < 0 {
			scanFrom = 0
		}
		if detectRefusalSignal(o.buf[scanFrom:]) {
			o.refused = true
			o.category = refusalCategory(o.buf)
		}
	}
}

func (o *refusalObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func detectRefusalSignal(b []byte) bool {
	if bytes.Contains(b, []byte(`"stop_reason":"refusal"`)) ||
		bytes.Contains(b, []byte(`"type":"refusal"`)) ||
		bytes.Contains(b, []byte("api_refusal_category")) {
		return true
	}
	return bytes.Contains(bytes.ToLower(b), []byte("safeguards flagged"))
}

const refusalCategoryMaxLen = 64

var refusalCategoryKeys = [][]byte{
	[]byte(`"api_refusal_category":"`),
	[]byte(`"refusal_category":"`),
	[]byte(`"category":"`),
}

func refusalCategory(b []byte) string {
	for _, key := range refusalCategoryKeys {
		if v, ok := delimitedValue(b, key, '"'); ok {
			return v
		}
	}
	if v, ok := delimitedValue(b, []byte("Details: ["), ']'); ok {
		return v
	}
	return ""
}

func delimitedValue(b, prefix []byte, end byte) (string, bool) {
	i := bytes.Index(b, prefix)
	if i < 0 {
		return "", false
	}
	rest := b[i+len(prefix):]
	j := bytes.IndexByte(rest, end)
	if j <= 0 || j > refusalCategoryMaxLen {
		return "", false
	}
	return string(rest[:j]), true
}

type passiveRefusalObserver struct {
	inner http.ResponseWriter
	scan  refusalObserver
}

func newPassiveRefusalObserver(inner http.ResponseWriter) *passiveRefusalObserver {
	return &passiveRefusalObserver{inner: inner}
}

func (o *passiveRefusalObserver) Header() http.Header { return o.inner.Header() }

func (o *passiveRefusalObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *passiveRefusalObserver) Write(p []byte) (int, error) {
	o.scan.observe(p)
	return o.inner.Write(p)
}

func (o *passiveRefusalObserver) ArmOutputProgress(mark func()) bool {
	armer, ok := o.inner.(interface{ ArmOutputProgress(func()) bool })
	return ok && armer.ArmOutputProgress(mark)
}

func (o *passiveRefusalObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *passiveRefusalObserver) stopReason(extractor *telemetry.UsageExtractor) string {
	stopReason, _, _ := extractor.AnthropicResponse()
	if stopReason == "" && o.scan.refused {
		return anthropicRefusalStopReason
	}
	return stopReason
}
