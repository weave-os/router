package routingpolicy

import (
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

type SwitchCacheView struct {
	Known               bool
	MessagesSinceServed int
	SinceServed         time.Duration
	TTL                 time.Duration
}

func FreshCacheView(input SwitchInput) SwitchCacheView {
	state, ok := input.ModelCaches[input.Fresh]
	if !ok {
		return SwitchCacheView{}
	}
	view := SwitchCacheView{Known: true, SinceServed: switchNow(input).Sub(state.ServedAt), TTL: time.Duration(state.WriteTTLSeconds) * time.Second}
	if state.MessageCount > 0 && input.MessageCount > 0 {
		view.MessagesSinceServed = input.MessageCount - state.MessageCount
	}
	if m, found := catalog.ByID(input.Fresh); found && view.TTL == 0 {
		view.TTL = providers.CacheTTLFor(m.PrimaryProvider())
	}
	return view
}
