package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workweave/router/internal/providers"
)

const (
	loginRequestID     = 2
	loginStartTimeout  = 15 * time.Second
	loginLifetime      = 10 * time.Minute
	stateIdle          = "idle"
	statePending       = "pending"
	stateAuthenticated = "authenticated"
	stateFailed        = "failed"
)

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type loginResult struct {
	Type    string `json:"type"`
	LoginID string `json:"loginId"`
	AuthURL string `json:"authUrl"`
}

type loginCompleted struct {
	LoginID string `json:"loginId"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

type oauthSession struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	loginID   string
	authURL   string
	state     string
	err       string
	responses chan rpcMessage
	writeMu   sync.Mutex
	stopOnce  sync.Once
}

// OAuthManager delegates browser authentication to the installed Codex CLI's
// app-server. It never reads or returns an OAuth token.
type OAuthManager struct {
	mu      sync.Mutex
	session *oauthSession
}

// NewOAuthManager constructs a local Codex OAuth manager.
func NewOAuthManager() *OAuthManager {
	return &OAuthManager{}
}

// Start begins the official browser-based ChatGPT OAuth flow through
// `codex app-server` and returns its authorization URL.
func (m *OAuthManager) Start(ctx context.Context) (providers.CodexOAuthStart, error) {
	m.mu.Lock()
	if m.session != nil {
		if m.session.state == statePending {
			start := providers.CodexOAuthStart{LoginID: m.session.loginID, AuthURL: m.session.authURL}
			m.mu.Unlock()
			return start, nil
		}
		m.session = nil
	}
	m.mu.Unlock()

	command := strings.TrimSpace(os.Getenv("ROUTER_CODEX_BIN"))
	if command == "" {
		command = "codex"
	}
	cmd := exec.CommandContext(context.Background(), command, "app-server", "--stdio")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return providers.CodexOAuthStart{}, fmt.Errorf("codex oauth stdout: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return providers.CodexOAuthStart{}, fmt.Errorf("codex oauth stdin: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return providers.CodexOAuthStart{}, fmt.Errorf("start codex app-server: %w", err)
	}

	session := &oauthSession{
		cmd:       cmd,
		stdin:     stdin,
		state:     statePending,
		responses: make(chan rpcMessage, 4),
	}
	m.mu.Lock()
	m.session = session
	m.mu.Unlock()
	go m.readLoop(session, stdout)

	if err := session.send(map[string]any{
		"method": "initialize",
		"id":     0,
		"params": map[string]any{
			"clientInfo": map[string]string{
				"name":    "weave_router",
				"title":   "Weave Router",
				"version": "0.1.0",
			},
		},
	}); err != nil {
		m.fail(session, err)
		return providers.CodexOAuthStart{}, err
	}
	if err := session.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		m.fail(session, err)
		return providers.CodexOAuthStart{}, err
	}
	if err := session.send(map[string]any{
		"method": "account/login/start",
		"id":     loginRequestID,
		"params": map[string]string{"type": "chatgpt"},
	}); err != nil {
		m.fail(session, err)
		return providers.CodexOAuthStart{}, err
	}

	timer := time.NewTimer(loginStartTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			m.fail(session, ctx.Err())
			return providers.CodexOAuthStart{}, ctx.Err()
		case <-timer.C:
			m.fail(session, errors.New("codex app-server did not return an OAuth URL"))
			return providers.CodexOAuthStart{}, errors.New("codex app-server did not return an OAuth URL")
		case message := <-session.responses:
			if string(message.ID) != "2" {
				continue
			}
			if len(message.Error) > 0 && string(message.Error) != "null" {
				m.fail(session, fmt.Errorf("codex login start failed: %s", string(message.Error)))
				return providers.CodexOAuthStart{}, errors.New("codex login start failed")
			}
			var result loginResult
			if err := json.Unmarshal(message.Result, &result); err != nil || result.LoginID == "" || result.AuthURL == "" {
				m.fail(session, errors.New("codex app-server returned an invalid OAuth response"))
				return providers.CodexOAuthStart{}, errors.New("codex app-server returned an invalid OAuth response")
			}
			m.mu.Lock()
			session.loginID = result.LoginID
			session.authURL = result.AuthURL
			m.mu.Unlock()
			go m.expire(session)
			return providers.CodexOAuthStart{LoginID: result.LoginID, AuthURL: result.AuthURL}, nil
		}
	}
}

// Status reports the active browser login without exposing credentials.
func (m *OAuthManager) Status(context.Context) providers.CodexOAuthStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil {
		return providers.CodexOAuthStatus{
			State:   m.session.state,
			LoginID: m.session.loginID,
			AuthURL: m.session.authURL,
			Error:   m.session.err,
		}
	}
	if localCodexChatGPTAuthExists() {
		return providers.CodexOAuthStatus{State: stateAuthenticated}
	}
	return providers.CodexOAuthStatus{State: stateIdle}
}

// Cancel stops the active browser login, if any.
func (m *OAuthManager) Cancel(context.Context) error {
	m.mu.Lock()
	session := m.session
	pending := session != nil && session.state == statePending
	m.mu.Unlock()
	if !pending {
		return nil
	}
	m.mu.Lock()
	loginID := session.loginID
	m.mu.Unlock()
	if loginID != "" {
		_ = session.send(map[string]any{
			"method": "account/login/cancel",
			"id":     3,
			"params": map[string]string{"loginId": loginID},
		})
	}
	m.mu.Lock()
	if session.state == statePending {
		session.state = stateIdle
	}
	m.mu.Unlock()
	m.stop(session)
	return nil
}

func (m *OAuthManager) readLoop(session *oauthSession, stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var message rpcMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		if message.Method == "account/login/completed" {
			var completed loginCompleted
			if json.Unmarshal(message.Params, &completed) == nil && completed.Success {
				m.mu.Lock()
				session.state = stateAuthenticated
				session.err = ""
				m.mu.Unlock()
				m.stop(session)
			} else {
				message := "Codex OAuth login failed"
				if completed.Error != "" {
					message = completed.Error
				}
				m.fail(session, errors.New(message))
			}
			continue
		}
		if len(message.ID) > 0 {
			select {
			case session.responses <- message:
			default:
			}
		}
	}
	m.mu.Lock()
	pending := session.state == statePending
	m.mu.Unlock()
	if pending {
		m.fail(session, errors.New("codex app-server exited during OAuth login"))
	}
}

func (m *OAuthManager) expire(session *oauthSession) {
	timer := time.NewTimer(loginLifetime)
	defer timer.Stop()
	<-timer.C
	m.mu.Lock()
	pending := session.state == statePending
	m.mu.Unlock()
	if pending {
		m.fail(session, errors.New("Codex OAuth login expired; start again"))
	}
}

func (m *OAuthManager) fail(session *oauthSession, err error) {
	m.mu.Lock()
	session.state = stateFailed
	session.err = err.Error()
	m.mu.Unlock()
	m.stop(session)
}

func (m *OAuthManager) stop(session *oauthSession) {
	session.stopOnce.Do(func() {
		_ = session.stdin.Close()
		if session.cmd.Process != nil {
			_ = session.cmd.Process.Kill()
		}
		go func() { _ = session.cmd.Wait() }()
	})
}

func (s *oauthSession) write(payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	payload = append(payload, '\n')
	_, err := s.stdin.Write(payload)
	return err
}

func (s *oauthSession) send(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.write(payload)
}

func localCodexChatGPTAuthExists() bool {
	base := strings.TrimSpace(os.Getenv("ROUTER_CODEX_AUTH_FILE"))
	if base == "" {
		home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if home == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return false
			}
			home = filepath.Join(userHome, ".codex")
		}
		base = filepath.Join(home, "auth.json")
	}
	data, err := os.ReadFile(base)
	if err != nil {
		return false
	}
	var auth struct {
		AuthMode string `json:"auth_mode"`
	}
	return json.Unmarshal(data, &auth) == nil && strings.EqualFold(strings.TrimSpace(auth.AuthMode), "chatgpt")
}

var _ providers.CodexOAuthLogin = (*OAuthManager)(nil)
