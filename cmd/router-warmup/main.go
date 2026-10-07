// router-warmup plans broad model warmup; live inference requires -execute.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

type warmupRequest struct {
	Model               string          `json:"model"`
	Messages            []warmupMessage `json:"messages"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
}
type warmupRole string

const warmupRoleUser warmupRole = "user"

type warmupMessage struct {
	Role    warmupRole `json:"role"`
	Content string     `json:"content"`
}

const warmupReasoningCompletionTokens = 16000

func warmupPlan() []warmupRequest {
	requests := make([]warmupRequest, 0, len(catalog.Models))
	for _, model := range catalog.Listing() {
		levels := router.Lookup(model.Model).Reasoning().Levels
		effort := ""
		if len(levels) > 0 {
			effort = levels[0]
		}
		completionTokens := 256
		if effort != "" && effort != "none" {
			completionTokens = warmupReasoningCompletionTokens
		}
		requests = append(requests, warmupRequest{Model: model.Model, Messages: []warmupMessage{{Role: warmupRoleUser, Content: "Reply with OK."}}, ReasoningEffort: effort, MaxCompletionTokens: completionTokens})
	}
	return requests
}

func executeWarmup(ctx context.Context, client *http.Client, origin, credential string, requests []warmupRequest) error {
	var failures []error
	sessionID := uuid.NewString()
	for _, prompt := range requests {
		payload, err := json.Marshal(prompt)
		if err != nil {
			return err
		}
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(origin, "/")+"/v1/chat/completions", bytes.NewReader(payload))
		if err != nil {
			cancel()
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(auth.RouterKeyHeader, credential)
		request.Header.Set("Session-Id", sessionID)
		request.Header.Set(proxy.ForceModelHeader, prompt.Model)
		response, err := client.Do(request)
		if err == nil {
			_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("HTTP %d", response.StatusCode)
			}
		}
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("warmup %s: %w", prompt.Model, err))
		}
	}
	clearCtx, cancelClear := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelClear()
	clearPayload := []byte(`{"model":"auto","messages":[{"role":"user","content":"/unforce-model"}]}`)
	clearRequest, clearErr := http.NewRequestWithContext(clearCtx, http.MethodPost, strings.TrimRight(origin, "/")+"/v1/chat/completions", bytes.NewReader(clearPayload))
	if clearErr == nil {
		clearRequest.Header.Set("Content-Type", "application/json")
		clearRequest.Header.Set(auth.RouterKeyHeader, credential)
		clearRequest.Header.Set("Session-Id", sessionID)
		var clearResponse *http.Response
		clearResponse, clearErr = client.Do(clearRequest)
		if clearErr == nil {
			_, clearErr = io.Copy(io.Discard, io.LimitReader(clearResponse.Body, 1<<20))
			clearResponse.Body.Close()
			if clearResponse.StatusCode != http.StatusOK {
				clearErr = fmt.Errorf("HTTP %d", clearResponse.StatusCode)
			}
		}
	}
	if clearErr != nil {
		failures = append(failures, fmt.Errorf("clear warmup model pin: %w", clearErr))
	}
	return errors.Join(failures...)
}

func main() {
	execute := flag.Bool("execute", false, "send live inference for every catalog model (incurs provider costs)")
	origin := flag.String("origin", "", "worker fleet origin")
	flag.Parse()
	requests := warmupPlan()
	if !*execute {
		if err := json.NewEncoder(os.Stdout).Encode(requests); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	credential := os.Getenv("ROUTER_WARMUP_API_KEY")
	if *origin == "" || credential == "" {
		fmt.Fprintln(os.Stderr, "-execute requires -origin and ROUTER_WARMUP_API_KEY")
		os.Exit(1)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if err := executeWarmup(context.Background(), client, *origin, credential, requests); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
