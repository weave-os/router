package router

// DeployedEntry is one routable target. Direct columns are 1:1
// (Model == BenchColumn); proxy entries reuse another column's scores.
type DeployedEntry struct {
	Model       string `json:"model"`
	Provider    string `json:"provider"`
	BenchColumn string `json:"bench_column"`
	Proxy       bool   `json:"proxy,omitempty"`
	ProxyNote   string `json:"proxy_note,omitempty"`
}

// RequestBindings carries a key's model_aliases (Custom) and enrolled gateway
// providers (Gateways); a non-empty Gateways set means gateway-exclusive routing.
type RequestBindings struct {
	Custom   map[string][]string
	Gateways map[string]struct{}
}

// ModelShare is one model's share of a projected routing distribution.
type ModelShare struct {
	Model string  `json:"model"`
	Share float64 `json:"share"` // fraction in [0, 1]
}

// DistributionPoint is the projected model mix at one dial position (shares
// sum to 1, modulo rounding); ProjectedCostPer1KInputUSD is the share-weighted
// input price for the dashboard's "spend vs all-Opus" readout.
type DistributionPoint struct {
	QualityBias                float64      `json:"quality_bias"`
	Models                     []ModelShare `json:"models"`
	ProjectedCostPer1KInputUSD float64      `json:"projected_cost_per_1k_input_usd"`
}
