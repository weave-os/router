package router

type DeployedEntry struct {
	Model       string `json:"model"`
	Provider    string `json:"provider"`
	BenchColumn string `json:"bench_column"`
	Proxy       bool   `json:"proxy,omitempty"`
	ProxyNote   string `json:"proxy_note,omitempty"`
}

type RequestBindings struct {
	Custom   map[string][]string
	Gateways map[string]struct{}
}

type ModelShare struct {
	Model string  `json:"model"`
	Share float64 `json:"share"`
}

type DistributionPoint struct {
	QualityBias                float64      `json:"quality_bias"`
	Models                     []ModelShare `json:"models"`
	ProjectedCostPer1KInputUSD float64      `json:"projected_cost_per_1k_input_usd"`
}
