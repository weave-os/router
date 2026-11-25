package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/ingress"
)

type servingReleaseHeaderWriter struct {
	gin.ResponseWriter
	request func() *http.Request
	stamped bool
}

func (w *servingReleaseHeaderWriter) stamp(status int) {
	if w.stamped {
		return
	}
	w.stamped = true
	if status < http.StatusBadRequest {
		ingress.SetServingReleaseHeaders(w.request().Context(), w.Header())
	}
}

func (w *servingReleaseHeaderWriter) WriteHeader(code int) {
	w.stamp(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *servingReleaseHeaderWriter) WriteHeaderNow() {
	w.stamp(w.Status())
	w.ResponseWriter.WriteHeaderNow()
}

func (w *servingReleaseHeaderWriter) Write(data []byte) (int, error) {
	w.stamp(w.Status())
	return w.ResponseWriter.Write(data)
}

func (w *servingReleaseHeaderWriter) WriteString(s string) (int, error) {
	w.stamp(w.Status())
	return w.ResponseWriter.WriteString(s)
}

func (w *servingReleaseHeaderWriter) Flush() {
	w.stamp(w.Status())
	w.ResponseWriter.Flush()
}
