package translate

import "weave-os/router/internal/usage"

type UsageSink interface {
	usage.Sink
	RecordOutputLimitReached()
}

func recordOutputLimit(sink UsageSink, reached bool) {
	if sink != nil && reached {
		sink.RecordOutputLimitReached()
	}
}
