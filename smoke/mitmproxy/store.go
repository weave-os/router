package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type cassette struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       rawTextBody       `json:"body"`
}

type rawTextBody []byte

func (b rawTextBody) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(b))
}

func (b *rawTextBody) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*b = rawTextBody(s)
	return nil
}

type store struct {
	dir string
	mu  sync.Mutex
}

func newStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &store{dir: dir}, nil
}

func requestKey(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(normalizeRequestBody(body))
	return hex.EncodeToString(h.Sum(nil))
}

var volatileBodyFields = []string{"prompt_cache_key"}

func normalizeRequestBody(body []byte) []byte {
	for _, field := range volatileBodyFields {
		if !gjson.GetBytes(body, field).Exists() {
			continue
		}
		out, err := sjson.DeleteBytes(body, field)
		if err != nil {
			continue
		}
		body = out
	}
	return body
}

func (s *store) path(key string) string {
	return filepath.Join(s.dir, key+".json")
}

func (s *store) load(key string) (*cassette, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path(key))
	if err != nil {
		return nil, false
	}
	var c cassette
	if json.Unmarshal(raw, &c) != nil {
		return nil, false
	}
	return &c, true
}

func (s *store) save(key string, c *cassette) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "cassette-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, s.path(key))
}

func sanitizeHeaders(h http.Header) map[string]string {
	drop := map[string]struct{}{
		"authorization":                          {},
		"x-api-key":                              {},
		"proxy-authorization":                    {},
		"cookie":                                 {},
		"set-cookie":                             {},
		"anthropic-organization-id":              {},
		"request-id":                             {},
		"cf-ray":                                 {},
		"anthropic-ratelimit-input-tokens-limit": {},
		"anthropic-ratelimit-input-tokens-remaining":  {},
		"anthropic-ratelimit-input-tokens-reset":      {},
		"anthropic-ratelimit-output-tokens-limit":     {},
		"anthropic-ratelimit-output-tokens-remaining": {},
		"anthropic-ratelimit-output-tokens-reset":     {},
		"anthropic-ratelimit-requests-limit":          {},
		"anthropic-ratelimit-requests-remaining":      {},
		"anthropic-ratelimit-requests-reset":          {},
		"anthropic-ratelimit-tokens-limit":            {},
		"anthropic-ratelimit-tokens-remaining":        {},
		"anthropic-ratelimit-tokens-reset":            {},
		"openai-organization":                         {},
		"openai-project":                              {},
		"openai-processing-ms":                        {},
		"x-request-id":                                {},
		"x-ratelimit-limit-requests":                  {},
		"x-ratelimit-remaining-requests":              {},
		"x-ratelimit-reset-requests":                  {},
		"x-ratelimit-limit-tokens":                    {},
		"x-ratelimit-remaining-tokens":                {},
		"x-ratelimit-reset-tokens":                    {},
		"x-ratelimit-limit-project-tokens":            {},
		"x-ratelimit-remaining-project-tokens":        {},
		"x-ratelimit-reset-project-tokens":            {},
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if _, skip := drop[strings.ToLower(k)]; skip {
			continue
		}
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

var errCacheMiss = fmt.Errorf("cassette: cache miss")
