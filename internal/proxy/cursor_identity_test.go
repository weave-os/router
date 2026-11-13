package proxy

import (
	"net/http"
	"strings"
	"testing"
)

func cursorChatBody(user, agentID string) []byte {
	return []byte(`{"model":"weave-auto","user":"` + user + `","messages":[{"role":"system","content":"You are an AI coding assistant"},{"role":"user","content":"<user_info>\nAvailable persistent agent stores:\n- Current agent's store: /tmp/cursor-1000/cursor_agent_stores/` + agentID + `/files\n- User's personal store: /tmp/cursor-1000/cursor_agent_stores/t1-u2/files\n</user_info>"},{"role":"user","content":"fix the bug"}]}`)
}

func cursorHeaders() http.Header {
	h := http.Header{}
	h.Set("User-Agent", "Cursor/1.0")
	return h
}

func TestChatClientIdentityUsesCursorAgentAsSession(t *testing.T) {
	const user = "google-oauth2|user_01ABC"
	a := ChatClientIdentity(cursorHeaders(), cursorChatBody(user, "22d7b4e0-95b6-47ba-aecb-329729a4066e"))
	b := ChatClientIdentity(cursorHeaders(), cursorChatBody(user, "1bd32df5-d28c-46a7-a715-015226622539"))
	if a.ClientApp != ClientAppCursor {
		t.Fatalf("client app = %q, want cursor", a.ClientApp)
	}
	if a.SessionID != "22d7b4e0-95b6-47ba-aecb-329729a4066e" {
		t.Fatalf("session id = %q, want the Cursor agent id", a.SessionID)
	}
	if a.SessionID == b.SessionID {
		t.Fatal("two Cursor chats from one user share a session id")
	}
}

func TestChatClientIdentityKeepsHeaderSessionForCursor(t *testing.T) {
	h := cursorHeaders()
	h.Set("X-Claude-Code-Session-Id", "header-session")
	got := ChatClientIdentity(h, cursorChatBody("u", "22d7b4e0-95b6-47ba-aecb-329729a4066e"))
	if want := ClientIdentityFromHeaders(h).SessionID; want == "" || got.SessionID != want {
		t.Fatalf("session id = %q, want header-derived %q", got.SessionID, want)
	}
}

func TestChatClientIdentityIgnoresAgentStoreForOtherClients(t *testing.T) {
	h := http.Header{}
	h.Set("User-Agent", "curl/8")
	if got := ChatClientIdentity(h, cursorChatBody("u", "22d7b4e0-95b6-47ba-aecb-329729a4066e")).SessionID; got != "" {
		t.Fatalf("non-Cursor session id = %q, want empty", got)
	}
}

func TestChatClientIdentityReadsCursorAgentFromMacOSStorePath(t *testing.T) {
	const agentID = "5fd236bb-2901-4cf0-8fa6-8f6ec342b8d2"
	body := strings.ReplaceAll(string(cursorChatBody("google-oauth2|user_01ABC", agentID)),
		"/tmp/cursor-1000/", "/Users/dev/Library/Application Support/Cursor/AgentStores/")
	got := ChatClientIdentity(cursorHeaders(), []byte(body))
	if got.SessionID != agentID {
		t.Fatalf("session id = %q, want the Cursor agent id from a store path containing a space", got.SessionID)
	}
}
