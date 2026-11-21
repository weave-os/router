package conversation

import (
	"sync"
	"time"

	"weave-os/router/internal/translate"
)

type ThreadCompaction struct {
	AnchorMessages int
	FirstDigest    string
	AnchorDigest   string
	Summary        string
	Model          string
	CreatedAt      time.Time
	UsedAt         time.Time
}

type CompactionStore interface {
	Load(key string, now time.Time) (ThreadCompaction, bool)
	Save(key string, compaction ThreadCompaction)
	Clear(key string)
}

func PlanThreadCompaction(env *translate.RequestEnvelope, tail int, summary, model string, now time.Time) (ThreadCompaction, bool) {
	anchor := env.MessageCount() - tail
	for anchor >= 2 && env.MessageRole(anchor) != "assistant" {
		anchor--
	}
	if anchor < 2 || summary == "" {
		return ThreadCompaction{}, false
	}
	return ThreadCompaction{
		AnchorMessages: anchor,
		FirstDigest:    env.MessageDigest(0),
		AnchorDigest:   env.MessageDigest(anchor - 1),
		Summary:        summary,
		Model:          model,
		CreatedAt:      now,
		UsedAt:         now,
	}, true
}

func ApplyThreadCompaction(env *translate.RequestEnvelope, compaction ThreadCompaction) bool {
	anchor := compaction.AnchorMessages
	if anchor < 2 || env.MessageCount() <= anchor {
		return false
	}
	if env.MessageDigest(0) != compaction.FirstDigest || env.MessageDigest(anchor-1) != compaction.AnchorDigest {
		return false
	}
	return env.ReplacePrefixWithSummary(anchor, compaction.Summary)
}

type MemoryCompactionStore struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]ThreadCompaction
}

func NewMemoryCompactionStore(ttl time.Duration) *MemoryCompactionStore {
	return &MemoryCompactionStore{ttl: ttl, entries: map[string]ThreadCompaction{}}
}

func (s *MemoryCompactionStore) Load(key string, now time.Time) (ThreadCompaction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	compaction, ok := s.entries[key]
	switch {
	case !ok:
		return ThreadCompaction{}, false
	case s.ttl > 0 && now.Sub(compaction.UsedAt) > s.ttl:
		delete(s.entries, key)
		return ThreadCompaction{}, false
	}
	compaction.UsedAt = now
	s.entries[key] = compaction
	return compaction, true
}

func (s *MemoryCompactionStore) Save(key string, compaction ThreadCompaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = compaction
}

func (s *MemoryCompactionStore) Clear(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}
