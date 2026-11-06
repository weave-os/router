package policyclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"weave-os/router/internal/router/policy"
)

func (c *Client) PrepareClassifier(ctx context.Context, operationID string) (policy.ClassifierPreparation, error) {
	method, path := http.MethodPost, "/internal/classifiers/qwen/prepare"
	if operationID != "" {
		if !policy.ValidPreparationOperationID(operationID) {
			return policy.ClassifierPreparation{}, fmt.Errorf("invalid classifier preparation operation")
		}
		method, path = http.MethodGet, path+"/"+operationID
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return policy.ClassifierPreparation{}, fmt.Errorf("build classifier preparation request: %w", err)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return policy.ClassifierPreparation{}, fmt.Errorf("call classifier preparation endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return policy.ClassifierPreparation{}, fmt.Errorf("classifier preparation status %d", response.StatusCode)
	}
	var preparation policy.ClassifierPreparation
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<16))
	if err := decoder.Decode(&preparation); err != nil {
		return policy.ClassifierPreparation{}, fmt.Errorf("decode classifier preparation: %w", err)
	}
	if !policy.ValidPreparationOperationID(preparation.OperationID) || operationID != "" && preparation.OperationID != operationID ||
		!(preparation.State == policy.PreparationReady && response.StatusCode == http.StatusOK || preparation.State == policy.PreparationWarming && response.StatusCode == http.StatusAccepted) {
		return policy.ClassifierPreparation{}, fmt.Errorf("invalid classifier preparation response")
	}
	return preparation, nil
}

var _ policy.ClassifierPreparer = (*Client)(nil)
