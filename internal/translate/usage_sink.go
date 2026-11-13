package translate

import "weave-os/router/internal/usage"

// UsageSink records provider facts separately from explicit output-limit evidence.
type UsageSink interface {
	usage.Sink
	RecordOutputLimitReached()
}

func recordOutputLimit(sink UsageSink, reached bool) {
	if sink != nil && reached {
		sink.RecordOutputLimitReached()
	}
}
