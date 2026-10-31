-- name: ListTokenEfficiencyRatios :many
SELECT harness, baseline_model, served_model, input_point, input_lower, output_point, output_lower, native, valid_from, valid_to
FROM router.token_efficiency_ratios
WHERE baseline_model = ANY(@baseline_models::text[]);

-- name: ListTokenEfficiencyQualityFactors :many
SELECT harness, baseline_model, router_pass_rate_lower, baseline_pass_rate_upper, valid_from, valid_to
FROM router.token_efficiency_quality_factors
WHERE baseline_model = ANY(@baseline_models::text[]);
