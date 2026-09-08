// Command genprices rewrites every client-side pricing artifact from the
// canonical router catalog exposed by internal/observability/otel/pricing.go.
// Run via `make generate`.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"weave-os/router/internal/observability/otel"
)

const (
	beginMarker = "# BEGIN_GENERATED_PRICES"
	endMarker   = "# END_GENERATED_PRICES"
)

// scriptPaths lists files containing a BEGIN/END_GENERATED_PRICES block.
// install.sh carries it in a heredoc, letting the installer write
// cc-statusline.sh without a URL fetch.
var scriptPaths = []string{
	"install/cc-statusline.sh",
	"install/install.sh",
}

const piPricingPath = "install/pi-router/src/pricing.generated.ts"

// benchPricingPath is the list-price table the Python benchmark harness
// (bench/) uses to reprice Codex token counts; JSON so it needs no Go toolchain.
const benchPricingPath = "bench/weave_bench/prices.generated.json"

func main() {
	table := otel.AllPricing()
	block := buildBlock(table)
	for _, path := range scriptPaths {
		if err := spliceFile(path, block); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote %s\n", path)
	}
	if err := os.WriteFile(piPricingPath, []byte(buildTypeScript(table)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: writing %s: %v\n", piPricingPath, err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %s\n", piPricingPath)
	benchJSON, err := buildBenchJSON(table)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: encoding %s: %v\n", benchPricingPath, err)
		os.Exit(1)
	}
	if err := os.WriteFile(benchPricingPath, benchJSON, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: writing %s: %v\n", benchPricingPath, err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %s\n", benchPricingPath)
}

// benchModelPricing mirrors catalog.Pricing in the catalog's native USD/1M
// units plus the effective cache multipliers, so the harness can compute
// list-price cost exactly like catalog.Cost without re-deriving defaults.
type benchModelPricing struct {
	InputUSDPerMillion   float64 `json:"input_usd_per_million"`
	OutputUSDPerMillion  float64 `json:"output_usd_per_million"`
	CacheReadMultiplier  float64 `json:"cache_read_multiplier"`
	CacheWriteMultiplier float64 `json:"cache_write_multiplier"`
}

type benchPricingFile struct {
	PricingVersion string                       `json:"pricing_version"`
	Models         map[string]benchModelPricing `json:"models"`
}

func buildBenchJSON(table map[string]otel.Pricing) ([]byte, error) {
	models := sortedModels(table)
	file := benchPricingFile{
		PricingVersion: pricingVersion(table, models),
		Models:         make(map[string]benchModelPricing, len(models)),
	}
	for _, model := range models {
		price := table[model]
		file.Models[model] = benchModelPricing{
			InputUSDPerMillion:   price.InputUSDPer1M,
			OutputUSDPerMillion:  price.OutputUSDPer1M,
			CacheReadMultiplier:  price.EffectiveCacheReadMultiplier(),
			CacheWriteMultiplier: price.EffectiveCacheWriteMultiplier(),
		}
	}
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func spliceFile(path, block string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	updated, err := splice(path, string(raw), block)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o755)
}

// buildBlock produces the shell prices='{...}' assignment from the pricing
// table, in USD/1k (= USD/1M ÷ 1000) to match cc-statusline.sh's jq math.
func buildBlock(table map[string]otel.Pricing) string {
	models := sortedModels(table)

	pad := maxLen(models) + 3 // room for `"` + name + `":`

	var b strings.Builder
	b.WriteString("prices='{\n")
	b.WriteString("  \"input\": {\n")
	for i, m := range models {
		entry := fmt.Sprintf("%q:", m)
		comma := ","
		if i == len(models)-1 {
			comma = ""
		}
		fmt.Fprintf(&b, "    %-*s %s%s\n", pad, entry, fmtPrice(table[m].InputUSDPer1M/1000), comma)
	}
	b.WriteString("  },\n")
	b.WriteString("  \"output\": {\n")
	for i, m := range models {
		entry := fmt.Sprintf("%q:", m)
		comma := ","
		if i == len(models)-1 {
			comma = ""
		}
		fmt.Fprintf(&b, "    %-*s %s%s\n", pad, entry, fmtPrice(table[m].OutputUSDPer1M/1000), comma)
	}
	b.WriteString("  },\n")
	b.WriteString("  \"cache_read\": {\n")
	for i, m := range models {
		entry := fmt.Sprintf("%q:", m)
		comma := ","
		if i == len(models)-1 {
			comma = ""
		}
		fmt.Fprintf(&b, "    %-*s %s%s\n", pad, entry, fmtCatalogPrice(table[m].EffectiveCacheReadMultiplier()), comma)
	}
	b.WriteString("  }\n")
	b.WriteString("}'")
	return b.String()
}

// buildTypeScript produces the price table consumed by the Pi extension. The
// values stay in the catalog's native USD/1M units so the generated artifact is
// lossless and the runtime arithmetic remains easy to audit.
func buildTypeScript(table map[string]otel.Pricing) string {
	models := sortedModels(table)
	version := pricingVersion(table, models)

	var b strings.Builder
	b.WriteString("// Code generated by cmd/genprices; DO NOT EDIT.\n")
	b.WriteString("// Source: internal/router/catalog (USD per 1M tokens).\n\n")
	b.WriteString("export interface ModelPricing {\n")
	b.WriteString("\tinputUsdPerMillion: number;\n")
	b.WriteString("\toutputUsdPerMillion: number;\n")
	b.WriteString("\tcacheReadMultiplier: number;\n")
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "export const PRICING_VERSION = %q;\n\n", version)
	b.WriteString("export const MODEL_PRICING: Readonly<Record<string, ModelPricing>> = Object.freeze({\n")
	for _, model := range models {
		price := table[model]
		fmt.Fprintf(
			&b,
			"\t%q: { inputUsdPerMillion: %s, outputUsdPerMillion: %s, cacheReadMultiplier: %s },\n",
			model,
			fmtCatalogPrice(price.InputUSDPer1M),
			fmtCatalogPrice(price.OutputUSDPer1M),
			fmtCatalogPrice(price.EffectiveCacheReadMultiplier()),
		)
	}
	b.WriteString("});\n")
	return b.String()
}

func sortedModels(table map[string]otel.Pricing) []string {
	models := make([]string, 0, len(table))
	for model := range table {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func pricingVersion(table map[string]otel.Pricing, models []string) string {
	var canonical strings.Builder
	for _, model := range models {
		price := table[model]
		fmt.Fprintf(
			&canonical,
			"%s\x00%s\x00%s\x00%s\n",
			model,
			fmtCatalogPrice(price.InputUSDPer1M),
			fmtCatalogPrice(price.OutputUSDPer1M),
			fmtCatalogPrice(price.EffectiveCacheReadMultiplier()),
		)
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return fmt.Sprintf("catalog-sha256:%x", sum[:8])
}

func fmtCatalogPrice(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// splice replaces everything between the begin and end markers (exclusive) with block.
func splice(path, src, block string) (string, error) {
	start := strings.Index(src, beginMarker)
	end := strings.Index(src, endMarker)
	if start < 0 || end <= start {
		return "", fmt.Errorf("%s or %s marker missing in %s", beginMarker, endMarker, path)
	}
	afterBegin := start + len(beginMarker)
	return src[:afterBegin] + "\n" + block + "\n" + src[end:], nil
}

func maxLen(models []string) int {
	n := 0
	for _, m := range models {
		if len(m) > n {
			n = len(m)
		}
	}
	return n
}

// fmtPrice formats a USD/1k price for cc-statusline.sh's jq math. Rounds to
// 6 sig figs first so IEEE 754 artifacts (e.g. 0.071/1000 rendering as
// 0.00007099999999999999) don't leak into the generated block.
func fmtPrice(v float64) string {
	if v == 0 {
		return "0"
	}
	const sigFigs = 6
	scale := math.Pow10(sigFigs - 1 - int(math.Floor(math.Log10(math.Abs(v)))))
	rounded := math.Round(v*scale) / scale
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}
