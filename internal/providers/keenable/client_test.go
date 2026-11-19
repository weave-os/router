package keenable_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers/keenable"
	"weave-os/router/internal/websearch"
)

const searchResponse = `{
  "query": "go context timeout",
  "mode": "pro",
  "results": [
    {"title": "Context package", "url": "https://pkg.go.dev/context", "description": "", "snippet": "Package context defines the Context type.", "published_at": "2024-01-02T00:00:00Z"},
    {"title": "Go blog: Context", "url": "https://go.dev/blog/context", "description": "Concurrency patterns.", "snippet": ""},
    {"title": "Duplicate", "url": "https://pkg.go.dev/context", "snippet": "dup"},
    {"title": "Not a URL", "url": "ftp://example.invalid/file", "snippet": "skip"},
    {"title": "Third", "url": "https://example.com/third", "snippet": "third result"}
  ]
}`

type captured struct {
	path   string
	auth   string
	title  string
	method string
	body   []byte
}

func newServer(t *testing.T, status int, response string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.method = r.Method
		got.auth = r.Header.Get("authorization")
		got.title = r.Header.Get("X-Keenable-Title")
		got.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestSearchSendsOnlyQueryText(t *testing.T) {
	srv, got := newServer(t, http.StatusOK, searchResponse)

	client := keenable.NewClient(srv.URL)
	if _, err := client.Search(context.Background(), websearch.Query{Text: "  go context timeout  ", MaxResults: 5}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	if got.method != http.MethodPost {
		t.Fatalf("method = %q", got.method)
	}
	req := gjson.ParseBytes(got.body)
	if q := req.Get("query").String(); q != "go context timeout" {
		t.Fatalf("query = %q", q)
	}
	if n := req.Get("max_results").Int(); n != 5 {
		t.Fatalf("max_results = %d", n)
	}
	var keys []string
	req.ForEach(func(k, _ gjson.Result) bool {
		keys = append(keys, k.String())
		return true
	})
	if strings.Join(keys, ",") != "query,max_results" {
		t.Fatalf("request keys = %v; only query and max_results may leave the router (body %s)", keys, got.body)
	}
	for _, forbidden := range []string{"allowed_domains", "blocked_domains", "site", "exclude_domains", "tools", "messages"} {
		if req.Get(forbidden).Exists() {
			t.Fatalf("request carries %q: %s", forbidden, got.body)
		}
	}
}

func TestSearchKeylessAndBearer(t *testing.T) {
	t.Run("keyless omits authorization and uses public path", func(t *testing.T) {
		srv, got := newServer(t, http.StatusOK, searchResponse)
		client := keenable.NewClient(srv.URL)
		if !client.Keyless() {
			t.Fatal("client without key must be keyless")
		}
		if _, err := client.Search(context.Background(), websearch.Query{Text: "q"}); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got.auth != "" {
			t.Fatalf("authorization = %q; keyless must not send one", got.auth)
		}
		if got.path != "/v1/search/public" {
			t.Fatalf("path = %q", got.path)
		}
		if got.title == "" {
			t.Fatal("X-Keenable-Title must be set; the public endpoint rejects requests without it")
		}
	})

	t.Run("blank key stays keyless", func(t *testing.T) {
		srv, got := newServer(t, http.StatusOK, searchResponse)
		client := keenable.NewClient(srv.URL, keenable.WithAPIKey("   "))
		if _, err := client.Search(context.Background(), websearch.Query{Text: "q"}); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got.auth != "" || got.path != "/v1/search/public" {
			t.Fatalf("auth = %q path = %q", got.auth, got.path)
		}
	})

	t.Run("key sends bearer and uses authenticated path", func(t *testing.T) {
		srv, got := newServer(t, http.StatusOK, searchResponse)
		client := keenable.NewClient(srv.URL, keenable.WithAPIKey("kn-test-key"), keenable.WithAppTitle("custom-title"))
		if client.Keyless() {
			t.Fatal("client with key must not be keyless")
		}
		if _, err := client.Search(context.Background(), websearch.Query{Text: "q"}); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got.auth != "Bearer kn-test-key" {
			t.Fatalf("authorization = %q", got.auth)
		}
		if got.path != "/v1/search" {
			t.Fatalf("path = %q", got.path)
		}
		if got.title != "custom-title" {
			t.Fatalf("X-Keenable-Title = %q", got.title)
		}
	})
}

func TestSearchParsesResults(t *testing.T) {
	srv, _ := newServer(t, http.StatusOK, searchResponse)
	client := keenable.NewClient(srv.URL + "/")

	resp, err := client.Search(context.Background(), websearch.Query{Text: "go context timeout"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []websearch.Result{
		{Title: "Context package", URL: "https://pkg.go.dev/context", Snippet: "Package context defines the Context type.", PageAge: "2024-01-02T00:00:00Z"},
		{Title: "Go blog: Context", URL: "https://go.dev/blog/context", Snippet: "Concurrency patterns."},
		{Title: "Third", URL: "https://example.com/third", Snippet: "third result"},
	}
	if len(resp.Results) != len(want) {
		t.Fatalf("results = %+v", resp.Results)
	}
	for i := range want {
		if resp.Results[i] != want[i] {
			t.Fatalf("result[%d] = %+v, want %+v", i, resp.Results[i], want[i])
		}
	}
	if resp.Summary != "" {
		t.Fatalf("summary = %q; the search API returns no summary", resp.Summary)
	}
}

func TestSearchRespectsMaxResults(t *testing.T) {
	srv, got := newServer(t, http.StatusOK, searchResponse)
	client := keenable.NewClient(srv.URL)

	resp, err := client.Search(context.Background(), websearch.Query{Text: "q", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://pkg.go.dev/context" {
		t.Fatalf("results = %+v", resp.Results)
	}
	if n := gjson.GetBytes(got.body, "max_results").Int(); n != 1 {
		t.Fatalf("max_results = %d", n)
	}
}

func TestSearchCapsMaxResultsAtDefault(t *testing.T) {
	for _, requested := range []int{0, -3, websearch.DefaultMaxResults + 40} {
		srv, got := newServer(t, http.StatusOK, searchResponse)
		client := keenable.NewClient(srv.URL)
		if _, err := client.Search(context.Background(), websearch.Query{Text: "q", MaxResults: requested}); err != nil {
			t.Fatalf("Search(%d): %v", requested, err)
		}
		if n := gjson.GetBytes(got.body, "max_results").Int(); n != websearch.DefaultMaxResults {
			t.Fatalf("requested %d: max_results = %d, want %d", requested, n, websearch.DefaultMaxResults)
		}
	}
}

func TestSearchNon2xxCarriesStatus(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusBadGateway} {
		srv, _ := newServer(t, status, `{"error":"echoed request content"}`)
		client := keenable.NewClient(srv.URL)
		_, err := client.Search(context.Background(), websearch.Query{Text: "q"})
		if err == nil {
			t.Fatalf("status %d: expected error", status)
		}
		var statusErr *keenable.StatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != status {
			t.Fatalf("status %d: err = %v", status, err)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(status)) {
			t.Fatalf("status %d: error %q does not mention the status code", status, err)
		}
		if strings.Contains(err.Error(), "echoed request content") {
			t.Fatalf("error includes upstream response content: %v", err)
		}
	}
}

func TestSearchRejectsInvalidJSON(t *testing.T) {
	srv, _ := newServer(t, http.StatusOK, `not json`)
	if _, err := keenable.NewClient(srv.URL).Search(context.Background(), websearch.Query{Text: "q"}); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSearchRejectsEmptyQuery(t *testing.T) {
	srv, got := newServer(t, http.StatusOK, searchResponse)
	if _, err := keenable.NewClient(srv.URL).Search(context.Background(), websearch.Query{Text: "   "}); err == nil {
		t.Fatal("expected error for empty query")
	}
	if got.body != nil {
		t.Fatal("empty query must not reach the search API")
	}
}

func TestSearchTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	client := keenable.NewClient(srv.URL, keenable.WithTimeout(50*time.Millisecond))
	start := time.Now()
	_, err := client.Search(context.Background(), websearch.Query{Text: "q"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Search took %v; timeout not applied", elapsed)
	}
}

func TestSearchCallerDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := keenable.NewClient(srv.URL).Search(ctx, websearch.Query{Text: "q"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestSearchClientTimeoutIsTimeoutError(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	_, err := keenable.NewClient(srv.URL, keenable.WithTimeout(50*time.Millisecond)).Search(context.Background(), websearch.Query{Text: "q"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var netErr interface{ Timeout() bool }
	if !errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &netErr) || !netErr.Timeout()) {
		t.Fatalf("err = %v, want a timeout error", err)
	}
	var statusErr *keenable.StatusError
	if errors.As(err, &statusErr) {
		t.Fatalf("timeout surfaced as status error: %v", err)
	}
}

func TestSearchTruncatesLongSnippets(t *testing.T) {
	long := strings.Repeat("a", 1000)
	srv, _ := newServer(t, http.StatusOK, `{"results":[{"title":"Long","url":"https://example.com/long","snippet":"`+long+`"}]}`)

	resp, err := keenable.NewClient(srv.URL).Search(context.Background(), websearch.Query{Text: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v", resp.Results)
	}
	snippet := resp.Results[0].Snippet
	if !strings.HasSuffix(snippet, "…") || len([]rune(snippet)) > 401 {
		t.Fatalf("snippet length = %d, want truncated with ellipsis", len([]rune(snippet)))
	}
}

func TestSearchEmptyResults(t *testing.T) {
	for _, body := range []string{`{}`, `{"results":[]}`, `{"results":null}`} {
		srv, _ := newServer(t, http.StatusOK, body)
		resp, err := keenable.NewClient(srv.URL).Search(context.Background(), websearch.Query{Text: "q"})
		if err != nil {
			t.Fatalf("body %s: Search: %v", body, err)
		}
		if len(resp.Results) != 0 || resp.Summary != "" {
			t.Fatalf("body %s: resp = %+v", body, resp)
		}
	}
}

func TestSearchNon2xxIgnoresResultsBody(t *testing.T) {
	srv, _ := newServer(t, http.StatusInternalServerError, searchResponse)
	resp, err := keenable.NewClient(srv.URL).Search(context.Background(), websearch.Query{Text: "q"})
	var statusErr *keenable.StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("err = %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results returned alongside status error: %+v", resp.Results)
	}
}
