package decisionmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Model is the System One judge model the client pins.
const Model = "typesafe/jev-1.13"

// DefaultTimeout bounds one classification round trip.
const DefaultTimeout = 3 * time.Second

const maxResponseBytes = 1 << 20

// QuestionID names one routing question; the value is the key used in the judge request.
type QuestionID string

const (
	QuestionTaskComplexity QuestionID = "task_complexity"
	QuestionCallComplexity QuestionID = "call_complexity"
	QuestionEscalate       QuestionID = "escalate"
)

// Choice is an answer label shared by the routing questions.
type Choice string

const (
	ChoiceHigh Choice = "high"
	ChoiceLow  Choice = "low"
	ChoiceYes  Choice = "yes"
	ChoiceNo   Choice = "no"
)

type questionInstructions struct {
	Question     string `json:"question"`
	Scope        string `json:"scope"`
	GradingRules string `json:"grading_rules"`
}

type question struct {
	Type         string               `json:"type"`
	Instructions questionInstructions `json:"instructions"`
	Criteria     map[Choice]string    `json:"criteria"`
}

const stateScope = "The coding-agent conversation summarized in state: the original objective, recent user messages, and recent agent turns with tool call statuses."

var questions = map[QuestionID]question{
	QuestionTaskComplexity: {Type: "choice", Instructions: questionInstructions{
		Question:     "How complex is the overall task the user asked the coding agent to complete?",
		Scope:        stateScope,
		GradingRules: "Judge the task as a whole, not the latest step. Choose high when finishing it needs open-ended design, multi-system reasoning, or debugging an unknown cause.",
	}, Criteria: map[Choice]string{
		ChoiceHigh: "Open-ended design, cross-cutting reasoning, or debugging with an unknown root cause.",
		ChoiceLow:  "Well-defined, mechanical, or small in scope; the path to done is already clear.",
	}},
	QuestionCallComplexity: {Type: "choice", Instructions: questionInstructions{
		Question:     "Does the agent's next step require new judgment?",
		Scope:        stateScope,
		GradingRules: "Classify by what remains uncertain, not by the tool the agent will use. Editing or searching can still require judgment when the plan is unresolved.",
	}, Criteria: map[Choice]string{
		ChoiceHigh: "Planning, reasoning, reviewing, or debugging: the next step decides what to do.",
		ChoiceLow:  "Executing an already-resolved plan: defined edits, searches, or other grunt work.",
	}},
	QuestionEscalate: {Type: "choice", Instructions: questionInstructions{
		Question:     "Should a stronger model take over the rest of this task?",
		Scope:        stateScope,
		GradingRules: "Choose yes only on evidence the agent is struggling: repeated failing tool calls, unresolved errors, circling without progress, or the user correcting it.",
	}, Criteria: map[Choice]string{
		ChoiceYes: "The agent is stuck, failing repeatedly, or not making progress.",
		ChoiceNo:  "The agent is making steady progress.",
	}},
}

// Answer is the judge's choice and its distribution over the question's labels.
type Answer struct {
	Choice        Choice             `json:"choice"`
	Probabilities map[Choice]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// Classifier answers routing questions about a compact conversation state. Router
// depends on this interface so tests can substitute a deterministic fake.
type Classifier interface {
	Classify(ctx context.Context, state State, ids []QuestionID) (map[QuestionID]Answer, error)
}

// Client calls the System One endpoint with zero-retention, no-fallback provider preferences.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

// NewClient targets baseURL's /systemone endpoint.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{
		endpoint: strings.TrimRight(baseURL, "/") + "/systemone",
		apiKey:   apiKey,
		http:     &http.Client{Timeout: timeout},
	}
}

type providerPreferences struct {
	Only           []string `json:"only"`
	AllowFallbacks bool     `json:"allow_fallbacks"`
	ZDR            bool     `json:"zdr"`
	DataCollection string   `json:"data_collection"`
}

type classifyRequest struct {
	Model     string                  `json:"model"`
	Provider  providerPreferences     `json:"provider"`
	State     State                   `json:"state"`
	Questions map[QuestionID]question `json:"questions"`
}

type classifyResponse struct {
	Answers map[QuestionID]Answer `json:"answers"`
}

// Classify asks every id in one request and validates each answer against its labels.
func (c *Client) Classify(ctx context.Context, state State, ids []QuestionID) (map[QuestionID]Answer, error) {
	asked := make(map[QuestionID]question, len(ids))
	for _, id := range ids {
		asked[id] = questions[id]
	}
	body, err := json.Marshal(classifyRequest{
		Model:     Model,
		Provider:  providerPreferences{Only: []string{"typesafe"}, ZDR: true, DataCollection: "deny"},
		State:     state,
		Questions: asked,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("decision model: status %d", resp.StatusCode)
	}
	var decoded classifyResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("decision model: decode response: %w", err)
	}
	for id, q := range asked {
		answer, found := decoded.Answers[id]
		if !found {
			return nil, fmt.Errorf("decision model: missing answer for %s", id)
		}
		if _, valid := q.Criteria[answer.Choice]; !valid {
			return nil, fmt.Errorf("decision model: %s answered unknown label %q", id, answer.Choice)
		}
	}
	return decoded.Answers, nil
}

// ErrUnavailable reports that the decision model classifier could not produce a routing decision.
var ErrUnavailable = errors.New("decision model classifier unavailable")
