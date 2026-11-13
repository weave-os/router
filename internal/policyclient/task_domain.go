package policyclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"weave-os/router/internal/router/taskdomain"
)

var taskDomainOutput = regexp.MustCompile(`^[01](,[01]){4}$`)

// TaskDomainClassifier calls an authenticated, immutable five-bit Qwen release.
type TaskDomainClassifier struct {
	endpoint      string
	bearer        string
	releaseSHA256 string
	client        *http.Client
}

// NewTaskDomainClassifier forbids redirects and request-selected endpoints or models.
func NewTaskDomainClassifier(endpoint, bearer, releaseSHA256 string, client *http.Client) (*TaskDomainClassifier, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || len(bearer) < 32 || strings.ContainsAny(bearer, "\r\n") || !taskdomain.ValidDigest(releaseSHA256) {
		return nil, errors.New("invalid task classifier endpoint, authentication or release")
	}
	transport := http.DefaultTransport
	if client != nil && client.Transport != nil {
		transport = client.Transport
	}
	return &TaskDomainClassifier{endpoint: strings.TrimRight(endpoint, "/") + "/classify", bearer: bearer, releaseSHA256: releaseSHA256, client: &http.Client{Transport: transport, Timeout: taskdomain.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Classify validates the release identity and exact output grammar on every call.
func (c *TaskDomainClassifier) Classify(ctx context.Context, userText string) (taskdomain.Profile, error) {
	if userText == "" || len(userText) > taskdomain.MaxInputBytes {
		return nil, errors.New("invalid task classifier input size")
	}
	body, err := json.Marshal(struct {
		SchemaVersion     string `json:"schema_version"`
		ReleaseSHA256     string `json:"release_sha256"`
		ProjectionVersion string `json:"projection_version"`
		UserText          string `json:"user_text"`
	}{taskdomain.SchemaVersion, c.releaseSHA256, taskdomain.ProjectionVersion, userText})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.bearer)
	request.Header.Set("Content-Type", "application/json")
	if deadline, ok := ctx.Deadline(); ok {
		request.Header.Set(taskdomain.BudgetHeader, strconv.FormatInt(min(max(1, time.Until(deadline).Milliseconds()), taskdomain.MaxBudgetMilliseconds), 10))
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("task classifier transport: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("task classifier HTTP %d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return nil, err
	}
	if len(payload) > 4096 {
		return nil, errors.New("task classifier response exceeds limit")
	}
	var prediction struct {
		SchemaVersion string `json:"schema_version"`
		ReleaseSHA256 string `json:"release_sha256"`
		Output        string `json:"output"`
		InputTokens   int    `json:"input_tokens"`
	}
	if err := json.Unmarshal(payload, &prediction); err != nil {
		return nil, errors.New("invalid task classifier response")
	}
	if prediction.SchemaVersion != taskdomain.SchemaVersion || prediction.ReleaseSHA256 != c.releaseSHA256 || prediction.InputTokens < 1 || prediction.InputTokens > taskdomain.MaxInputTokens || !taskDomainOutput.MatchString(prediction.Output) {
		return nil, errors.New("invalid task classifier facts or release")
	}
	profile := make(taskdomain.Profile, 5)
	for i, domain := range []taskdomain.Domain{taskdomain.UI, taskdomain.Logic, taskdomain.Data, taskdomain.Infra, taskdomain.Docs} {
		profile[domain] = prediction.Output[i*2] == '1'
	}
	return profile, nil
}
