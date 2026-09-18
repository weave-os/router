package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
)

const escalationJudgeBaseURL = "https://api.fireworks.ai/inference/v1"
const escalationJudgeResponseLimit = 128 * 1024

// ErrInvalidEscalationJudgment separates paid malformed responses from transport failures.
var ErrInvalidEscalationJudgment = errors.New("invalid escalation judgment")

// EscalationJudge runs a policy-authorized, Weave-funded conversation judgment.
type EscalationJudge struct {
	plans    *policy.PlanResolver
	executor *dispatch.Executor
	apiKey   string
}

// NewEscalationJudge requires platform credentials; caller credentials never apply.
func NewEscalationJudge(plans *policy.PlanResolver, executor *dispatch.Executor, apiKey string) (*EscalationJudge, error) {
	if plans == nil || executor == nil || strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("escalation judge requires policy resolver, executor, and platform key")
	}
	return &EscalationJudge{plans: plans, executor: executor, apiKey: apiKey}, nil
}

// Judge returns measured usage even when the provider's verdict cannot be parsed.
func (j *EscalationJudge) Judge(ctx context.Context, request llmescalation.JudgeRequest) (llmescalation.Judgment, error) {
	plan, err := j.plans.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeEscalationJudge,
		RouterRequest: router.Request{
			EnabledProviders:     map[string]struct{}{providers.ProviderFireworks: {}},
			EstimatedInputTokens: (len(request.Transcript) + len(llmescalation.SystemPrompt)) / 3,
		},
	})
	if err != nil {
		return llmescalation.Judgment{}, fmt.Errorf("resolve escalation judge: %w", err)
	}
	// The synthetic call inherits cancellation, but no tenant headers, credentials,
	// aliases, subscription state, or identity forwarding from the served turn.
	callCtx, cancel := context.WithTimeout(context.Background(), llmescalation.JudgeTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return llmescalation.Judgment{}, err
	}
	callCtx = requestcontext.WithCredentials(callCtx, &requestcontext.Credentials{APIKey: []byte(j.apiKey), BaseURL: escalationJudgeBaseURL})
	judgment := llmescalation.Judgment{CostSource: llmescalation.CostSourceUnknown}
	transport := dispatch.Buffered{
		Reason: string(policy.PurposeEscalationJudge),
		Prepare: func(_ context.Context, attempt dispatch.Attempt) (providers.PreparedRequest, *http.Request, error) {
			body, encodeErr := json.Marshal(struct {
				Model          string                   `json:"model"`
				Messages       []escalationJudgeMessage `json:"messages"`
				ResponseFormat json.RawMessage          `json:"response_format"`
				MaxTokens      int                      `json:"max_tokens"`
				Stream         bool                     `json:"stream"`
			}{Model: attempt.Target.UpstreamID, Messages: []escalationJudgeMessage{{Role: escalationJudgeRoleSystem, Content: llmescalation.SystemPrompt}, {Role: escalationJudgeRoleUser, Content: request.Transcript}}, ResponseFormat: llmescalation.ResponseSchema, MaxTokens: plan.Budget().MaxOutputTokens})
			if encodeErr != nil {
				return providers.PreparedRequest{}, nil, encodeErr
			}
			upstream := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			upstream.Header.Set("Content-Type", "application/json")
			return providers.PreparedRequest{Body: body, Headers: make(http.Header)}, upstream, nil
		},
		Consume: func(_ context.Context, _ dispatch.Attempt, response *http.Response) error {
			defer response.Body.Close()
			if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
				return &providers.UpstreamStatusError{Status: response.StatusCode}
			}
			encoded, readErr := io.ReadAll(io.LimitReader(response.Body, escalationJudgeResponseLimit+1))
			if readErr != nil {
				return fmt.Errorf("read escalation response: %w", readErr)
			}
			if len(encoded) > escalationJudgeResponseLimit {
				return errors.New("escalation response exceeds size limit")
			}
			judgment, readErr = parseEscalationJudgment(encoded)
			return readErr
		},
	}.Transport()
	transport.OperationID = request.OperationID
	_, err = j.executor.Run(callCtx, inference.InvocationRequest{Purpose: policy.PurposeEscalationJudge, RequestID: request.RequestID}, plan, transport)
	return judgment, err
}

type escalationJudgeFinishReason string

const escalationJudgeFinishStop escalationJudgeFinishReason = "stop"

type escalationJudgeRole string

const (
	escalationJudgeRoleSystem escalationJudgeRole = "system"
	escalationJudgeRoleUser   escalationJudgeRole = "user"
)

type escalationJudgeMessage struct {
	Role    escalationJudgeRole `json:"role"`
	Content string              `json:"content"`
}

func parseEscalationJudgment(encoded []byte) (llmescalation.Judgment, error) {
	var response struct {
		Choices []struct {
			FinishReason escalationJudgeFinishReason `json:"finish_reason"`
			Message      struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens        *int     `json:"prompt_tokens"`
			CompletionTokens    *int     `json:"completion_tokens"`
			Cost                *float64 `json:"cost"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	judgment := llmescalation.Judgment{CostSource: llmescalation.CostSourceUnknown}
	if err := json.Unmarshal(encoded, &response); err != nil {
		return judgment, errors.New("invalid escalation response envelope")
	}
	if response.Usage != nil && response.Usage.PromptTokens != nil && response.Usage.CompletionTokens != nil {
		prompt, completion, cached := *response.Usage.PromptTokens, *response.Usage.CompletionTokens, response.Usage.PromptTokensDetails.CachedTokens
		if prompt >= 0 && completion >= 0 && cached >= 0 && cached <= prompt {
			judgment.Usage = inference.Usage{Known: true, InputTokens: prompt, OutputTokens: completion, CacheReadTokens: cached}
			if response.Usage.Cost != nil && *response.Usage.Cost >= 0 {
				judgment.CostUSD = *response.Usage.Cost
				judgment.CostKnown = true
				judgment.CostSource = llmescalation.CostSourceProviderReported
			} else if pricing, found := catalog.PriceFor(providers.ProviderFireworks, policy.EscalationJudgeModel); found {
				judgment.CostUSD = catalog.EffectiveInputCost(prompt, 0, cached, pricing, providers.ProviderFireworks) + catalog.EffectiveOutputCost(prompt, completion, pricing)
				judgment.CostKnown = true
				judgment.CostSource = llmescalation.CostSourceCatalogEstimate
			}
		}
	}
	if len(response.Choices) != 1 || response.Choices[0].FinishReason != escalationJudgeFinishStop {
		return judgment, fmt.Errorf("%w: response has no complete verdict", ErrInvalidEscalationJudgment)
	}
	var verdict struct {
		Escalate *bool   `json:"escalate"`
		Reason   *string `json:"reason"`
	}
	var content string
	if err := json.Unmarshal(response.Choices[0].Message.Content, &content); err != nil {
		return judgment, fmt.Errorf("%w: verdict content is not text", ErrInvalidEscalationJudgment)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&verdict); err != nil {
		return judgment, fmt.Errorf("%w: verdict schema", ErrInvalidEscalationJudgment)
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF || verdict.Escalate == nil || verdict.Reason == nil || strings.TrimSpace(*verdict.Reason) == "" {
		return judgment, fmt.Errorf("%w: verdict fields", ErrInvalidEscalationJudgment)
	}
	judgment.Escalate, judgment.Reason = *verdict.Escalate, *verdict.Reason
	return judgment, nil
}
