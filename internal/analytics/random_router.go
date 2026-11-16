package analytics

func randomBenchmarkSchemaFields() []Field {
	return []Field{
		{"api_key_id", "string", true, "Identifier of the authenticated installation-scoped Router key; never its secret."},
		{"propensity", "float", true, "Probability assigned to the selected model by its routing policy."},
		{"planner_outcome", "string", true, "Cache planner outcome; null when no planner evidence was recorded."},
		{"inference_attempt_count", "integer", true, "Recorded inference attempts for a strict random benchmark request, including failures and auxiliary operations."},
		{"inference_attempt_models", "string[]", true, "Models recorded on strict random benchmark inference attempts, in operation order."},
	}
}
