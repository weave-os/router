package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const classifierThreadField = "weave_classifier_thread"

func WithClassifierThread(service *proxy.Service, availability StrategyAvailability) gin.HandlerFunc {
	return func(c *gin.Context) {
		threadToken := c.GetHeader(requestcontext.ClassifierThreadHeader)
		headerValues := c.Request.Header.Values(requestcontext.ClassifierThreadHeader)
		c.Request.Header.Del(requestcontext.ClassifierThreadHeader)
		if len(headerValues) == 1 && threadToken == requestcontext.ClassifierThreadUnavailableToken {
			observability.FromGin(c).Warn("Client could not enroll classifier thread", "path", c.FullPath())
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "classifier_client_unavailable"})
			return
		}
		if len(headerValues) > 1 || (len(headerValues) == 1 && threadToken == "") {
			observability.FromGin(c).Debug("Invalid classifier thread header", "path", c.FullPath(), "header_count", len(headerValues))
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "invalid_classifier_thread"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, requestcontext.MaxRequestBodyBytes+1))
		if err != nil || len(body) > requestcontext.MaxRequestBodyBytes {
			observability.FromGin(c).Debug("Classifier request body read rejected", "path", c.FullPath(), "size_bytes", len(body), "max_body_bytes", requestcontext.MaxRequestBodyBytes, "err", err)
			status := http.StatusBadRequest
			if len(body) > requestcontext.MaxRequestBodyBytes {
				status = http.StatusRequestEntityTooLarge
			}
			c.AbortWithStatusJSON(status, gin.H{"error": "invalid_request_body"})
			return
		}
		if gjson.GetBytes(body, classifierThreadField).Exists() {
			var fields map[string]json.RawMessage
			var bodyToken string
			if json.Unmarshal(body, &fields) != nil || json.Unmarshal(fields[classifierThreadField], &bodyToken) != nil || bodyToken == "" || (threadToken != "" && threadToken != bodyToken) {
				observability.FromGin(c).Debug("Invalid classifier body token or conflicting header", "path", c.FullPath())
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "invalid_classifier_thread"})
				return
			}
			threadToken = bodyToken
			delete(fields, classifierThreadField)
			body, err = json.Marshal(fields)
			if err != nil {
				observability.FromGin(c).Error("Failed to remove classifier token from request", "path", c.FullPath(), "err", err)
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request_body"})
				return
			}
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		if threadToken != "" && (c.FullPath() == "/v1/route/handoff" || c.FullPath() == "/v1/route" || c.FullPath() == "/v1/route/preview") {
			observability.FromGin(c).Debug("Classifier thread requires an inference endpoint", "path", c.FullPath())
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "classifier_requires_inference_endpoint"})
			return
		}
		ctx, err := service.AdmitClassifierThread(c.Request.Context(), threadToken)
		if err != nil {
			observability.FromGin(c).Warn("Classifier thread admission rejected", "path", c.FullPath(), "err", err)
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "classifier_thread_unavailable"})
			return
		}
		c.Request = c.Request.WithContext(ctx)
		if !applyRoutingModel(c, body, availability) {
			return
		}
		c.Next()
	}
}
