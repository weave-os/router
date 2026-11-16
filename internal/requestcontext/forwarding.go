package requestcontext

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"weave-os/router/internal/auth"
)

const (
	baggageOnBehalfOf  = "on-behalf-of"
	baggagePassthrough = "passthrough"
)

type callerModelPassthroughContextKey struct{}

func WithCallerModelPassthrough(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, callerModelPassthroughContextKey{}, enabled)
}

const ClaudeCodeSessionHeader = "X-Claude-Code-Session-Id"

type ForwardedHeaderSnapshotContextKey struct{}

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
			if safeExternalKeyHeaderDestination(key, name) {
				capture(name)
			}
		}
		if safeExternalKeyHeaderDestination(key, key.BaggageHeader) {
			capture(key.BaggageHeader)
		}
	}
	if snapshot == nil {
		return ctx
	}
	return context.WithValue(ctx, ForwardedHeaderSnapshotContextKey{}, snapshot)
}

func ForwardedHeaderSnapshotFrom(ctx context.Context) http.Header {
	h, _ := ctx.Value(ForwardedHeaderSnapshotContextKey{}).(http.Header)
	return h
}

func ApplyForwardedClientHeaders(ctx context.Context, upstream *http.Request, inbound http.Header) {
	creds := CredentialsFromContext(ctx)
	if creds == nil {
		return
	}
	for _, name := range creds.ForwardedClientHeaders {
		if !safeCredentialHeaderDestination(creds, name) || headerFieldExists(upstream.Header, name) {
			continue
		}
		if v := forwardedValue(ctx, inbound, name); v != "" {
			upstream.Header.Set(name, v)
		}
	}
	if !safeCredentialHeaderDestination(creds, creds.BaggageHeader) || headerFieldExists(upstream.Header, creds.BaggageHeader) {
		return
	}
	baggage := forwardedValue(ctx, inbound, creds.BaggageHeader)
	if merged := mergeBaggage(baggage, ClientIdentityFrom(ctx).Email, callerModelPassthroughFrom(ctx)); merged != "" {
		upstream.Header.Set(creds.BaggageHeader, merged)
	}
}

func callerModelPassthroughFrom(ctx context.Context) bool {
	enabled, _ := ctx.Value(callerModelPassthroughContextKey{}).(bool)
	return enabled
}

func forwardedValue(ctx context.Context, inbound http.Header, name string) string {
	if !auth.IsSafeForwardingHeader(name) {
		return ""
	}
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

func safeExternalKeyHeaderDestination(key *auth.ExternalAPIKey, name string) bool {
	if key == nil || !auth.IsSafeForwardingHeader(name) {
		return false
	}
	return headerDestinationCount(name, key.ForwardedClientHeaders, key.BaggageHeader, key.IdentityHeader) == 1
}

func safeCredentialHeaderDestination(creds *Credentials, name string) bool {
	if creds == nil || !auth.IsSafeForwardingHeader(name) {
		return false
	}
	return headerDestinationCount(name, creds.ForwardedClientHeaders, creds.BaggageHeader, creds.IdentityHeader) == 1
}

func headerDestinationCount(name string, forwarded []string, baggage, identity string) int {
	count := 0
	for _, candidate := range forwarded {
		if strings.EqualFold(name, candidate) {
			count++
		}
	}
	if strings.EqualFold(name, baggage) {
		count++
	}
	if strings.EqualFold(name, identity) {
		count++
	}
	return count
}

func headerFieldExists(headers http.Header, name string) bool {
	for existingName := range headers {
		if strings.EqualFold(existingName, name) {
			return true
		}
	}
	return false
}

func mergeBaggage(baggage, email string, passthrough bool) string {
	bag := map[string]json.RawMessage{}
	if baggage != "" {
		if err := json.Unmarshal([]byte(baggage), &bag); err != nil {
			return baggage
		}
		if bag == nil {
			return baggage
		}
	}
	if email != "" {
		encodedEmail, err := json.Marshal(email)
		if err != nil {
			return baggage
		}
		bag[baggageOnBehalfOf] = encodedEmail
	}
	bag[baggagePassthrough] = json.RawMessage(strconv.FormatBool(passthrough))
	merged, err := json.Marshal(bag)
	if err != nil {
		return baggage
	}
	return string(merged)
}
