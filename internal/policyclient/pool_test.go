package policyclient

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSidecarBurstReusesConnections(t *testing.T) {
	atomicClient, err := NewAtomicClassifier("https://policy.example", strings.Repeat("a", 32), "release", strings.Repeat("b", 64), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]*http.Client{"atomic": atomicClient.client, "plain": New("", nil, time.Second*5).client, "supplied": New("", &http.Client{}, time.Second*5).client} {
		t.Run(name, func(t *testing.T) {
			const width = 4
			arrived := make(chan struct{}, width)
			release := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				arrived <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			var connections atomic.Int32
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			defer close(release)
			for range 2 {
				runPolicyBurst(t, client, server.URL, arrived, release, width)
			}
			if n := connections.Load(); n != width {
				t.Fatalf("two bursts opened %d connections; want %d", n, width)
			}
		})
	}
}

func runPolicyBurst(t *testing.T, client *http.Client, url string, arrived <-chan struct{}, release chan<- struct{}, width int) {
	t.Helper()
	done := make(chan error, width)
	for range width {
		go func() {
			resp, err := client.Get(url)
			if err == nil {
				err = resp.Body.Close()
			}
			done <- err
		}()
	}
	for range width {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("burst did not reach server")
		}
	}
	for range width {
		release <- struct{}{}
	}
	for range width {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
