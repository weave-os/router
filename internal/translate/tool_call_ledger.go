package translate

import (
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"weave-os/router/internal/translate/toolcheck"
)

type ToolCallEntry struct {
	ExternalID  string
	SourceID    string
	SourceIndex int
	Name        string
	Arguments   strings.Builder
	Open        bool
	Closed      bool
	aliases     map[string]struct{}
	accepted    *AcceptedToolCall
	projectedID string
}

type ToolCallLedger struct {
	entries         map[int]*ToolCallEntry
	nextID          uint64
	externalOwners  map[string]int
	projectedOwners map[string]int
}

func NewToolCallLedger() *ToolCallLedger {
	return &ToolCallLedger{entries: make(map[int]*ToolCallEntry), externalOwners: make(map[string]int), projectedOwners: make(map[string]int)}
}

func (l *ToolCallLedger) Upsert(index int, sourceID, name string) *ToolCallEntry {
	entry, ok := l.entries[index]
	if !ok {
		externalID := sourceID
		if _, claimed := l.externalOwners[externalID]; claimed {
			externalID = ""
		}
		for externalID == "" {
			l.nextID++
			candidate := "call_router_" + strconv.FormatUint(l.nextID, 36)
			if _, claimed := l.externalOwners[candidate]; !claimed {
				externalID = candidate
			}
		}
		l.externalOwners[externalID] = index
		entry = &ToolCallEntry{
			ExternalID:  strings.Clone(externalID),
			SourceIndex: index,
			Open:        true,
			aliases:     make(map[string]struct{}),
		}
		l.entries[index] = entry
	}
	if sourceID != "" {
		sourceID = strings.Clone(sourceID)
	}
	if sourceID != "" {
		if entry.SourceID == "" {
			entry.SourceID = sourceID
		}
		entry.aliases[sourceID] = struct{}{}
	}
	if entry.accepted != nil {
		return entry
	}
	if name != "" && entry.Name == "" {
		entry.Name = strings.Clone(name)
	}
	return entry
}

func (l *ToolCallLedger) AppendArguments(index int, sourceID, name, arguments string) *ToolCallEntry {
	entry := l.Upsert(index, sourceID, name)
	if entry.accepted == nil {
		entry.Arguments.WriteString(arguments)
	}
	return entry
}

func (l *ToolCallLedger) Close(index int, sourceID, name, fallbackArguments string) *ToolCallEntry {
	entry := l.Upsert(index, sourceID, name)
	if entry.accepted != nil {
		return entry
	}
	selected := selectToolArguments(entry.Arguments.String(), fallbackArguments)
	entry.Arguments.Reset()
	entry.Arguments.WriteString(selected)
	entry.Open = false
	entry.Closed = true
	return entry
}

func (l *ToolCallLedger) Entry(index int) (*ToolCallEntry, bool) {
	entry, ok := l.entries[index]
	return entry, ok
}

func (e *ToolCallEntry) HasSourceID(id string) bool {
	_, ok := e.aliases[id]
	return ok
}

type ToolCallSuppression uint8

const (
	ToolCallNotSuppressed ToolCallSuppression = iota
	ToolCallMissingName
)

type AcceptedToolCall struct {
	ID          string
	Name        string
	Arguments   string
	Suppression ToolCallSuppression
	Issue       *toolcheck.Issue
}

func (e *ToolCallEntry) Accept(validator *toolcheck.Validator) AcceptedToolCall {
	return e.accept(validator, false)
}

func (e *ToolCallEntry) acceptRaw() AcceptedToolCall {
	return e.accept(nil, true)
}

func (e *ToolCallEntry) accept(validator *toolcheck.Validator, raw bool) AcceptedToolCall {
	if e.accepted != nil {
		return *e.accepted
	}
	call := AcceptedToolCall{ID: e.ExternalID, Name: e.Name}
	if e.Name == "" {
		call.Suppression = ToolCallMissingName
	} else if raw {
		call.Arguments = e.Arguments.String()
	} else {
		verdict := validator.Check(e.Name, e.Arguments.String())
		call.Arguments, call.Issue = verdict.Args, verdict.Issue
	}
	e.Open, e.Closed = false, true
	e.accepted = &call
	return call
}

func (l *ToolCallLedger) SetArguments(index int, arguments string) {
	entry := l.Upsert(index, "", "")
	if entry.accepted != nil {
		return
	}
	entry.Arguments.Reset()
	entry.Arguments.WriteString(arguments)
}

func (l *ToolCallLedger) Entries() []*ToolCallEntry {
	indices := make([]int, 0, len(l.entries))
	for index := range l.entries {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	entries := make([]*ToolCallEntry, 0, len(indices))
	for _, index := range indices {
		entries = append(entries, l.entries[index])
	}
	return entries
}

func selectToolArguments(buffered, fallback string) string {
	if buffered == "" || (!gjson.Valid(buffered) && gjson.Valid(fallback)) {
		return fallback
	}
	return buffered
}

func (l *ToolCallLedger) SuppressedCount() int {
	count := 0
	for _, entry := range l.entries {
		if entry.accepted != nil && entry.accepted.Suppression != ToolCallNotSuppressed {
			count++
		}
	}
	return count
}

func (l *ToolCallLedger) ProjectID(index int, project func(string) string) string {
	entry := l.entries[index]
	if entry.projectedID != "" {
		return entry.projectedID
	}
	candidate := project(entry.ExternalID)
	for {
		if _, claimed := l.projectedOwners[candidate]; candidate != "" && !claimed {
			break
		}
		l.nextID++
		candidate = project("call_router_" + strconv.FormatUint(l.nextID, 36))
	}
	l.projectedOwners[candidate] = index
	entry.projectedID = candidate
	return candidate
}
