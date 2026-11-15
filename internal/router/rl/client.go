package rl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/observability"
)

const DefaultTimeout = 2 * time.Second

type HTTPDecider struct {
	baseURL string
	client  *http.Client
	headers map[string]string
}

func NewHTTPDecider(baseURL string, client *http.Client, timeout time.Duration) *HTTPDecider {
	return NewHTTPDeciderWithHeaders(baseURL, client, timeout, nil)
}

func NewHTTPDeciderWithHeaders(baseURL string, client *http.Client, timeout time.Duration, headers map[string]string) *HTTPDecider {
	if client == nil {
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		client = &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	client = observability.WrapHTTPClient(client)
	copied := map[string]string{}
	for k, v := range headers {
		if strings.TrimSpace(k) == "" || v == "" {
			continue
		}
		copied[k] = v
	}
	return &HTTPDecider{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
		headers: copied,
	}
}

type routeRequest struct {
	PromptText         string            `json:"prompt_text"`
	TurnIndex          int               `json:"turn_index"`
	CandidateModels    []string          `json:"candidate_models"`
	CandidateProviders map[string]string `json:"candidate_providers"`
}

type routeResponse struct {
	Model      string  `json:"model"`
	Score      float64 `json:"score"`
	ScoreLabel string  `json:"score_label"`
	Reason     string  `json:"reason"`
	StateLabel string  `json:"state_label"`
	Error      string  `json:"error"`
}

func (d *HTTPDecider) Decide(ctx context.Context, q Query) (Result, error) {
	models := make([]string, 0, len(q.Candidates))
	providers := make(map[string]string, len(q.Candidates))
	for _, c := range q.Candidates {
		models = append(models, c.RosterID)
		providers[c.RosterID] = c.Provider
	}
	body, err := json.Marshal(routeRequest{
		PromptText:         q.PromptText,
		TurnIndex:          q.TurnIndex,
		CandidateModels:    models,
		CandidateProviders: providers,
	})
	if err != nil {
		return Result{}, fmt.Errorf("marshal route request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/route", bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("build route request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	for k, v := range d.headers {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("call policy sidecar: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("read policy response: %w", err)
	}

	var parsed routeResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return Result{}, fmt.Errorf("decode policy response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		if parsed.Error != "" {
			return Result{}, fmt.Errorf("policy sidecar status %d: %s", resp.StatusCode, parsed.Error)
		}
		return Result{}, fmt.Errorf("policy sidecar status %d", resp.StatusCode)
	}
	if parsed.Model == "" {
		return Result{}, fmt.Errorf("policy sidecar returned empty model")
	}

	return Result{
		Model:      parsed.Model,
		Score:      parsed.Score,
		ScoreLabel: parsed.ScoreLabel,
		Reason:     parsed.Reason,
		StateLabel: parsed.StateLabel,
	}, nil
}

var _ Decider = (*HTTPDecider)(nil)
