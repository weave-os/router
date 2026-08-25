package proxy

import (
	"context"
	"encoding/json"
	"net/http"
)

// baggageOnBehalfOf is the key the router adds to the vendor's baggage header.
// Wire shape agreed with Snowflake Cortex: raw JSON, no percent-encoding.
const baggageOnBehalfOf = "on-behalf-of"

// ApplyForwardedClientHeaders copies configured inbound headers and re-emits the baggage header
// with the resolved email. Must be called after prep.Headers are set and protected headers reapplied.
func ApplyForwardedClientHeaders(ctx context.Context, upstream *http.Request, inbound http.Header) {
	creds := CredentialsFromContext(ctx)
	if creds == nil {
		return
	}
	for _, name := range creds.ForwardedClientHeaders {
		if v := inbound.Get(name); v != "" {
			upstream.Header.Set(name, v)
		}
	}
	if creds.BaggageHeader == "" {
		return
	}
	if merged := mergeBaggageEmail(inbound.Get(creds.BaggageHeader), ClientIdentityFrom(ctx).Email); merged != "" {
		upstream.Header.Set(creds.BaggageHeader, merged)
	}
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
