package main

import (
	"net/http"
	"time"
)

const (
	h2MaxConcurrentStreams          = 1000
	h2MaxReceiveBufferPerConnection = 16 << 20
	h2MaxReceiveBufferPerStream     = 4 << 20
)

func newHTTPServer(address string, handler http.Handler) *http.Server {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		Protocols:         protocols,
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams:          h2MaxConcurrentStreams,
			MaxReceiveBufferPerConnection: h2MaxReceiveBufferPerConnection,
			MaxReceiveBufferPerStream:     h2MaxReceiveBufferPerStream,
		},
	}
}
