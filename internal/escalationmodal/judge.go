package escalationmodal

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
	"strings"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/router/llmescalation"
)

const maxResponseBytes = 16 * 1024

var (
	releaseNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,127}$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Judge calls the authenticated Modal escalation classifier. The endpoint pins
// the release; Judge records whichever release served each verdict.
type Judge struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

// NewJudge requires the production HTTPS endpoint and a dedicated platform key.
func NewJudge(endpoint, apiKey string, client *http.Client) (*Judge, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("LLM escalation endpoint must be an HTTPS origin")
	}
	if len(apiKey) < 32 {
		return nil, errors.New("LLM escalation API key must contain at least 32 characters")
	}
	if client == nil {
		client = &http.Client{Timeout: llmescalation.JudgeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Judge{endpoint: strings.TrimRight(parsed.String(), "/") + "/classify", apiKey: apiKey, client: client}, nil
}

// Judge returns an escalation verdict only for a verified release and native digit.
func (j *Judge) Judge(ctx context.Context, request llmescalation.JudgeRequest) (llmescalation.Judgment, error) {
	if request.Transcript == "" {
		return llmescalation.Judgment{}, fmt.Errorf("%w: empty interval", llmescalation.ErrInvalidJudgment)
	}
	encoded, err := json.Marshal(struct {
		User string `json:"user"`
	}{User: request.Transcript})
	if err != nil {
		return llmescalation.Judgment{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, j.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return llmescalation.Judgment{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+j.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-Request-ID", request.RequestID)
	httpRequest.Header.Set(llmescalation.QwenRendererHeader, llmescalation.QwenRendererVersion)
	response, err := j.client.Do(httpRequest)
	if err != nil {
		return llmescalation.Judgment{}, fmt.Errorf("call LLM escalation endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return llmescalation.Judgment{}, fmt.Errorf("LLM escalation endpoint returned HTTP %d", response.StatusCode)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return llmescalation.Judgment{}, fmt.Errorf("read LLM escalation response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return llmescalation.Judgment{}, fmt.Errorf("%w: response exceeds size limit", llmescalation.ErrInvalidJudgment)
	}
	var verdict struct {
		ReleaseName   string    `json:"release_name"`
		ModelSHA256   string    `json:"model_sha256"`
		PromptSHA256  *string   `json:"prompt_sha256"`
		Prediction    *int      `json:"prediction"`
		RawIsDigit    *bool     `json:"raw_is_digit"`
		Probabilities []float64 `json:"probabilities"`
		InputTokens   *int      `json:"input_tokens"`
	}
	if json.Unmarshal(responseBody, &verdict) != nil || !releaseNamePattern.MatchString(verdict.ReleaseName) || !sha256Pattern.MatchString(verdict.ModelSHA256) || (verdict.PromptSHA256 != nil && !sha256Pattern.MatchString(*verdict.PromptSHA256)) || verdict.Prediction == nil || (*verdict.Prediction != 0 && *verdict.Prediction != 1) || verdict.RawIsDigit == nil || !*verdict.RawIsDigit || verdict.InputTokens == nil || *verdict.InputTokens < 1 || len(verdict.Probabilities) != 2 {
		return llmescalation.Judgment{}, fmt.Errorf("%w: malformed response or release identity", llmescalation.ErrInvalidJudgment)
	}
	promptSHA256 := ""
	if verdict.PromptSHA256 != nil {
		promptSHA256 = *verdict.PromptSHA256
	}
	return llmescalation.Judgment{
		Escalate:   *verdict.Prediction == 1,
		Usage:      inference.Usage{Known: true, InputTokens: *verdict.InputTokens, OutputTokens: 1},
		CostSource: llmescalation.CostSourceUnknown,
		Release:    &llmescalation.ServedRelease{Name: verdict.ReleaseName, ModelSHA256: verdict.ModelSHA256, PromptSHA256: promptSHA256},
	}, nil
}
