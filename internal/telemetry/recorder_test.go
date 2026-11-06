package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recorderCall struct {
	kind string
	span Span
	log  LogRecord
}

type recordingRecorder struct {
	calls []recorderCall
}

func (r *recordingRecorder) Record(span Span) {
	r.calls = append(r.calls, recorderCall{kind: "span", span: span})
}

func (r *recordingRecorder) RecordLog(record LogRecord) {
	r.calls = append(r.calls, recorderCall{kind: "log", log: record})
}

func (r *recordingRecorder) Flush() {
	r.calls = append(r.calls, recorderCall{kind: "flush"})
}

func TestRecorderContextForwardsCallsInOrder(t *testing.T) {
	start := time.Unix(10, 20)
	span := Span{Name: "request", Start: start, End: start.Add(time.Second)}
	record := LogRecord{Name: "request.done", Time: start, Severity: SeverityInfo}
	recorder := &recordingRecorder{}
	ctx := WithRecorder(context.Background(), recorder)

	Record(ctx, span)
	RecordLog(ctx, record)
	Flush(ctx)

	require.Equal(t, []recorderCall{
		{kind: "span", span: span},
		{kind: "log", log: record},
		{kind: "flush"},
	}, recorder.calls)
}

func TestRecorderContextWithoutRecorderIsNoOp(t *testing.T) {
	ctx := context.Background()
	span := Span{Name: "request"}
	record := LogRecord{Name: "request.done"}

	Record(ctx, span)
	RecordLog(ctx, record)
	Flush(ctx)

	got := WithRecorder(ctx, nil)
	if got != ctx {
		t.Fatal("WithRecorder(ctx, nil) must return the original context")
	}
	Record(got, span)
	RecordLog(got, record)
	Flush(got)
}
