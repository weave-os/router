package catalog

type RouterTokenScalars struct {
	Input      float64
	Output     float64
	CacheWrite float64
	CacheRead  float64
}

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
