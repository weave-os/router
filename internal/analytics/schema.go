package analytics

// SchemaVersion identifies the export contract. Additive field changes keep
// the version; a removal or a semantic change bumps it.
const SchemaVersion = "3"

// Field documents one exported column so a consumer can generate warehouse DDL
// without reading prose docs.
type Field struct {
	Name string `json:"name"`
	// Type is a warehouse-neutral type name: string, timestamp, integer,
	// float, boolean, or string[].
	Type        string `json:"type"`
	Nullable    bool   `json:"nullable"`
	Description string `json:"description"`
}

// Schema returns the field dictionary for Decision, in wire order.
// schema_test asserts it matches Decision's JSON tags exactly.
func Schema() []Field {
	return []Field{
		{"id", "string", false, "Unique id of this routing decision row."},
		{"recorded_at", "timestamp", false, "Ingest time. The export is ordered and paged on this, not on requested_at."},
		{"requested_at", "timestamp", false, "Event time of the routed turn. Can move backwards within a page."},
		{"request_id", "string", false, "Router request id. One request id can span several rows (retry, failover)."},
		{"trace_id", "string", false, "Trace id correlating this decision with emitted spans."},
		{"session_id", "string", true, "Client session id, when the client supplied one."},
		{"device_id", "string", true, "Client device id, when the client supplied one."},
		{"client_app", "string", true, "Calling application as reported by the client."},
		{"turn_type", "string", true, "Turn classification: main_loop, tool_result, probe, title_gen, compaction, classifier, sub_agent_dispatch. Filter on main_loop to count user-visible turns."},
		{"user_id", "string", true, "Router-assigned end-user id, stable within the installation."},
		{"user_email", "string", true, "End-user email as supplied by the client."},
		{"user_account_uuid", "string", true, "End-user account uuid as supplied by the client."},
		{"requested_model", "string", true, "Model the caller asked for."},
		{"decision_model", "string", true, "Model the router served."},
		{"decision_provider", "string", true, "Upstream provider that served the turn."},
		{"candidate_models", "string[]", true, "Models the router considered for this turn."},
		{"chosen_score", "float", true, "Score of the served model within the candidate set. Comparable within a row, not across rows."},
		{"decision_reason", "string", true, "Free-form diagnostic prose explaining the decision. NOT a stable enum: the format changes between router versions, so do not parse it."},
		{"sticky_hit", "boolean", false, "True when the turn reused a session-sticky decision instead of scoring fresh."},
		{"failover_used", "boolean", false, "True when the first-choice upstream failed and another served the turn."},
		{"cross_format", "boolean", false, "True when the request was translated between API formats (e.g. Anthropic to OpenAI)."},
		{"estimated_input_tokens", "integer", true, "Input tokens estimated at decision time, before the upstream reported actuals."},
		{"input_tokens", "integer", true, "Input tokens reported by the upstream."},
		{"output_tokens", "integer", true, "Output tokens reported by the upstream."},
		{"cache_creation_tokens", "integer", true, "Tokens written to the upstream prompt cache."},
		{"cache_read_tokens", "integer", true, "Tokens served from the upstream prompt cache."},
		{"subscription_served", "boolean", false, "True when the turn ran on the caller's own Claude/Codex subscription. Its quota already paid for the turn, so the actual_* costs are 0 while the token counts stay real."},
		{"actual_input_cost_usd", "float", true, "Input cost of the model that actually served the turn. 0 when subscription_served."},
		{"actual_output_cost_usd", "float", true, "Output cost of the model that actually served the turn. 0 when subscription_served."},
		{"route_latency_ms", "integer", true, "Time spent choosing a model."},
		{"upstream_latency_ms", "integer", true, "Time spent waiting on the upstream provider."},
		{"total_latency_ms", "integer", true, "End-to-end time for the action."},
		{"ttft_ms", "integer", true, "Time to first token on a streamed response."},
		{"upstream_status_code", "integer", true, "HTTP status returned by the upstream provider."},
		{"upstream_finish_reason", "string", true, "Finish reason reported by the upstream provider."},
		{"stop_reason", "string", true, "Normalized stop reason for the turn."},
		{"tool_use_blocks", "integer", true, "Count of tool-use blocks in the response."},
		{"invalid_tool_args_blocks", "integer", true, "Count of tool-use blocks whose arguments failed to parse."},
	}
}
