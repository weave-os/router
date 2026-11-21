package taskdomain

import (
	"encoding/json"
	"log/slog"
)

type PreviewStatus string

const (
	PreviewReady            PreviewStatus = "ready"
	PreviewInvalidRequest   PreviewStatus = "invalid_request"
	PreviewEvidenceRejected PreviewStatus = "evidence_rejected"
	PreviewFailed           PreviewStatus = "failed"
)

type PreviewRequest struct {
	Policy                json.RawMessage `json:"policy"`
	Evidence              json.RawMessage `json:"evidence"`
	Cluster               string          `json:"cluster"`
	Harness               string          `json:"harness"`
	SubscriptionProviders []string        `json:"subscription_providers,omitempty"`
	Domains               []Domain        `json:"domains"`
}

type PreviewItem struct {
	Status                  PreviewStatus `json:"status"`
	RosterSHA256            string        `json:"roster_sha256,omitempty"`
	EvidenceSHA256          string        `json:"evidence_sha256,omitempty"`
	BenchmarkSnapshotSHA256 string        `json:"benchmark_snapshot_sha256,omitempty"`
	BenchmarkIngestDate     string        `json:"benchmark_ingest_date,omitempty"`
	Preview                 any           `json:"preview,omitempty"`
}

type PreviewRecipes struct {
	Version   string
	Influence float64
	Recipes   any
}

type Previewer interface {
	PreviewRecipes() PreviewRecipes
	Preview(logger *slog.Logger, req PreviewRequest, profile Profile) PreviewItem
}
