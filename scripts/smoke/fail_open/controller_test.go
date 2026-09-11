package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestExtractModelFromChatBody(t *testing.T) {
	got := extractModel([]byte(`{"model":"claude-haiku-4-5","messages":[]}`), "/v1/messages")
	if got != "claude-haiku-4-5" {
		t.Fatalf("model=%q", got)
	}
}

func TestExtractModelFromGeminiPath(t *testing.T) {
	got := extractModel(nil, "/v1beta/models/gemini-2.5-flash:generateContent")
	if got != "gemini-2.5-flash" {
		t.Fatalf("model=%q", got)
	}
}

func TestRefuseClosesListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	c := &controller{mode: modeOK, role: "policy", listenAddr: addr}
	if err := c.openFault(); err != nil {
		t.Fatal(err)
	}
	if err := c.setMode(modeRefuse); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected TCP refusal")
	}
	if err := c.setMode(modeOK); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("listener did not recover: %v", err)
}

func TestControlModeAndStats(t *testing.T) {
	c := &controller{mode: modeOK, role: "anthropic", listenAddr: "127.0.0.1:0"}
	if err := c.openFault(); err != nil {
		t.Fatal(err)
	}
	controlLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(controlLn, controlMux(c))

	base := "http://" + controlLn.Addr().String()
	resp, err := http.Post(base+"/mode?set=fivexx", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	stats, err := http.Get(base + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer stats.Body.Close()
	raw, _ := io.ReadAll(stats.Body)
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["mode"] != modeFiveXX {
		t.Fatalf("mode=%v body=%s", parsed["mode"], raw)
	}
}

func controlMux(c *controller) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mode", func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("set")
		_ = c.setMode(mode)
		writeJSON(w, `{"mode":"`+mode+`"}`)
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		writeJSON(w, `{"hits":0,"mode":"`+c.mode+`","path":"","method":"","model":""}`)
	})
	return mux
}
