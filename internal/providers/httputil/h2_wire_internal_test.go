package httputil

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"weave-os/router/internal/providers"
)

// rawH2Upstream is a TLS server that speaks HTTP/2 frames directly, so a test
// can make the upstream misbehave in ways a real http.Server never would.
// onRequest runs after the response headers for the first stream are sent.
func rawH2Upstream(t *testing.T, onRequest func(fr *http2.Framer, conn net.Conn, streamID uint32)) (string, *tls.Config) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.EnableHTTP2 = true
	srv.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
		"h2": func(_ *http.Server, conn *tls.Conn, _ http.Handler) {
			defer conn.Close()
			preface := make([]byte, len(http2.ClientPreface))
			if _, err := io.ReadFull(conn, preface); err != nil {
				return
			}
			fr := http2.NewFramer(conn, conn)
			if fr.WriteSettings() != nil {
				return
			}
			for {
				frame, err := fr.ReadFrame()
				if err != nil {
					return
				}
				switch f := frame.(type) {
				case *http2.SettingsFrame:
					if !f.IsAck() && fr.WriteSettingsAck() != nil {
						return
					}
				case *http2.HeadersFrame:
					var block bytes.Buffer
					enc := hpack.NewEncoder(&block)
					_ = enc.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
					if fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, BlockFragment: block.Bytes(), EndHeaders: true}) != nil {
						return
					}
					onRequest(fr, conn, f.StreamID)
					return
				}
			}
		},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL, srv.Client().Transport.(*http.Transport).TLSClientConfig
}

func h2BodyReadError(t *testing.T, onRequest func(fr *http2.Framer, conn net.Conn, streamID uint32)) error {
	t.Helper()
	url, tlsConfig := rawH2Upstream(t, onRequest)
	tr := newTransport(time.Second, time.Second, 5*time.Second, false)
	tr.TLSClientConfig = tlsConfig.Clone()
	tr.TLSClientConfig.NextProtos = []string{"h2"}
	t.Cleanup(tr.CloseIdleConnections)

	resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 2, resp.ProtoMajor, "transport must negotiate HTTP/2")
	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)
	return err
}

func TestNewTransport_GoAwayMidBodyIsHTTP2ConnectionLoss(t *testing.T) {
	err := h2BodyReadError(t, func(fr *http2.Framer, _ net.Conn, streamID uint32) {
		_ = fr.WriteGoAway(streamID, http2.ErrCodeEnhanceYourCalm, nil)
	})

	assert.Contains(t, err.Error(), "GOAWAY")
	assert.True(t, providers.IsHTTP2ConnectionLoss(err), "GOAWAY not classified: %v", err)
}

func TestNewTransport_ConnectionErrorMidBodyIsHTTP2ConnectionLoss(t *testing.T) {
	err := h2BodyReadError(t, func(fr *http2.Framer, _ net.Conn, _ uint32) {
		_ = fr.WriteRawFrame(http2.FrameData, 0, 0, []byte("x"))
		time.Sleep(100 * time.Millisecond)
	})

	assert.True(t, providers.IsHTTP2ConnectionLoss(err), "connection error not classified: %v", err)
}

func TestNewTransport_StreamResetMidBodyIsHTTP2ConnectionLoss(t *testing.T) {
	err := h2BodyReadError(t, func(fr *http2.Framer, _ net.Conn, streamID uint32) {
		_ = fr.WriteRSTStream(streamID, http2.ErrCodeInternal)
		time.Sleep(100 * time.Millisecond)
	})

	assert.True(t, providers.IsHTTP2ConnectionLoss(err), "stream reset not classified: %v", err)
}

func TestNewTransport_PingsAnIdleHTTP2Connection(t *testing.T) {
	origIdle := DefaultH2ReadIdleTimeout
	t.Cleanup(func() { DefaultH2ReadIdleTimeout = origIdle })
	DefaultH2ReadIdleTimeout = 50 * time.Millisecond

	pinged := make(chan struct{})
	url, tlsConfig := rawH2Upstream(t, func(fr *http2.Framer, conn net.Conn, _ uint32) {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			frame, err := fr.ReadFrame()
			if err != nil {
				return
			}
			if ping, ok := frame.(*http2.PingFrame); ok && !ping.IsAck() {
				close(pinged)
				return
			}
		}
	})
	tr := newTransport(time.Second, time.Second, 0, false)
	tr.TLSClientConfig = tlsConfig.Clone()
	tr.TLSClientConfig.NextProtos = []string{"h2"}
	t.Cleanup(tr.CloseIdleConnections)

	resp, err := (&http.Client{Transport: tr}).Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()

	select {
	case <-pinged:
	case <-time.After(3 * time.Second):
		t.Fatal("idle HTTP/2 connection was never health-checked with a PING")
	}
}
