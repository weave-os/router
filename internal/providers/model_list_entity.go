package providers

import "net/http"

// EmptyJSONEntity is the body replayed on a model-list retry for gateways that
// require an entity on the catalog GET.
var EmptyJSONEntity = []byte("{}")

// ModelListNeedsEntity reports whether a rejected model-list response is worth
// retrying with a JSON entity. Snowflake Cortex rejects a bodyless catalog GET
// with 415 or 400, then serves the catalog when both header and body are present.
func ModelListNeedsEntity(status int) bool {
	return status == http.StatusUnsupportedMediaType || status == http.StatusBadRequest
}
