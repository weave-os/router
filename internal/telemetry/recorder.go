package telemetry

import "context"

// Recorder collects one request's spans and log records until Flush.
type Recorder interface {
	Record(Span)
	RecordLog(LogRecord)
	Flush()
}

type recorderKey struct{}

func WithRecorder(ctx context.Context, r Recorder) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderKey{}, r)
}

func Record(ctx context.Context, s Span) {
	if r, ok := ctx.Value(recorderKey{}).(Recorder); ok {
		r.Record(s)
	}
}

func RecordLog(ctx context.Context, r LogRecord) {
	if recorder, ok := ctx.Value(recorderKey{}).(Recorder); ok {
		recorder.RecordLog(r)
	}
}

func Flush(ctx context.Context) {
	if r, ok := ctx.Value(recorderKey{}).(Recorder); ok {
		r.Flush()
	}
}
