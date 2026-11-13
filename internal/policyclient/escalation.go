package policyclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"weave-os/router/internal/router/escalation"
)

const escalationResponseMaxBytes = 2 << 20

// ObserveEscalation performs one optional observation without the policy retry loop.
func (c *Client) ObserveEscalation(ctx context.Context, observation escalation.ObserveRequest) (escalation.ObserveResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	encodedObservation, err := json.Marshal(observation)
	if err != nil {
		return escalation.ObserveResponse{}, fmt.Errorf("encode escalation observation: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/escalation/observe", bytes.NewReader(encodedObservation))
	if err != nil {
		return escalation.ObserveResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return escalation.ObserveResponse{}, fmt.Errorf("observe escalation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return escalation.ObserveResponse{}, fmt.Errorf("escalation service status %d", response.StatusCode)
	}
	encodedResponse, err := io.ReadAll(io.LimitReader(response.Body, escalationResponseMaxBytes+1))
	if err != nil {
		return escalation.ObserveResponse{}, fmt.Errorf("read escalation response: %w", err)
	}
	if len(encodedResponse) > escalationResponseMaxBytes {
		return escalation.ObserveResponse{}, fmt.Errorf("escalation response exceeds %d bytes", escalationResponseMaxBytes)
	}
	var envelope struct {
		escalation.ObserveResponse
		Prediction *struct {
			Score     *float64 `json:"score"`
			Threshold *float64 `json:"threshold"`
			Escalate  *bool    `json:"escalate"`
		} `json:"prediction"`
	}
	if err = json.Unmarshal(encodedResponse, &envelope); err != nil {
		return escalation.ObserveResponse{}, fmt.Errorf("decode escalation response: %w", err)
	}
	observed := envelope.ObserveResponse
	if prediction := envelope.Prediction; prediction != nil {
		// Missing/null fields must not turn into a valid all-zero prediction.
		if prediction.Score == nil || prediction.Threshold == nil || prediction.Escalate == nil {
			return escalation.ObserveResponse{}, fmt.Errorf("incomplete escalation prediction")
		}
		observed.Prediction = &escalation.Prediction{Score: *prediction.Score, Threshold: *prediction.Threshold, Escalate: *prediction.Escalate}
	}
	return observed, observed.Validate(observation.PredictDue)
}
