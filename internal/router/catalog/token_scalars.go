package catalog

// RouterTokenScalars estimates how many tokens a baseline model would have
// needed per token the served model actually used, one ratio per token kind.
// It exists because pricing one model's observed token counts at another
// model's rates assumes both are equally verbose, and they are not.
//
// Each value is tokens(baseline) / tokens(served), so it multiplies the served
// model's counts to yield the baseline's estimated counts. Above 1.0 means the
// baseline is the more verbose of the pair, which makes the counterfactual
// dearer and the reported savings larger, since consumers report comparison
// cost minus actual cost. Inverting this direction breaks no test: reciprocal
// cells stay self-consistent.
type RouterTokenScalars struct {
	Input      float64
	Output     float64
	CacheWrite float64
	CacheRead  float64
}

// routerTokenScalarTable maps served model -> baseline model -> ratios. A pair
// absent from the table means unit scalars, so models outside the measured
// window keep the equal-token-count behaviour rather than dropping out of the
// comparison.
//
// Nothing in the router reads this; the only consumer is WorkWeave's router
// dashboard, which scripts/sync_router_pricing.py copies this file into
// verbatim, rewriting just the package clause. Keep the file free of imports
// and functions so that stays true. Keys are raw IDs to match Models itself,
// which the copy cannot borrow a key type from.
var routerTokenScalarTable = map[string]map[string]RouterTokenScalars{
	"gpt-5.6-luna": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"claude-opus-5": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"claude-sonnet-5": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"deepseek/deepseek-v4-flash": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"deepseek/deepseek-v4.1-flash": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"gpt-6-astra": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"z-ai/glm-5.3-flash": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"claude-fable-5-1": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"gpt-5.6-sol": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"grok-4.6": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"grok-4.7": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
	"z-ai/glm-5.3": {
		"gpt-5.6-luna":     {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-terra":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"gpt-5.6-sol":      {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-haiku-4-5": {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-sonnet-5":  {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
		"claude-opus-5":    {Input: 1, Output: 1, CacheWrite: 1, CacheRead: 1},
	},
}
