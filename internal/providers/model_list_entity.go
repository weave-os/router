package providers

import "net/http"

var EmptyJSONEntity = []byte("{}")

func ModelListNeedsEntity(status int) bool {
	return status == http.StatusUnsupportedMediaType || status == http.StatusBadRequest
}
