// Fault controller for PR2 fail-open runtime tests.
//
// Listens on two sockets: /control (mode changes) and /fault (TCP faults).
// Modes:
//
//	refuse  — no listener on /fault (SYN refused)
//	stall   — accept HTTP, never write a response
//	reset   — accept TCP, then RST the established connection
//	ok      — healthy HTTP fixture (policy or provider)
//	malformed / fivexx / impossible — HTTP error / invalid selection
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	modeOK         = "ok"
	modeRefuse     = "refuse"
	modeStall      = "stall"
	modeReset      = "reset"
	modeMalformed  = "malformed"
	modeFiveXX     = "fivexx"
	modeImpossible = "impossible"
)

type controller struct {
	mu         sync.Mutex
	mode       string
	role       string
	hits       int
	lastMethod string
	lastPath   string
	lastBody   []byte
	lastModel  string
	faultLn    net.Listener
	faultAddr  string
	listenAddr string
}

func main() {
	listen := getenv("FAIL_OPEN_LISTEN", "0.0.0.0:8091")
	control := getenv("FAIL_OPEN_CONTROL", "0.0.0.0:8092")
	role := getenv("FAIL_OPEN_ROLE", "policy") // policy | anthropic | openai | google
	c := &controller{mode: modeOK, role: role, listenAddr: listen}
	if err := c.openFault(); err != nil {
		log.Fatal(err)
	}
	go serveControl(c, control)
	log.Printf("fail-open controller role=%s fault=%s control=%s", role, listen, control)
	select {}
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func (c *controller) setMode(mode string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = mode
	if mode == modeRefuse {
		return c.closeFaultLocked()
	}
	if c.faultLn == nil {
		return c.openFaultLocked()
	}
	return nil
}

func (c *controller) openFault() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.openFaultLocked()
}

func (c *controller) openFaultLocked() error {
	if c.faultLn != nil {
		return nil
	}
	ln, err := net.Listen("tcp", c.listenAddr)
	if err != nil {
		return err
	}
	c.faultLn = ln
	c.faultAddr = ln.Addr().String()
	go c.acceptLoop(ln)
	return nil
}

func (c *controller) closeFaultLocked() error {
	if c.faultLn == nil {
		return nil
	}
	err := c.faultLn.Close()
	c.faultLn = nil
	return err
}

func (c *controller) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go c.handleConn(conn)
	}
}

func (c *controller) handleConn(conn net.Conn) {
	c.mu.Lock()
	mode := c.mode
	role := c.role
	c.mu.Unlock()

	switch mode {
	case modeReset:
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		_ = conn.Close()
		return
	case modeStall:
		_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
		buf := make([]byte, 1<<16)
		_, _ = conn.Read(buf)
		time.Sleep(90 * time.Second)
		_ = conn.Close()
		return
	}

	serveHTTPOnConn(conn, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		c.mu.Lock()
		c.hits++
		c.lastMethod = r.Method
		c.lastPath = r.URL.Path
		c.lastBody = append([]byte(nil), body...)
		c.lastModel = extractModel(body, r.URL.Path)
		mode = c.mode
		c.mu.Unlock()
		handleFixture(w, r, role, mode, body)
	})
}

func extractModel(body []byte, path string) string {
	var parsed map[string]any
	if json.Unmarshal(body, &parsed) == nil {
		if m, ok := parsed["model"].(string); ok {
			return m
		}
	}
	// Gemini: /v1beta/models/<model>:generateContent
	if i := strings.Index(path, "/models/"); i >= 0 {
		rest := path[i+len("/models/"):]
		if j := strings.Index(rest, ":"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return ""
}

func handleFixture(w http.ResponseWriter, r *http.Request, role, mode string, body []byte) {
	switch r.URL.Path {
	case "/livez", "/readyz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	case "/capabilities":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"policy_router_v1","reports_outcomes":false,"reports_feedback":false,"honors_preferred_models":true,"honors_quality_price_bias":true,"supports_debug_route_detail":false,"supports_preview":false,"supports_shadow":false,"authoritative_per_turn_selection":false}`))
		return
	case "/outcome", "/feedback":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
		return
	}

	switch mode {
	case modeFiveXX:
		http.Error(w, `{"error":"injected"}`, http.StatusBadGateway)
		return
	case modeMalformed:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not-json`))
		return
	case modeImpossible:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"not-a-real-model-zzzz","score":1,"reason":"impossible"}`))
		return
	}

	if role == "policy" && r.URL.Path == "/route" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"claude-sonnet-4-5","score":1,"score_label":"ok","reason":"fixture","state_label":"ok"}`))
		return
	}

	serveProviderOK(w, r, role, body)
}

func serveProviderOK(w http.ResponseWriter, r *http.Request, role string, body []byte) {
	stream := strings.Contains(r.URL.RawQuery, "stream=true") || strings.Contains(string(body), `"stream":true`)
	model := extractModel(body, r.URL.Path)
	if model == "" {
		model = "claude-haiku-4-5"
	}
	switch role {
	case "openai":
		if strings.Contains(r.URL.Path, "/responses") {
			writeJSON(w, fmt.Sprintf(`{"id":"resp_fo","object":"response","status":"completed","model":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fail-open-ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`, model))
			return
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-fo\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"fail-open-ok\"},\"finish_reason\":null}]}\n\n")
			fmt.Fprintf(w, "data: [DONE]\n\n")
			return
		}
		writeJSON(w, fmt.Sprintf(`{"id":"chatcmpl-fo","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"fail-open-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, model))
	case "google":
		writeJSON(w, `{"candidates":[{"content":{"parts":[{"text":"fail-open-ok"}],"role":"model"},"finishReason":"STOP"}]}`)
	default: // anthropic
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fo\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null}}\n\n", model)
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fail-open-ok\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		writeJSON(w, fmt.Sprintf(`{"id":"msg_fo","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"fail-open-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, model))
	}
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func serveHTTPOnConn(conn net.Conn, h http.HandlerFunc) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	rw := &connResponse{conn: conn, header: make(http.Header)}
	h(rw, req)
	rw.flush()
}

type connResponse struct {
	conn      net.Conn
	header    http.Header
	status    int
	wroteHead bool
	body      []byte
}

func (c *connResponse) Header() http.Header { return c.header }
func (c *connResponse) Write(b []byte) (int, error) {
	if !c.wroteHead {
		c.WriteHeader(http.StatusOK)
	}
	c.body = append(c.body, b...)
	return len(b), nil
}
func (c *connResponse) WriteHeader(status int) {
	if c.wroteHead {
		return
	}
	c.status = status
	c.wroteHead = true
}
func (c *connResponse) flush() {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	statusText := http.StatusText(c.status)
	if statusText == "" {
		statusText = "OK"
	}
	if c.header.Get("Content-Type") == "" {
		c.header.Set("Content-Type", "text/plain")
	}
	c.header.Set("Content-Length", fmt.Sprintf("%d", len(c.body)))
	fmt.Fprintf(c.conn, "HTTP/1.1 %d %s\r\n", c.status, statusText)
	_ = c.header.Write(c.conn)
	_, _ = c.conn.Write([]byte("\r\n"))
	_, _ = c.conn.Write(c.body)
}

func serveControl(c *controller, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/mode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		mode := strings.TrimSpace(r.URL.Query().Get("set"))
		if mode == "" {
			var payload struct {
				Mode string `json:"mode"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mode = payload.Mode
		}
		switch mode {
		case modeOK, modeRefuse, modeStall, modeReset, modeMalformed, modeFiveXX, modeImpossible:
		default:
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		if err := c.setMode(mode); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, fmt.Sprintf(`{"mode":%q}`, mode))
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		writeJSON(w, fmt.Sprintf(`{"hits":%d,"mode":%q,"path":%q,"method":%q,"model":%q}`, c.hits, c.mode, c.lastPath, c.lastMethod, c.lastModel))
	})
	mux.HandleFunc("/reset-stats", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.hits = 0
		c.lastBody = nil
		c.lastModel = ""
		c.lastPath = ""
		c.mu.Unlock()
		writeJSON(w, `{"ok":true}`)
	})
	mux.HandleFunc("/last-body", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		body := append([]byte(nil), c.lastBody...)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	log.Fatal(http.ListenAndServe(addr, mux))
}

func init() {
	_ = syscall.SIGPIPE
}
