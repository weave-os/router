package inference

import (
	"sync"
	"time"
)

// AttemptBudget limits new attempts across several independently resolved plans.
// It does not interrupt an already serving stream.
type AttemptBudget struct {
	mu        sync.Mutex
	Remaining int
	Deadline  time.Time
}

func (b *AttemptBudget) Take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Remaining <= 0 || time.Now().After(b.Deadline) {
		return false
	}
	b.Remaining--
	return true
}
