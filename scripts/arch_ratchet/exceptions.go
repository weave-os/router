package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
)

const exceptionsPath = "internal/architecture/ratchet_exceptions.json"

type exception struct {
	Subject string `json:"subject"`
	Metric  metric `json:"metric"`
	Max     int    `json:"max"`
	Reason  string `json:"reason"`
}

type exceptionSet struct {
	Exceptions []exception `json:"exceptions"`
}

func loadExceptions(filePath string) (exceptionSet, error) {
	raw, err := os.ReadFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return exceptionSet{}, nil
	}
	if err != nil {
		return exceptionSet{}, fmt.Errorf("read exceptions: %w", err)
	}
	var set exceptionSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return exceptionSet{}, fmt.Errorf("parse %s: %w", exceptionsPath, err)
	}
	for _, entry := range set.Exceptions {
		if entry.Subject == "" || entry.Reason == "" || entry.Max <= 0 || !slices.Contains(knownMetrics, entry.Metric) {
			return exceptionSet{}, fmt.Errorf("%s: each exception needs a subject, a known metric, a positive max and a reason: %+v", exceptionsPath, entry)
		}
	}
	return set, nil
}

var knownMetrics = []metric{
	metricCognitive, metricLines, metricParams, metricNesting, metricFileLines,
	metricNolint, metricModelMatching, metricServiceFields, metricServiceMethods, metricContextKeys,
}

func (set exceptionSet) permits(subject string, m metric, value int) bool {
	for _, entry := range set.Exceptions {
		if entry.Subject == subject && entry.Metric == m && value <= entry.Max {
			return true
		}
	}
	return false
}
