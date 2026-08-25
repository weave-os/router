package proxy

import (
	"context"
	"encoding/json"
	"net/http"
)

// baggageOnBehalfOf is the member the router contributes to a vendor baggage
// header so a gateway fronting its own observability can attribute a turn to a
// person. Agreed wire shape with Snowflake Cortex: raw JSON, no percent-encoding.
const baggageOnBehalfOf = "on-behalf-of"

// ApplyForwardedClientHeaders copies the inbound headers this endpoint asked
// for onto the upstream request and re-emits its baggage header with the
// router-resolved user email. Must be called after prep.Headers are copied and
// the adapter's protected headers reapplied, so the endpoint sees the caller's
// own correlation ids without a client being able to forge the identity the
// router derived. inbound may be nil for router-originated calls.
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

// mergeBaggageEmail returns the caller's baggage object with the resolved email
// under on-behalf-of, overwriting any value the client sent: the endpoint
// attributes spend off this bag, so a forged member must not survive. A bag
// that isn't a JSON object travels unchanged rather than being discarded.
func mergeBaggageEmail(baggage, email string) string {
	if email == "" {
		return baggage
	}
	bag := map[string]json.RawMessage{}
	if baggage != "" {
		if err := json.Unmarshal([]byte(baggage), &bag); err != nil {
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
