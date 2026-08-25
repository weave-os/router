package providers

import "net/http"

// EmptyJSONEntity is the body replayed on a model-list retry for gateways that
// require an entity on the catalog GET.
var EmptyJSONEntity = []byte("{}")

// ModelListNeedsEntity reports whether a rejected model-list response is worth
// retrying with a JSON entity. Snowflake Cortex answers a bodyless catalog GET
// with 415 ("Invalid input value") and a typed but bodyless one with 400
// ("request entity required"), while serving the catalog for a GET carrying
// both.
func ModelListNeedsEntity(status int) bool {
	return status == http.StatusUnsupportedMediaType || status == http.StatusBadRequest
}
