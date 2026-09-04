package proxy

import (
	"context"
	"encoding/json"
	"net/http"

	"weave-os/router/internal/auth"
)

// baggageOnBehalfOf is the key the router adds to the vendor's baggage header.
// Wire shape agreed with Snowflake Cortex: raw JSON, no percent-encoding.
const baggageOnBehalfOf = "on-behalf-of"

// ClaudeCodeSessionHeader carries Claude Code's session id. Older CLI builds
// omit it and embed the id in metadata.user_id; ClientIdentity resolves both.
const ClaudeCodeSessionHeader = "X-Claude-Code-Session-Id"

// ForwardedHeaderSnapshotContextKey is the request-context key for the inbound
// correlation headers captured at ingress.
type ForwardedHeaderSnapshotContextKey struct{}

// WithForwardedHeaderSnapshot captures inbound values of every header the
// installation's external keys forward, so router-built requests (compaction
// summaries, Cortex web search) reach the tenant's endpoint correlated.
func WithForwardedHeaderSnapshot(ctx context.Context, keys []*auth.ExternalAPIKey, inbound http.Header) context.Context {
	var snapshot http.Header
	capture := func(name string) {
		if name == "" {
			return
		}
		v := inbound.Get(name)
		if v == "" {
			return
		}
		if snapshot == nil {
			snapshot = make(http.Header, 4)
		}
		snapshot.Set(name, v)
	}
	for _, key := range keys {
		if key == nil {
			continue
		}
		for _, name := range key.ForwardedClientHeaders {
			capture(name)
		}
		capture(key.BaggageHeader)
	}
	if snapshot == nil {
		return ctx
	}
	return context.WithValue(ctx, ForwardedHeaderSnapshotContextKey{}, snapshot)
}

// ForwardedHeaderSnapshotFrom reads the ingress header snapshot stashed on ctx.
func ForwardedHeaderSnapshotFrom(ctx context.Context) http.Header {
	h, _ := ctx.Value(ForwardedHeaderSnapshotContextKey{}).(http.Header)
	return h
}

// ApplyForwardedClientHeaders copies configured inbound headers and re-emits the baggage header
// with the resolved email. Must be called after prep.Headers are set and protected headers reapplied.
func ApplyForwardedClientHeaders(ctx context.Context, upstream *http.Request, inbound http.Header) {
	creds := CredentialsFromContext(ctx)
	if creds == nil {
		return
	}
	for _, name := range creds.ForwardedClientHeaders {
		if v := forwardedValue(ctx, inbound, name); v != "" {
			upstream.Header.Set(name, v)
		}
	}
	if creds.BaggageHeader == "" {
		return
	}
	baggage := forwardedValue(ctx, inbound, creds.BaggageHeader)
	if merged := mergeBaggageEmail(baggage, ClientIdentityFrom(ctx).Email); merged != "" {
		upstream.Header.Set(creds.BaggageHeader, merged)
	}
}

// forwardedValue returns the first non-empty value from: inbound header,
// ingress snapshot, or (for X-Claude-Code-Session-Id only) the resolved
// identity — covering clients that embed the id in the body, not a header.
func forwardedValue(ctx context.Context, inbound http.Header, name string) string {
	if v := inbound.Get(name); v != "" {
		return v
	}
	if v := ForwardedHeaderSnapshotFrom(ctx).Get(name); v != "" {
		return v
	}
	if http.CanonicalHeaderKey(name) == ClaudeCodeSessionHeader {
		return ClientIdentityFrom(ctx).SessionID
	}
	return ""
}

// mergeBaggageEmail injects the router-resolved email as on-behalf-of, overwriting any
// client-supplied value (forged attribution must not survive). Non-JSON bags pass through.
func mergeBaggageEmail(baggage, email string) string {
	if email == "" {
		return baggage
	}
	bag := map[string]json.RawMessage{}
	if baggage != "" {
		if err := json.Unmarshal([]byte(baggage), &bag); err != nil {
			return baggage
		}
		// JSON null decodes without error but nils the map.
		if bag == nil {
			return baggage
		}
	}
	encodedEmail, err := json.Marshal(email)
	if err != nil {
		return baggage
	}
	bag[baggageOnBehalfOf] = encodedEmail
	merged, err := json.Marshal(bag)
	if err != nil {
		return baggage
	}
	// A header value cannot carry the control characters json.Marshal escapes
	// anyway, so the encoded bag is safe to emit verbatim.
	return string(merged)
}
