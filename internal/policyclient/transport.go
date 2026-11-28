package policyclient

import (
	"net/http"
	"time"
)

var sidecarTransport http.RoundTripper = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 128
	t.MaxIdleConnsPerHost = 64
	t.ForceAttemptHTTP2 = true
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: 30 * time.Second, PingTimeout: 5 * time.Second}
	return t
}()

func withSidecarTransport(client *http.Client, timeout time.Duration) *http.Client {
	if client == nil {
		return &http.Client{Transport: sidecarTransport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if client.Transport != nil {
		return client
	}
	c := *client
	c.Transport = sidecarTransport
	return &c
}
