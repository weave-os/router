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
		{"rollout_id", "string", true, "Client-supplied rollout identifier used to join benchmark tasks and rewards."},
		{"device_id", "string", true, "Client device id, when the client supplied one."},
		{"client_app", "string", true, "Calling application as reported by the client."},
		{"turn_type", "string", true, "Turn classification: main_loop, tool_result, probe, title_gen, compaction, classifier, sub_agent_dispatch. Filter on main_loop to count user-visible turns."},
		{"user_id", "string", true, "Router-assigned end-user id, stable within the installation."},
		{"user_email", "string", true, "End-user email as supplied by the client."},
		{"user_account_uuid", "string", true, "End-user account uuid as supplied by the client."},
		{"requested_model", "string", true, "Model the caller asked for."},
		{"decision_model", "string", true, "Model the router served."},
		{"decision_provider", "string", true, "Upstream provider that served the turn."},
		{"route_id", "string", true, "Identifier of the routing decision path."},
		{"routing_strategy", "string", true, "Routing strategy that selected the served model."},
		{"policy_route_key", "string", true, "Stable route key emitted by the active routing policy."},
		{"cluster_router_version", "string", true, "Version of the cluster router that scored the turn."},
		{"candidate_models", "string[]", true, "Models the router considered for this turn."},
		{"chosen_score", "float", true, "Score of the served model within the candidate set. Comparable within a row, not across rows."},
		{"decision_reason", "string", true, "Free-form diagnostic prose explaining the decision. NOT a stable enum: the format changes between router versions, so do not parse it."},
		{"blind_experiment_arm", "string", true, "Effective blind experiment arm: router_on or passthrough. Null outside an active experiment."},
		{"blind_experiment_assignment_source", "string", true, "Whether the effective experiment arm came from automatic allocation or a manual override. Null outside an active experiment."},
		{"blind_experiment_subject_key", "string", true, "Canonical subject key used for the experiment assignment. Null outside an active experiment."},
		{"cohort_experiment_id", "string", true, "Frozen cohort experiment identifier; null on legacy installations."},
		{"cohort_group_id", "integer", true, "Fixed group number for the linked roster member."},
		{"cohort_phase_index", "integer", true, "Scheduled phase at request time."},
		{"cohort_revision", "integer", true, "Immutable schedule revision used at request time."},
		{"cohort_scheduled_arm", "string", true, "Assigned treatment before an operator override."},
		{"cohort_treatment_applied", "boolean", true, "Whether the intended treatment actually controlled model selection; false on an overridden or bypassed turn."},
		{"cohort_bypass_reason", "string", true, "Why scheduled treatment did not apply (including unassigned or disabled identities)."},
		{"policy_pin_requested", "boolean", true, "True when the caller sent an x-weave-policy-pin header. Null when no pin was requested."},
		{"policy_pin_honoured", "boolean", true, "True when the turn was served by exactly the pinned policy artifact and roster. False when the pin was ignored (installation not authorized) or unservable. Null when no pin was requested."},
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
		{"subscriber_plan", "string", true, "Router subscription plan that admitted the turn."},
		{"entitlement_version", "integer", true, "Monotonic subscriber entitlement version used for admission."},
		{"capacity_source", "string", true, "Capacity that funded the turn: included_router, linked_claude, linked_codex, prepaid, or billing_override."},
		{"retail_usage_usd_micros", "integer", true, "Retail value of the served model usage in USD micros, independent of funding source."},
		{"included_usage_usd_micros", "integer", true, "Retail usage funded by included Router allowance; null for other capacity sources."},
		{"linked_usage_usd_micros", "integer", true, "Retail usage served by a linked Claude or Codex subscription; null for other capacity sources."},
		{"prepaid_usage_usd_micros", "integer", true, "Retail usage funded by subscriber prepaid balance; null for other capacity sources."},
		{"settlement_failed", "boolean", true, "Whether included-allowance or prepaid settlement failed after serving. Null when no Router settlement applies."},
		{"serving_profile_id", "string", true, "Server-owned Router profile used to serve the turn."},
		{"serving_profile_version", "string", true, "Immutable revision of the server-owned Router profile."},
		{"serving_release_id", "string", true, "Verified serving release identifier."},
		{"serving_binding_id", "string", true, "Verified serving binding identifier."},
		{"boost_optimizer_version", "string", true, "Boost source optimizer version used for selection."},
		{"policy_artifact_id", "string", true, "Routing policy artifact identifier."},
		{"policy_artifact_sha256", "string", true, "SHA-256 digest of the routing policy artifact."},
		{"roster_version", "string", true, "Immutable routing roster version."},
		{"selection_policy_release_id", "string", true, "Selection-policy release identifier."},
		{"selection_policy_sha256", "string", true, "SHA-256 digest of the selection policy."},
		{"client_git_head_sha", "string", true, "Client-reported HEAD sha of the tree a trial-mode session started from, abbreviated as the client printed it (compare by prefix). Stamped on the session's first turn only; null elsewhere."},
		{"client_git_branch", "string", true, "Client-reported branch of the tree a trial-mode session started from. First turn only; null elsewhere."},
		{"client_git_dirty", "boolean", true, "True when the client-reported starting tree had uncommitted changes. Null when not captured, so null and false are distinct."},
	}
}
