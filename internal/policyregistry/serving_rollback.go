package policyregistry

import (
	"context"
	"errors"
	"fmt"
)

func (c *ServingController) validateExactRollback(ctx context.Context, proposal ProposalView) error {
	// Only the exact historical object may bypass forward profile preservation.
	snapshot, err := c.store.ReadServingState(ctx, proposal.Target)
	if err != nil {
		c.logger.Error("Failed to read exact rollback history", "target", proposal.Target, "selection_set_sha256", proposal.SelectionSet.SHA256, "err", err)
		return fmt.Errorf("read exact rollback history: %w", err)
	}
	for _, activation := range snapshot.State.Activations {
		if activation.SelectionSet == proposal.SelectionSet {
			return nil
		}
	}
	c.logger.Warn("Rejected exact rollback: selection set was not previously activated on target", "target", proposal.Target, "selection_set_sha256", proposal.SelectionSet.SHA256)
	return errors.New("exact rollback requires a selection set previously activated on the same target")
}
