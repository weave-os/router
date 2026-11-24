package respobserve

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/sse"
	"weave-os/router/internal/telemetry"
)

const AnthropicRefusalStopReason = "refusal"

const refusalScanCap = 64 * 1024

const refusalScanOverlap = 32

type RefusalObserver struct {
	inner    http.ResponseWriter
	buf      []byte
	Refused  bool
	Category string
}

func NewRefusalObserver(inner http.ResponseWriter) *RefusalObserver {
	return &RefusalObserver{inner: inner}
}

func (o *RefusalObserver) Header() http.Header { return o.inner.Header() }

func (o *RefusalObserver) WriteHeader(status int) {
	sse.PrepareJSONResponse(o.inner)
	o.inner.WriteHeader(status)
}

func (o *RefusalObserver) Write(p []byte) (int, error) {
	o.observe(p)
	return sse.WriteJSONChunk(o.inner, p)
}

func (o *RefusalObserver) observe(p []byte) {
	if !o.Refused && len(o.buf) < refusalScanCap {
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
		if DetectRefusalSignal(o.buf[scanFrom:]) {
			o.Refused = true
			o.Category = RefusalCategory(o.buf)
		}
	}
}

func (o *RefusalObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func DetectRefusalSignal(b []byte) bool {
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

func RefusalCategory(b []byte) string {
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

type PassiveRefusalObserver struct {
	inner http.ResponseWriter
	Scan  RefusalObserver
}

func NewPassiveRefusalObserver(inner http.ResponseWriter) *PassiveRefusalObserver {
	return &PassiveRefusalObserver{inner: inner}
}

func (o *PassiveRefusalObserver) Header() http.Header { return o.inner.Header() }

func (o *PassiveRefusalObserver) WriteHeader(status int) { o.inner.WriteHeader(status) }

func (o *PassiveRefusalObserver) Write(p []byte) (int, error) {
	o.Scan.observe(p)
	return o.inner.Write(p)
}

func (o *PassiveRefusalObserver) ArmOutputProgress(mark func()) bool {
	armer, ok := o.inner.(interface{ ArmOutputProgress(func()) bool })
	return ok && armer.ArmOutputProgress(mark)
}

func (o *PassiveRefusalObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (o *PassiveRefusalObserver) StopReason(extractor *telemetry.UsageExtractor) string {
	stopReason, _, _ := extractor.AnthropicResponse()
	if stopReason == "" && o.Scan.Refused {
		return AnthropicRefusalStopReason
	}
	return stopReason
}
