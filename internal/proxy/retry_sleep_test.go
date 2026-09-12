package proxy_test

import (
	"context"
	"time"
)

func noRetrySleep(context.Context, time.Duration) error { return nil }
