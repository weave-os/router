# Direct DeepSeek and GPT-5.6 v0.76 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add direct DeepSeek API dispatch and produce a measured, deterministic v0.76 cluster artifact containing GPT-5.6 Sol, Terra, Luna, DeepSeek V4 Flash, and the routable DeepSeek V4 Pro target without changing v0.75's cluster geometry.

**Architecture:** The provider slice reuses the generic OpenAI-compatible adapter, adds one canonical provider identity at the composition root, and makes direct DeepSeek the first catalog binding while preserving all current fallbacks. The artifact slice consumes a reviewed measurement manifest and applies an additive overlay to v0.75; one deterministic Python builder writes every derived file and copies the centroid binary byte-for-byte. The two slices are independently reviewable and committable, and `artifacts/latest` remains v0.75.

**Tech Stack:** Go 1.25, Python 3 standard library, JSON/YAML routing artifacts, `testify`, repository `make` targets

**Spec:** `docs/superpowers/specs/2026-09-17-direct-deepseek-gpt56-v076-design.md`

## Global Constraints

- Run commands directly: this checkout has no `devenv.nix`, and the required Go, Python, and Make tools are already installed.
- Preserve the layer model: provider identity belongs in `internal/providers`, catalog data in `internal/router/catalog`, HTTP behavior in `internal/providers/openaicompat`, and concrete wiring only in `cmd/router/main.go`.
- Use the `ProviderDeepSeek` constant everywhere after introducing it; never add a bare `"deepseek"` provider value outside its defining constant or JSON/YAML artifact data.
- Keep DeepSeek native thinking mode disabled. Do not add `CapReasoning`, synthesize `thinking`, preserve cross-turn `reasoning_content`, or rewrite `tool_choice` in this change.
- Direct DeepSeek is the first binding; Makora, Together, Fireworks, OpenRouter, and Wafer bindings remain in their existing relative order.
- Keep the bare `deepseek/deepseek-v4-pro` catalog row untiered. The v0.76 routing target is `deepseek/deepseek-v4-pro-0813`.
- Copy `v0.75/centroids.bin` byte-for-byte. Do not retrain, recluster, or change embedder metadata.
- Do not edit `internal/router/cluster/artifacts/latest`; v0.76 lands as a candidate.
- Do not invent benchmark values. Task 5 requires a reviewed empirical measurement export; if it is absent, stop after Task 4 with the provider slice and generator complete.
- Do not commit credentials, raw private prompts, customer/company identifiers, or internal ticket links.
- Do not edit generated SQLC files.

---

## Slice A: Direct DeepSeek Provider

### Task 1: Define the Direct DeepSeek Provider Contract

**Files:**

- Modify: `internal/providers/provider.go`
- Modify: `internal/providers/families_test.go`
- Modify: `internal/providers/cache_ttl_test.go`
- Modify: `internal/providers/openaicompat/client.go`
- Modify: `internal/providers/openaicompat/client_test.go`

**Interfaces:**

- Produces: `ProviderDeepSeek` in the providers package with value `"deepseek"`
- Produces: `DeepSeekBaseURL` in the OpenAI-compatible adapter with value `"https://api.deepseek.com"`
- Produces: `APIKeyEnvVar(ProviderDeepSeek) == "DEEPSEEK_API_KEY"`
- Produces: `FamilyFor(ProviderDeepSeek) == FamilyOpenAICompat`
- Produces: `CacheTTLFor(ProviderDeepSeek) == 5*time.Minute`
- Consumed by: catalog bindings in Task 2 and composition-root wiring in Task 3

- [ ] **Step 1: Add failing provider-family and cache-TTL assertions**

Extend `TestFamilyForKnownProviders` in `internal/providers/families_test.go`:

```go
providers.ProviderDeepSeek: providers.FamilyOpenAICompat,
```

Extend `TestCacheTTLFor` in `internal/providers/cache_ttl_test.go`:

```go
assert.Equal(t, 5*time.Minute, providers.CacheTTLFor(providers.ProviderDeepSeek),
	"deepseek should report the short best-effort cache window")
```

`TestEveryProviderHasFamilyAndEnvVar` will also require a non-empty API-key environment variable once the provider is included in `AllProviders`.

- [ ] **Step 2: Run the focused provider tests and verify they fail to compile**

Run:

```bash
go test ./internal/providers -run 'Test(FamilyForKnownProviders|CacheTTLFor|EveryProviderHasFamilyAndEnvVar)$'
```

Expected: FAIL because `ProviderDeepSeek` is undefined.

- [ ] **Step 3: Add the provider constant and all provider-owned map entries**

In `internal/providers/provider.go`, add the constant beside the other OpenAI-compatible providers:

```go
ProviderDeepSeek = "deepseek"
```

Add these entries:

```go
// ProviderFamilies
ProviderDeepSeek: FamilyOpenAICompat,

// APIKeyEnvVars
ProviderDeepSeek: "DEEPSEEK_API_KEY",

// CacheTTL
ProviderDeepSeek: 5 * time.Minute,
```

Do not add DeepSeek to `baseURLRequiredProviders`; it has a vendor default.

- [ ] **Step 4: Add a failing base-URL contract test**

Add to `internal/providers/openaicompat/client_test.go`:

```go
func TestDeepSeekBaseURL(t *testing.T) {
	assert.Equal(t, "https://api.deepseek.com", openaicompat.DeepSeekBaseURL)
}
```

- [ ] **Step 5: Run the base-URL test and verify it fails to compile**

Run:

```bash
go test ./internal/providers/openaicompat -run '^TestDeepSeekBaseURL$'
```

Expected: FAIL because `DeepSeekBaseURL` is undefined.

- [ ] **Step 6: Add the DeepSeek base URL**

Add to the constant block in `internal/providers/openaicompat/client.go`:

```go
// DeepSeekBaseURL is DeepSeek's direct OpenAI-compatible API endpoint.
DeepSeekBaseURL = "https://api.deepseek.com"
```

- [ ] **Step 7: Run the focused tests**

Run:

```bash
go test ./internal/providers ./internal/providers/openaicompat
```

Expected: PASS.

- [ ] **Step 8: Commit the provider contract**

```bash
git add internal/providers/provider.go internal/providers/families_test.go internal/providers/cache_ttl_test.go internal/providers/openaicompat/client.go internal/providers/openaicompat/client_test.go
git commit -m "feat(providers): define direct DeepSeek provider"
```

### Task 2: Add Preferred Direct DeepSeek Catalog Bindings

**Files:**

- Modify: `internal/router/catalog/catalog.go`
- Modify: `internal/router/catalog/catalog_test.go`

**Interfaces:**

- Consumes: `ProviderDeepSeek` from Task 1
- Produces: direct bindings for Flash, bare Pro passthrough, and dated routable Pro
- Produces: catalog model-ID rewrites used by `upstreamIDsForProvider`
- Preserves: existing fallback binding order after the new direct binding

- [ ] **Step 1: Add a failing table test for direct bindings and prices**

Add to `internal/router/catalog/catalog_test.go`:

```go
func TestDeepSeekDirectBindings(t *testing.T) {
	tests := []struct {
		model          string
		upstreamID     string
		inputUSDPer1M  float64
		outputUSDPer1M float64
		cacheRead      float64
	}{
		{"deepseek/deepseek-v4-flash", "deepseek-flash", 0.14, 0.28, 0.20},
		{"deepseek/deepseek-v4-pro", "deepseek-v4-pro", 1.74, 3.48, 0.145 / 1.74},
		{"deepseek/deepseek-v4-pro-0813", "deepseek-v4-pro", 1.74, 3.48, 0.145 / 1.74},
	}

	available := map[string]struct{}{providers.ProviderDeepSeek: {}}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			binding, ok := ResolveBinding(tt.model, available)
			require.True(t, ok)
			assert.Equal(t, providers.ProviderDeepSeek, binding.Provider)
			assert.Equal(t, tt.upstreamID, binding.UpstreamID)
			assert.Equal(t, tt.inputUSDPer1M, binding.Price.InputUSDPer1M)
			assert.Equal(t, tt.outputUSDPer1M, binding.Price.OutputUSDPer1M)
			assert.InDelta(t, tt.cacheRead, binding.Price.EffectiveCacheReadMultiplier(), 1e-12)
		})
	}
}
```

- [ ] **Step 2: Add failing fallback-order tests**

Add cases proving direct wins when available and the old first available binding wins otherwise:

```go
func TestDeepSeekBindingPreferenceAndFallback(t *testing.T) {
	directAndFallback := map[string]struct{}{
		providers.ProviderDeepSeek:   {},
		providers.ProviderOpenRouter: {},
	}
	binding, ok := ResolveBinding("deepseek/deepseek-v4-flash", directAndFallback)
	require.True(t, ok)
	assert.Equal(t, providers.ProviderDeepSeek, binding.Provider)

	fallbackOnly := map[string]struct{}{providers.ProviderOpenRouter: {}}
	binding, ok = ResolveBinding("deepseek/deepseek-v4-flash", fallbackOnly)
	require.True(t, ok)
	assert.Equal(t, providers.ProviderOpenRouter, binding.Provider)

	proFallback := map[string]struct{}{providers.ProviderTogether: {}}
	binding, ok = ResolveBinding("deepseek/deepseek-v4-pro-0813", proFallback)
	require.True(t, ok)
	assert.Equal(t, providers.ProviderTogether, binding.Provider)
}
```

Also retain or add an assertion that `TierFor("deepseek/deepseek-v4-pro") == TierUnknown` and `TierFor("deepseek/deepseek-v4-pro-0813") == TierMid`.

- [ ] **Step 3: Run the focused catalog tests and verify they fail**

Run:

```bash
go test ./internal/router/catalog -run '^TestDeepSeek'
```

Expected: FAIL because no direct DeepSeek bindings exist.

- [ ] **Step 4: Prepend the direct Flash binding**

In the `deepseek/deepseek-v4-flash` catalog row, insert before Makora:

```go
{Provider: providers.ProviderDeepSeek, UpstreamID: "deepseek-flash",
	Price: Pricing{InputUSDPer1M: 0.140, OutputUSDPer1M: 0.280, CacheReadMultiplier: 0.028 / 0.140}},
```

Keep `TierLow`, `ImageInputUnsupported`, and `AgenticLow` unchanged.

- [ ] **Step 5: Prepend the direct Pro bindings without changing tiers**

Insert this binding before Together in both Pro catalog rows:

```go
{Provider: providers.ProviderDeepSeek, UpstreamID: "deepseek-v4-pro",
	Price: Pricing{InputUSDPer1M: 1.740, OutputUSDPer1M: 3.480, CacheReadMultiplier: 0.145 / 1.740}},
```

Keep the bare Pro row untiered. Keep the dated `-0813` row `TierMid`.

- [ ] **Step 6: Add DeepSeek to the catalog's canonical-provider test set**

In `TestCatalog_BindingsReferenceCanonicalProviders`, add:

```go
providers.ProviderDeepSeek: {},
```

- [ ] **Step 7: Run catalog tests**

Run:

```bash
go test ./internal/router/catalog
```

Expected: PASS.

- [ ] **Step 8: Commit the catalog bindings**

```bash
git add internal/router/catalog/catalog.go internal/router/catalog/catalog_test.go
git commit -m "feat(catalog): prefer direct DeepSeek bindings"
```

### Task 3: Wire Direct DeepSeek and Prove the Outbound Request

**Files:**

- Create: `cmd/router/deepseek_provider_test.go`
- Modify: `cmd/router/main.go`
- Modify: `internal/providers/openaicompat/client_test.go`
- Modify: `docs/CONFIGURATION.md`

**Interfaces:**

- Consumes: provider constant/base URL from Task 1 and catalog bindings from Task 2
- Produces: a registered OpenAI-compatible client keyed by `DEEPSEEK_API_KEY`
- Produces: logical-to-upstream model mapping for both direct DeepSeek models
- Preserves: generic non-thinking request preparation

- [ ] **Step 1: Add the composition model-map test**

Create `cmd/router/deepseek_provider_test.go`:

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"weave-os/router/internal/providers"
)

func TestDeepSeekUpstreamIDs(t *testing.T) {
	ids := upstreamIDsForProvider(providers.ProviderDeepSeek)
	assert.Equal(t, "deepseek-flash", ids["deepseek/deepseek-v4-flash"])
	assert.Equal(t, "deepseek-v4-pro", ids["deepseek/deepseek-v4-pro"])
	assert.Equal(t, "deepseek-v4-pro", ids["deepseek/deepseek-v4-pro-0813"])
}
```

- [ ] **Step 2: Run the helper test**

Run:

```bash
go test ./cmd/router -run '^TestDeepSeekUpstreamIDs$'
```

Expected: PASS after Task 2. This test is written before wiring so the client construction has a pinned mapping contract.

- [ ] **Step 3: Add the direct-wire characterization test**

Add a test to `internal/providers/openaicompat/client_test.go` that creates a local server, constructs a client with the DeepSeek model map, and asserts the wire request:

```go
func TestProxy_DeepSeekDirectRewritesModelAndUsesBearerAuth(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	client := openaicompat.NewClientWithModelIDMap("deepseek-key", upstream.URL, map[string]string{
		"deepseek/deepseek-v4-flash": "deepseek-flash",
	})
	prep := providers.PreparedRequest{
		Body:    []byte(`{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`),
		Headers: make(http.Header),
	}
	err := client.Proxy(context.Background(), router.Decision{Model: "deepseek/deepseek-v4-flash", Provider: providers.ProviderDeepSeek}, prep, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.NoError(t, err)
	assert.Equal(t, "/chat/completions", gotPath)
	assert.Equal(t, "Bearer deepseek-key", gotAuth)
	assert.Equal(t, "deepseek-flash", gotBody["model"])
}
```

Use the test file's existing request/response helpers where they make the test shorter without weakening these three assertions.

- [ ] **Step 4: Run the direct-wire test**

Run:

```bash
go test ./internal/providers/openaicompat -run '^TestProxy_DeepSeekDirectRewritesModelAndUsesBearerAuth$'
```

Expected: PASS because the generic adapter already implements the required behavior. This is a characterization gate before composition changes.

- [ ] **Step 5: Register DeepSeek in the composition root**

Add a block in `cmd/router/main.go` adjacent to the other OpenAI-compatible providers:

```go
{
	deepSeekBaseURL := config.GetOr("DEEPSEEK_BASE_URL", openaiCompatProvider.DeepSeekBaseURL)
	registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
		providers.ProviderDeepSeek, "DeepSeek", "DEEPSEEK_API_KEY", deepSeekBaseURL, byokOnly,
		func(key, baseURL string) providers.Client {
			return openaiCompatProvider.NewClientWithModelIDMap(
				key,
				baseURL,
				upstreamIDsForProvider(providers.ProviderDeepSeek),
				openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient),
			)
		})
}
```

Do not add a special dispatch branch: `FamilyOpenAICompat` makes the existing proxy and dispatch paths authoritative.

- [ ] **Step 6: Document deployment and BYOK configuration**

Add these rows to the provider table in `docs/CONFIGURATION.md`:

```markdown
| `DEEPSEEK_API_KEY` | *(none)* | Enables direct DeepSeek API routing for DeepSeek V4 Flash and Pro. |
| `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | Override the direct DeepSeek OpenAI-compatible endpoint. |
```

Add one sentence below the table stating that direct DeepSeek is preferred when configured and existing catalog bindings remain fallbacks.

- [ ] **Step 7: Verify provider wiring and documentation**

Run:

```bash
go test ./cmd/router -run '^TestDeepSeekUpstreamIDs$'
go test ./internal/providers/... ./internal/router/catalog/...
make check-docs
git diff --check
```

Expected: PASS.

- [ ] **Step 8: Regenerate catalog-derived price projections and inspect the result**

Run:

```bash
make generate-statusline
git diff -- install/install.sh install/cc-statusline.sh
```

Expected: only DeepSeek primary-price changes may appear. GPT-5.6 rows must not churn. If no generated file changes, leave them unstaged.

- [ ] **Step 9: Commit direct provider wiring**

```bash
git add cmd/router/main.go cmd/router/deepseek_provider_test.go internal/providers/openaicompat/client_test.go docs/CONFIGURATION.md install/install.sh install/cc-statusline.sh
git diff --cached --check
git commit -m "feat(router): wire direct DeepSeek API"
```

If the generated installer files have no diff, omit them from `git add`.

---

## Slice B: Frozen-Geometry v0.76 Candidate

### Task 4: Build a Deterministic, Fail-Closed Overlay Generator

**Files:**

- Create: `scripts/build_v076_gpt56_deepseek_overlay.py`
- Create: `scripts/test_build_v076_gpt56_deepseek_overlay.py`
- Create: `scripts/testdata/v076_overlay/parent/centroids.bin`
- Create: `scripts/testdata/v076_overlay/parent/model_registry.json`
- Create: `scripts/testdata/v076_overlay/parent/quality_means.json`
- Create: `scripts/testdata/v076_overlay/parent/model_axes.json`
- Create: `scripts/testdata/v076_overlay/parent/model_features.json`
- Create: `scripts/testdata/v076_overlay/parent/rankings.json`
- Create: `scripts/testdata/v076_overlay/parent/metadata.yaml`
- Create: `scripts/testdata/v076_overlay/measurements.json`

**Interfaces:**

- Produces CLI:
  `python3 scripts/build_v076_gpt56_deepseek_overlay.py --parent <dir> --measurements <json> --output <dir> [--overwrite]`
- Produces Python functions:
  `load_measurements(path: Path) -> dict`,
  `validate_measurements(data: dict, k: int) -> None`,
  `calibrate_cluster(observed: float, anchors: list[tuple[float, float]]) -> float`,
  `blend_rankings(quality: dict, axes: dict, knobs: dict) -> dict`, and
  `build_bundle(parent: Path, measurements: Path, output: Path, overwrite: bool) -> None`
- Consumes: v0.75-format v2 artifacts and a reviewed measurement manifest
- Produces: all seven generated artifact files, never `latest`

- [ ] **Step 1: Create minimal deterministic test fixtures**

The fixture parent uses `k=2` and three models so tests stay readable. Its measurement file must include all three operation types:

```json
{
  "schema_version": 1,
  "version": "v0.76",
  "parent": "v0.75",
  "collected_at": "2026-09-17",
  "models": {
    "gpt-5.6-luna": {
      "provider": "openai",
      "bench_column": "routerarena_gpt-5.6-luna",
      "evidence": "measured",
      "quality": [0.42, 0.57],
      "cluster_samples": [12, 9],
      "operational": {
        "input_per_1k_usd": 0.0002,
        "output_per_1k_usd": 0.0012,
        "ttft_s": 0.8,
        "tps": 90.0,
        "verbosity_tokens": null
      },
      "source": "fixture-eval-2026-09-17"
    },
    "deepseek/deepseek-v4-pro-0813": {
      "provider": "deepseek",
      "bench_column": "routerarena_deepseek/deepseek-v4-pro",
      "evidence": "same-upstream-alias",
      "source_model": "deepseek/deepseek-v4-pro",
      "operational": {
        "input_per_1k_usd": 0.00174,
        "output_per_1k_usd": 0.00348,
        "ttft_s": 1.1,
        "tps": 75.0,
        "verbosity_tokens": null
      },
      "source": "fixture-alias"
    },
    "fixture/proxy-model": {
      "provider": "openrouter",
      "bench_column": "routerarena_fixture/source-model",
      "evidence": "proxy",
      "source_model": "fixture/source-model",
      "proxy_reason": "fixture exercises explicit reviewed proxy behavior",
      "operational": {
        "input_per_1k_usd": 0.002,
        "output_per_1k_usd": 0.004,
        "ttft_s": 1.2,
        "tps": 60.0,
        "verbosity_tokens": 150
      },
      "source": "fixture-proxy-review"
    }
  },
  "remove_models": ["deepseek/deepseek-v4-pro"]
}
```

The fixture `centroids.bin` may contain a short sentinel payload because the Python builder copies rather than parses it.

- [ ] **Step 2: Write failing manifest-validation tests**

In `scripts/test_build_v076_gpt56_deepseek_overlay.py`, load the script with `importlib.util` and add tests covering:

```python
def test_rejects_quality_vector_with_wrong_cluster_count(self):
    data = self.valid_manifest()
    data["models"]["gpt-5.6-luna"]["quality"] = [0.42]
    with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*quality.*2"):
        module.validate_measurements(data, 2)

def test_rejects_non_finite_quality(self):
    data = self.valid_manifest()
    data["models"]["gpt-5.6-luna"]["quality"][0] = float("nan")
    with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*quality.*finite"):
        module.validate_measurements(data, 2)

def test_rejects_measured_entry_without_cluster_samples(self):
    data = self.valid_manifest()
    del data["models"]["gpt-5.6-luna"]["cluster_samples"]
    with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*cluster_samples"):
        module.validate_measurements(data, 2)

def test_rejects_silent_proxy(self):
    data = self.valid_manifest()
    data["models"]["gpt-5.6-luna"] = {
        "evidence": "proxy",
        "source_model": "existing/model",
        "operational": self.valid_operational(),
    }
    with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*proxy_reason"):
        module.validate_measurements(data, 2)

def test_rejects_missing_operational_axis(self):
    data = self.valid_manifest()
    del data["models"]["gpt-5.6-luna"]["operational"]["tps"]
    with self.assertRaisesRegex(ValueError, r"gpt-5.6-luna.*tps"):
        module.validate_measurements(data, 2)

def test_rejects_non_positive_price_or_speed(self):
    for field in ("input_per_1k_usd", "output_per_1k_usd", "ttft_s", "tps"):
        with self.subTest(field=field):
            data = self.valid_manifest()
            data["models"]["gpt-5.6-luna"]["operational"][field] = 0
            with self.assertRaisesRegex(ValueError, rf"gpt-5.6-luna.*{field}.*positive"):
                module.validate_measurements(data, 2)

def test_rejects_existing_output_without_overwrite(self):
    with tempfile.TemporaryDirectory() as tmp:
        output = Path(tmp) / "v0.76"
        output.mkdir()
        (output / "rankings.json").write_text("{}\n", encoding="utf-8")
        with self.assertRaisesRegex(FileExistsError, "--overwrite"):
            module.build_bundle(FIXTURE_PARENT, FIXTURE_MANIFEST, output, overwrite=False)
```

Each test must assert the specific `ValueError` message includes the model and invalid field.

- [ ] **Step 3: Run the validation tests and verify they fail**

Run:

```bash
python3 -m unittest scripts/test_build_v076_gpt56_deepseek_overlay.py -v
```

Expected: FAIL because the builder module does not exist.

- [ ] **Step 4: Implement manifest loading and validation**

Implement these exact rules with only Python's standard library:

```python
VALID_EVIDENCE = {"measured", "same-upstream-alias", "proxy"}
REQUIRED_OPERATIONAL = {"input_per_1k_usd", "output_per_1k_usd", "ttft_s", "tps", "verbosity_tokens"}

def validate_measurements(data: dict, k: int) -> None:
    if data.get("schema_version") != 1:
        raise ValueError("schema_version must be 1")
    if data.get("version") != "v0.76" or data.get("parent") != "v0.75":
        raise ValueError("manifest must describe v0.75 -> v0.76")
    models = data.get("models")
    if not isinstance(models, dict) or not models:
        raise ValueError("models must be a non-empty object")
    remove_models = data.get("remove_models")
    if not isinstance(remove_models, list) or len(remove_models) != len(set(remove_models)):
        raise ValueError("remove_models must be a duplicate-free list")
```

Continue by iterating model names in sorted order. For `measured`, require `quality` and `cluster_samples`, both length `k`, with every quality finite and every sample count a positive integer. For `same-upstream-alias` and `proxy`, require `source_model`; copy quality from the unmodified parent snapshot only after confirming that source exists in every cluster. Allow `source_model == destination model` for an existing row whose quality is retained while operational axes are refreshed. `proxy` additionally requires a non-empty `proxy_reason`. Require every operational key; prices, TTFT, and throughput must be finite and positive, while verbosity may be null or finite and non-negative. Reject a model that is both added and removed. Never infer an evidence type.

- [ ] **Step 5: Write failing calibration tests**

Add tests for exact interpolation and clamping:

```python
def test_calibrate_cluster_interpolates_between_sorted_anchors(self):
    self.assertAlmostEqual(
        module.calibrate_cluster(0.60, [(0.50, 0.40), (0.70, 0.80)]),
        0.60,
    )

def test_calibrate_cluster_clamps_below_and_above_anchor_range(self):
    anchors = [(0.50, 0.40), (0.70, 0.80)]
    self.assertEqual(module.calibrate_cluster(0.10, anchors), 0.40)
    self.assertEqual(module.calibrate_cluster(0.90, anchors), 0.80)
```

Also reject fewer than two distinct observed anchor scores.

- [ ] **Step 6: Implement monotonic piecewise-linear calibration**

Sort anchors by observed score, collapse duplicate observed scores by averaging their target values, enforce a non-decreasing target sequence with a cumulative maximum, interpolate between neighbors, and clamp outside the observed range. Finally cap at the largest target anchor. Keep calibration as a pure function; the production manifest stores its reviewed output rather than recalibrating from private raw observations.

- [ ] **Step 7: Write failing parent-parity ranking tests**

The fixture's current `rankings.json` must be generated from its quality, axes, and default knobs. Add:

```python
def test_blend_rankings_reproduces_parent_fixture(self):
    quality = load_json(FIXTURE_PARENT / "quality_means.json")["quality_means"]
    axes = load_json(FIXTURE_PARENT / "model_axes.json")["axes"]
    rankings_file = load_json(FIXTURE_PARENT / "rankings.json")
    self.assertEqual(
        module.blend_rankings(quality, axes, rankings_file["meta"]),
        rankings_file["rankings"],
    )
```

Use the parent `rankings.json.meta` values as the historical projection inputs: they carry alpha, speed weight, output-cost ratio, expected output tokens, and per-model verbosity. Add a second test against the real v0.75 files that fails if the reconstructed ranking differs from the committed parent by more than `1e-6`; this is the parity gate before the builder can generate v0.76.

- [ ] **Step 8: Implement runtime-equivalent ranking generation**

Mirror `Scorer.blendScoresV2` for one cluster at a time:

```python
cost = input_per_1k + output_cost_ratio * output_per_1k * verbosity_factor
speed = ttft_s + expected_output_tokens / tps
q_norm = (q - q_min) / (q_max - q_min) if q_max > q_min else 0.0
c_norm = (cost - c_min) / (c_max - c_min) if c_max > c_min else 0.0
s_norm = (speed - s_min) / (s_max - s_min) if timed_range_exists else None
score = alpha * q_norm + (1 - alpha - speed_weight) * (1 - c_norm)
if timed_range_exists:
    score += speed_weight * (1 - (s_norm if s_norm is not None else 1.0))
```

When there is no timing differentiation, redistribute speed weight using the same `wQ/(wQ+wC)` and `wC/(wQ+wC)` rule as Go. Use a `float32` helper based on `struct.pack("<f", value)`/`struct.unpack` at the same boundaries as the runtime. Sort model keys for deterministic tie order and JSON output.

- [ ] **Step 9: Write failing end-to-end builder tests**

Add assertions that a generated fixture bundle:

- has byte-identical centroids;
- removes bare Pro and adds dated Pro;
- adds the measured GPT row;
- has the same model set in registry, every quality row, axes, and features;
- has `psi_probe[i] == quality_means[str(i)][model]`;
- records `version=v0.76`, `parent=v0.75`, and `status=candidate`;
- leaves no `latest` file;
- produces byte-identical files on two builds;
- refuses overwrite without `--overwrite`.

- [ ] **Step 10: Implement bundle construction and deterministic serialization**

Apply each validated entry to all owning projections in one in-memory transaction:

- `model_registry.json`: remove retired rows; upsert one row with the manifest's `provider`, `bench_column`, and proxy fields; sort by model ID.
- `quality_means.json`: remove retired columns from every cluster; write the measured or copied value into all `k` rows.
- `model_axes.json`: set the five operational fields exactly as supplied by the manifest.
- `model_features.json`: set `models[model].psi_probe` from the ordered quality cells and `models[model].operational` from the same axes object; update `meta.k`, `meta.n_models`, `meta.roster_version`, and `meta.comment`.
- `rankings.json`: retain the parent's historical `meta`, update its roster/cost/axis provenance fields and generator identity, and replace `rankings` with the runtime-equivalent blend over the complete new roster.

After projection, assert all five model-indexed structures have exactly the registry model set and that each feature `psi_probe` cell equals its `quality_means` cell before writing anything.

Use `json.dumps(payload, indent=2, sort_keys=True, allow_nan=False) + "\n"` for JSON artifacts. Copy centroids with `shutil.copyfile`. Do not add a YAML dependency merely for this generator. Read the parent metadata as text and implement `replace_top_level_yaml_block(text, key, replacement)` that recognizes only column-zero keys, replaces the complete block up to the next column-zero key, and errors when a required key is missing or duplicated. Test this helper separately. Preserve the parent `training`, `embedder`, `cache_config`, and all unknown blocks byte-for-byte; replace only `version`, `parent`, `status`, `deployed_providers`, `deployed_models`, `cost_per_1k_input_usd`, and `changelog`, then append a deterministic `overlay_evidence` block.

The output directory may already exist without `--overwrite` only when it contains exactly `inputs/model_measurements.json` and that file resolves to the supplied `--measurements` path. This supports the review-first production layout in Task 5. Any generated artifact or unrelated file makes the command fail closed. Copy the validated manifest into `inputs/model_measurements.json` when the source is outside the output tree.

Write output through a temporary sibling directory, include the preserved `inputs/` directory, validate the completed bundle, and rename only after every file validates, so a failed build cannot leave a partial bundle. When replacing an input-only output directory, rename it to a sibling backup first and restore it if final installation fails; do not delete the only reviewed manifest before the atomic swap succeeds.

- [ ] **Step 11: Run generator tests and formatting checks**

Run:

```bash
python3 -m unittest scripts/test_build_v076_gpt56_deepseek_overlay.py -v
python3 -m py_compile scripts/build_v076_gpt56_deepseek_overlay.py scripts/test_build_v076_gpt56_deepseek_overlay.py
git diff --check
```

Expected: PASS.

- [ ] **Step 12: Commit the deterministic generator**

```bash
git add scripts/build_v076_gpt56_deepseek_overlay.py scripts/test_build_v076_gpt56_deepseek_overlay.py scripts/testdata/v076_overlay
git commit -m "feat(cluster): add deterministic v0.76 overlay builder"
```

### Task 5: Import Reviewed Measurements and Generate v0.76

**Files:**

- Create: `internal/router/cluster/artifacts/v0.76/inputs/model_measurements.json`
- Generate: `internal/router/cluster/artifacts/v0.76/centroids.bin`
- Generate: `internal/router/cluster/artifacts/v0.76/model_registry.json`
- Generate: `internal/router/cluster/artifacts/v0.76/quality_means.json`
- Generate: `internal/router/cluster/artifacts/v0.76/model_axes.json`
- Generate: `internal/router/cluster/artifacts/v0.76/model_features.json`
- Generate: `internal/router/cluster/artifacts/v0.76/rankings.json`
- Generate: `internal/router/cluster/artifacts/v0.76/metadata.yaml`

**Interfaces:**

- Consumes: reviewed measurement export for the three GPT-5.6 models and direct-provider operational measurements for both DeepSeek models
- Consumes: builder from Task 4 and parent bundle v0.75
- Produces: complete candidate v0.76 bundle
- Hard gate: empirical values cannot be supplied by code or inferred from model names

- [ ] **Step 1: Verify the reviewed measurement export exists before changing artifacts**

The reviewer must provide the production manifest at:

```text
internal/router/cluster/artifacts/v0.76/inputs/model_measurements.json
```

It must contain exactly these additions/alias operations:

```text
gpt-5.6-luna                     evidence=measured
gpt-5.6-terra                    evidence=measured
gpt-5.6-sol                      evidence=measured
deepseek/deepseek-v4-flash       evidence=same-upstream-alias (source is the existing Flash row; refresh operational axes only)
deepseek/deepseek-v4-pro-0813    evidence=same-upstream-alias or measured
remove: deepseek/deepseek-v4-pro
```

If Flash is instead supplied as `measured`, it must include a newly reviewed sixteen-cell quality vector and sixteen positive sample counts; the validator must not accept `measured` as a label over retained cells.

Run the builder in validation-only mode:

```bash
python3 scripts/build_v076_gpt56_deepseek_overlay.py \
  --parent internal/router/cluster/artifacts/v0.75 \
  --measurements internal/router/cluster/artifacts/v0.76/inputs/model_measurements.json \
  --output internal/router/cluster/artifacts/v0.76 \
  --validate-only
```

Expected: `validated v0.76 measurements: 5 entries, k=16`.

If the file is absent or validation fails, stop here. Report the missing/invalid evidence; do not create v0.76 with guessed numbers.

- [ ] **Step 2: Review measurement provenance before generation**

Use `jq` to inspect only aggregate, public-safe fields:

```bash
jq '{schema_version,version,parent,collected_at,models:(.models|to_entries|map({model:.key,evidence:.value.evidence,source:.value.source,samples:.value.cluster_samples}))}' \
  internal/router/cluster/artifacts/v0.76/inputs/model_measurements.json
```

Confirm every measured GPT model has sixteen positive sample counts, all source IDs are non-sensitive, and no raw prompts or credentials are present.

- [ ] **Step 3: Generate the production bundle**

Run:

```bash
python3 scripts/build_v076_gpt56_deepseek_overlay.py \
  --parent internal/router/cluster/artifacts/v0.75 \
  --measurements internal/router/cluster/artifacts/v0.76/inputs/model_measurements.json \
  --output internal/router/cluster/artifacts/v0.76
```

Expected: seven generated artifact files plus the preserved `inputs/` directory; no `latest` file.

- [ ] **Step 4: Prove centroid identity**

Run:

```bash
cmp internal/router/cluster/artifacts/v0.75/centroids.bin internal/router/cluster/artifacts/v0.76/centroids.bin
sha256sum internal/router/cluster/artifacts/v0.75/centroids.bin internal/router/cluster/artifacts/v0.76/centroids.bin
```

Expected: `cmp` exits 0 and both hashes are identical.

- [ ] **Step 5: Prove roster membership and retirement**

Run:

```bash
jq -r '.deployed_models[].model' internal/router/cluster/artifacts/v0.76/model_registry.json
```

Expected: includes Sol, Terra, Luna, Flash, and dated Pro; excludes bare Pro; all unchanged v0.75 models remain.

- [ ] **Step 6: Prove deterministic regeneration**

Generate into a temporary directory and compare:

```bash
tmp_dir=$(mktemp -d)
python3 scripts/build_v076_gpt56_deepseek_overlay.py \
  --parent internal/router/cluster/artifacts/v0.75 \
  --measurements internal/router/cluster/artifacts/v0.76/inputs/model_measurements.json \
  --output "$tmp_dir/v0.76"
diff -ru --exclude inputs internal/router/cluster/artifacts/v0.76 "$tmp_dir/v0.76"
```

Expected: no diff. Remove only the explicit temporary directory after recording success.

- [ ] **Step 7: Commit the candidate artifact**

```bash
git add internal/router/cluster/artifacts/v0.76
git diff --cached --check
git commit -m "feat(cluster): add measured GPT-5.6 v0.76 candidate"
```

Confirm `internal/router/cluster/artifacts/latest` is not staged.

### Task 6: Add v0.76 Load, Equivalence, and Provider-Fallback Gates

**Files:**

- Modify: `internal/router/cluster/artifacts_test.go`
- Modify: `internal/router/cluster/model_features_test.go`
- Modify: `internal/router/cluster/scorer_test.go`
- Modify: `cmd/routing-report/main.go`
- Modify: `cmd/routing-report/main_test.go`

**Interfaces:**

- Consumes: generated v0.76 bundle from Task 5
- Produces: permanent CI gates for artifact completeness, feature equivalence, routing eligibility, direct-provider preference, and reports

- [ ] **Step 1: Generalize feature equivalence to every modern shipped bundle**

Replace the hard-coded `const version = "v0.65"` test with a table containing at least v0.75 and v0.76:

```go
func TestFeaturesMatchQualityMeans(t *testing.T) {
	for _, version := range []string{"v0.75", "v0.76"} {
		t.Run(version, func(t *testing.T) {
			assertFeaturesMatchQualityMeans(t, version)
		})
	}
}
```

Move the existing body into `assertFeaturesMatchQualityMeans(t *testing.T, version string)` and preserve exact-value comparisons.

- [ ] **Step 2: Add a v0.76 artifact completeness test**

Add to `internal/router/cluster/artifacts_test.go`:

```go
func TestV076RosterIsCompleteAndFrozenFromV075(t *testing.T) {
	v75, err := LoadBundle("v0.75")
	require.NoError(t, err)
	v76, err := LoadBundle("v0.76")
	require.NoError(t, err)

	assert.Equal(t, v75.Centroids.K, v76.Centroids.K)
	assert.Equal(t, v75.Centroids.Dim, v76.Centroids.Dim)
	assert.Equal(t, 16, v76.Centroids.K)
	models := v76.Registry.Models()
	assert.Contains(t, models, "gpt-5.6-sol")
	assert.Contains(t, models, "gpt-5.6-terra")
	assert.Contains(t, models, "gpt-5.6-luna")
	assert.Contains(t, models, "deepseek/deepseek-v4-flash")
	assert.Contains(t, models, "deepseek/deepseek-v4-pro-0813")
	assert.NotContains(t, models, "deepseek/deepseek-v4-pro")

	for _, model := range models {
		_, ok := v76.ModelAxes[model]
		require.Truef(t, ok, "missing axes for %s", model)
		for clusterID := 0; clusterID < v76.Centroids.K; clusterID++ {
			value, ok := v76.QualityMeans[clusterID][model]
			require.Truef(t, ok, "missing quality for %s cluster %d", model, clusterID)
			require.False(t, math.IsNaN(float64(value)))
			require.False(t, math.IsInf(float64(value), 0))
		}
	}

	for _, model := range []string{
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-pro-0813",
	} {
		price, ok := catalog.PrimaryPriceFor(model)
		require.Truef(t, ok, "missing primary catalog price for %s", model)
		axes := v76.ModelAxes[model]
		require.NotNil(t, axes.InputPer1KUSD)
		require.NotNil(t, axes.OutputPer1KUSD)
		assert.InDelta(t, price.InputUSDPer1M/1000, *axes.InputPer1KUSD, 1e-12)
		assert.InDelta(t, price.OutputUSDPer1M/1000, *axes.OutputPer1KUSD, 1e-12)
	}
}
```

Import `internal/router/catalog` for the pricing assertions. Use a byte-level `os.ReadFile` comparison in the same test or a sibling test to assert the two `centroids.bin` files are equal.

- [ ] **Step 3: Run the new artifact tests**

Run:

```bash
go test ./internal/router/cluster -run 'Test(V076RosterIsCompleteAndFrozenFromV075|FeaturesMatchQualityMeans)$'
```

Expected: PASS.

- [ ] **Step 4: Add scorer binding tests for v0.76**

Add this helper and test to `internal/router/cluster/scorer_test.go`:

```go
func providerForModel(entries []DeployedEntry, model string) string {
	for _, entry := range entries {
		if entry.Model == model {
			return entry.Provider
		}
	}
	return ""
}

func TestV076PrefersDirectDeepSeekAndRetainsFallbacks(t *testing.T) {
	bundle, err := LoadBundle("v0.76")
	require.NoError(t, err)
	embed := &fakeEmbedder{id: bundle.EmbedderID(), dim: bundle.Centroids.Dim}

	direct, err := NewScorer(bundle, DefaultConfig(), embed, map[string]struct{}{
		providers.ProviderDeepSeek:  {},
		providers.ProviderMakora:    {},
		providers.ProviderTogether:  {},
	})
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderDeepSeek, providerForModel(direct.DeployedModels(), "deepseek/deepseek-v4-flash"))
	assert.Equal(t, providers.ProviderDeepSeek, providerForModel(direct.DeployedModels(), "deepseek/deepseek-v4-pro-0813"))

	fallback, err := NewScorer(bundle, DefaultConfig(), embed, map[string]struct{}{
		providers.ProviderMakora:   {},
		providers.ProviderTogether: {},
	})
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderMakora, providerForModel(fallback.DeployedModels(), "deepseek/deepseek-v4-flash"))
	assert.Equal(t, providers.ProviderTogether, providerForModel(fallback.DeployedModels(), "deepseek/deepseek-v4-pro-0813"))
}
```

Use the package's existing static/fake embedder and `DeployedModels()` accessor; do not duplicate scorer internals.

- [ ] **Step 5: Update routing-report premium classification**

Add Sol and Terra to `premiumModels`; Luna remains non-premium:

```go
"gpt-5.6-sol": {}, "gpt-5.6-terra": {},
```

Add this unit assertion to `cmd/routing-report/main_test.go` so later roster changes do not silently skew the summary:

```go
func TestGPT56PremiumClassification(t *testing.T) {
	_, sol := premiumModels["gpt-5.6-sol"]
	_, terra := premiumModels["gpt-5.6-terra"]
	_, luna := premiumModels["gpt-5.6-luna"]
	if !sol || !terra || luna {
		t.Fatalf("premium classification: sol=%v terra=%v luna=%v", sol, terra, luna)
	}
}
```

- [ ] **Step 6: Run cluster and report tests**

Run:

```bash
go test ./internal/router/cluster
go test -tags no_onnx ./cmd/routing-report
```

Expected: PASS.

- [ ] **Step 7: Generate and review the routing diff**

Run:

```bash
go run -tags no_onnx ./cmd/routing-report \
  --target v0.76 \
  --baseline v0.75 \
  --out /tmp/v076-routing-report.md \
  --per-prompt-json /tmp/v076-routing-decisions.json
```

Review the report for unexplained domination, register regressions, and new-model shares. Do not commit `/tmp` output. If a measurement/proxy error is found, correct the reviewed manifest and regenerate through Task 5; never hand-edit generated artifacts.

- [ ] **Step 8: Commit permanent artifact gates**

```bash
git add internal/router/cluster/artifacts_test.go internal/router/cluster/model_features_test.go internal/router/cluster/scorer_test.go cmd/routing-report/main.go cmd/routing-report/main_test.go
git commit -m "test(cluster): gate v0.76 artifact behavior"
```

### Task 7: Run Repository and Live-Capability Release Gates

**Files:**

- Modify only if generation produces expected tracked changes: `install/install.sh`, `install/cc-statusline.sh`, `docs/POLICY_INFERENCE.md`, `docs/POLICY_INFERENCE.json`
- Do not modify: `internal/router/cluster/artifacts/latest`

**Interfaces:**

- Consumes: completed provider and artifact slices
- Produces: verification evidence and a candidate-ready branch

- [ ] **Step 1: Run generated-file and architecture checks**

Run:

```bash
make generate
make inference-boundary
make check-agent-guides
make check-docs
git diff --check
```

Expected: PASS. Inspect and stage only deterministic generated changes caused by catalog data.

- [ ] **Step 2: Run focused package tests**

Run:

```bash
go test ./internal/providers/... ./internal/router/catalog/... ./internal/router/cluster ./cmd/router
go test -tags no_onnx ./cmd/routing-report
python3 -m unittest scripts/test_build_v076_gpt56_deepseek_overlay.py -v
```

Expected: PASS.

- [ ] **Step 3: Run the repository precommit gate**

Run:

```bash
make precommit
```

Expected: PASS. If an unrelated existing failure appears, record the exact command and error separately; do not weaken a test or widen this change to repair unrelated work.

- [ ] **Step 4: Exercise direct DeepSeek against the local mock conformance test**

Run:

```bash
go test ./internal/providers/openaicompat -run '^TestProxy_DeepSeekDirectRewritesModelAndUsesBearerAuth$' -count=1 -v
```

Expected: PASS with path, auth scheme, and model rewrite assertions.

- [ ] **Step 5: Run live DeepSeek smoke checks only when credentials are supplied**

With `DEEPSEEK_API_KEY` configured on the router and `ROUTER_BASE_URL` plus `ROUTER_API_KEY` available in the caller's approved environment, run these requests through the public OpenAI-compatible route:

```bash
curl --fail-with-body --silent --show-error \
  -H "Authorization: Bearer ${ROUTER_API_KEY}" \
  -H 'Content-Type: application/json' \
  -H 'x-weave-force-model: deepseek/deepseek-v4-flash' \
  --data '{"model":"router","messages":[{"role":"user","content":"Reply with exactly: flash-ok"}],"stream":false}' \
  "${ROUTER_BASE_URL}/v1/chat/completions"

curl --fail-with-body --silent --show-error --no-buffer \
  -H "Authorization: Bearer ${ROUTER_API_KEY}" \
  -H 'Content-Type: application/json' \
  -H 'x-weave-force-model: deepseek/deepseek-v4-pro-0813' \
  --data '{"model":"router","messages":[{"role":"user","content":"Reply with exactly: pro-ok"}],"stream":true}' \
  "${ROUTER_BASE_URL}/v1/chat/completions"

curl --fail-with-body --silent --show-error --no-buffer \
  -H "Authorization: Bearer ${ROUTER_API_KEY}" \
  -H 'Content-Type: application/json' \
  -H 'x-weave-force-model: deepseek/deepseek-v4-pro-0813' \
  --data '{"model":"router","messages":[{"role":"user","content":"Use echo_text with text tool-ok."}],"tools":[{"type":"function","function":{"name":"echo_text","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}}}],"tool_choice":{"type":"function","function":{"name":"echo_text"}},"stream":true}' \
  "${ROUTER_BASE_URL}/v1/chat/completions"
```

Capture HTTP success, a terminal `[DONE]` for each stream, the expected canonical-model decision in correlated router logs, and the `echo_text` tool-call arguments. Search the correlated log records for secret-bearing fields and confirm none contain `DEEPSEEK_API_KEY` or `ROUTER_API_KEY` values.

Do not run credentialed inference merely because an environment variable exists; execution requires the user's authorization because it incurs external cost. If authorization or credentials are unavailable, record the live gate as not run and keep v0.76 `status: candidate`.

- [ ] **Step 6: Confirm the default artifact was not promoted**

Run:

```bash
test "$(tr -d '\n' < internal/router/cluster/artifacts/latest)" = "v0.75"
git diff -- internal/router/cluster/artifacts/latest
```

Expected: test exits 0 and there is no diff.

- [ ] **Step 7: Inspect final scope and commit generated projections if needed**

Run:

```bash
git status --short
git diff --stat
git diff --check
```

If `make generate` changed expected tracked projections, commit only those files:

```bash
git add install/install.sh install/cc-statusline.sh docs/POLICY_INFERENCE.md docs/POLICY_INFERENCE.json
git diff --cached --check
git commit -m "chore: refresh generated model projections"
```

Omit unchanged paths from `git add`. The final worktree must contain no uncommitted task changes and must preserve unrelated user files.

- [ ] **Step 8: Prepare the candidate review evidence**

Report:

- commits produced by Tasks 1–7;
- exact tests and generation commands run;
- v0.75/v0.76 centroid SHA-256 values;
- measurement evidence types and sample-count ranges, without private prompts;
- routing-report distribution changes and any consciously accepted proxy;
- live DeepSeek smoke status;
- confirmation that `latest` remains v0.75.

Do not propose promotion until the separate live bake-off gate in the design spec is complete.
