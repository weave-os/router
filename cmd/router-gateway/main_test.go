package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorkerTransportUsesRequestDeadlineForFirstByte(t *testing.T) {
	for _, test := range []struct {
		name     string
		deadline time.Duration
	}{
		{name: "cold worker takes longer than two minutes", deadline: 600 * time.Second},
		{name: "request deadline still cancels waiting", deadline: 60 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := newWorkerTransport()
				defer transport.CloseIdleConnections()
				transport.Proxy = nil
				workerDone := make(chan struct{})
				transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
					client, worker := net.Pipe()
					go func() {
						defer close(workerDone)
						defer worker.Close()
						request, err := http.ReadRequest(bufio.NewReader(worker))
						if err != nil {
							t.Errorf("read worker request: %v", err)
							return
						}
						defer request.Body.Close()
						time.Sleep(121 * time.Second)
						// A cancelled request closes the pipe before the worker responds.
						_, _ = io.WriteString(worker, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
					}()
					return client, nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), test.deadline)
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://worker.test/v1/models", nil)
				require.NoError(t, err)
				started := time.Now()
				response, err := (&http.Client{Transport: transport}).Do(request)
				defer func() { <-workerDone }()
				if test.deadline < 121*time.Second {
					require.ErrorIs(t, err, context.DeadlineExceeded)
					require.Equal(t, test.deadline, time.Since(started))
					return
				}
				require.NoError(t, err)
				defer response.Body.Close()
				require.Equal(t, http.StatusOK, response.StatusCode)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.Equal(t, "ok", string(body))
				require.Equal(t, 121*time.Second, time.Since(started))
			})
		})
	}
}
