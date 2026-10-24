package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

const (
	upstreamCallFailedMessage = "Upstream call failed."
	cortexDisplayName         = "Cortex"
)

var providerDisplayNames = map[string]string{
	providers.ProviderAnthropic:        "Anthropic",
	providers.ProviderAnthropicGateway: "your gateway",
	providers.ProviderOpenAI:           "OpenAI",
	providers.ProviderOpenAIGateway:    "your gateway",
	providers.ProviderGoogle:           "Gemini",
	providers.ProviderOpenRouter:       "OpenRouter",
	providers.ProviderFireworks:        "Fireworks",
	providers.ProviderDeepInfra:        "DeepInfra",
	providers.ProviderBedrock:          "Bedrock",
	providers.ProviderMakora:           "Makora",
	providers.ProviderMiniMax:          "MiniMax",
	providers.ProviderTogether:         "Together",
	providers.ProviderXAI:              "xAI",
	providers.ProviderMeta:             "Meta",
	providers.ProviderWafer:            "Wafer",
	providers.ProviderWaferAnthropic:   "Wafer",
}

// providerDisplayNameFor names who served an attempt. A customer gateway is
// named by its host so a gateway failure is never blamed on the model vendor.
func providerDisplayNameFor(provider, baseURL string) string {
	if baseURL == "" {
		return providerDisplayName(provider)
	}
	host, hostname := baseURL, ""
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		host, hostname = u.Host, u.Hostname()
	}
	// Any Cortex account host, not only the paths IsCortexBaseURL accepts,
	// since the Anthropic gateway mounts under its own Cortex path.
	if providers.IsCortexBaseURL(baseURL) || strings.HasSuffix(strings.ToLower(hostname), ".snowflakecomputing.com") {
		return cortexDisplayName
	}
	return "your gateway (" + host + ")"
}

func (a tracedAttempt) providerName() string {
	return providerDisplayNameFor(a.Target.Provider, a.BaseURL)
}

func providerDisplayName(provider string) string {
	if name, ok := providerDisplayNames[provider]; ok {
		return name
	}
	if provider == "" {
		return "The provider"
	}
	return provider
}

// failureWording is how a failed attempt reads in a routing badge ("timed
// out") and in a terminal error ("timeout").
type failureWording struct {
	badge    string
	terminal string
}

func failureWordingFor(a tracedAttempt) failureWording {
	status := upstreamStatus(a.Err)
	withStatus := func(text string) string {
		if status == 0 {
			return text
		}
		return text + " (" + strconv.Itoa(status) + ")"
	}
	if providers.IsUpstreamCyberPolicyRefusal(a.Err) {
		return failureWording{"declined the request", "declined the request"}
	}
	if errors.Is(a.Err, ErrSubscriptionPoolExhausted) {
		return failureWording{"had no subscription capacity", "subscription capacity exhausted"}
	}
	if errors.Is(a.Err, translate.ErrCustomToolArguments) {
		return failureWording{"sent tool arguments we couldn't translate", "tool call translation failed"}
	}
	switch TurnErrorClass(a.Diagnosis.ErrorClass) {
	case TurnErrorStreamStalled:
		return failureWording{"stalled", "stream stalled"}
	case TurnErrorTimeout:
		return failureWording{"timed out", "timeout"}
	case TurnErrorRateLimited:
		if status == providerOverloadedStatus {
			return failureWording{"was overloaded", withStatus("overloaded")}
		}
		return failureWording{"was rate-limited", withStatus("rate limited")}
	case TurnErrorUpstream5xx:
		return failureWording{"returned a " + strconv.Itoa(status), "server error " + strconv.Itoa(status)}
	case TurnErrorStreamCut:
		return failureWording{"cut the stream", "stream cut"}
	case TurnErrorRefusal:
		return failureWording{"declined the request", "declined the request"}
	case TurnErrorClientCanceled:
		return failureWording{"was canceled", "request canceled"}
	}
	if status >= 400 && status < 500 {
		if a.Diagnosis.BodyTranslated && a.owner() == FailureOwnerRouter {
			return failureWording{"rejected our translated request", withStatus("rejected our translated request")}
		}
		return failureWording{"rejected the request", withStatus("rejected the request")}
	}
	return failureWording{"failed", "error"}
}

// rescueDetailLine is the badge's second line naming the failed attempt the
// rescue replaced and who owned the failure.
func rescueDetailLine(a tracedAttempt) string {
	owner := a.owner()
	if owner == "" || owner == FailureOwnerUnknown {
		return ""
	}
	return translate.RescueDetailPrefix + a.providerName() + " " + failureWordingFor(a).badge + " (" + string(owner) + " issue)\n\n"
}

// withRescueDetail appends the rescue detail line to a rendered badge. An
// empty badge (suppressed or not shown) stays empty.
func withRescueDetail(marker, detail string) string {
	if marker == "" || detail == "" {
		return marker
	}
	return marker + detail
}

// lastFailureDetail renders the detail line for the request's latest failed
// attempt, or "" when none was traced.
func lastFailureDetail(trace *attemptTrace) string {
	last, ok := trace.lastFailure()
	if !ok {
		return ""
	}
	return rescueDetailLine(last)
}

// attributedError carries a terminal message naming the owner of the failed
// attempt that ended the turn, and the attribution prefixed onto the upstream
// error message; it unwraps to the attempt's error.
type attributedError struct {
	err     error
	message string
	prefix  string
}

func (e attributedError) Error() string { return e.err.Error() }
func (e attributedError) Unwrap() error { return e.err }

// attributeTerminalError names the owner of err's attempt for the terminal
// message, unless the request hides router surfaces.
func attributeTerminalError(trace *attemptTrace, err error) error {
	if err == nil || trace == nil || trace.hideDetails() {
		return err
	}
	last, ok := trace.lastFailure()
	if !ok || !errors.Is(err, last.Err) {
		return err
	}
	return attributeAttempt(last, trace.retriesOf(last), err)
}

// attributeStreamError attributes a failure the serving attempt hit after its
// stream committed, before dispatch has traced it.
func attributeStreamError(ctx context.Context, provider string, err error) error {
	trace := attemptTraceFromContext(ctx)
	if err == nil || trace.hideDetails() {
		return err
	}
	var attributed attributedError
	if errors.As(err, &attributed) {
		return err
	}
	a := tracedAttempt{
		Target:    inference.Target{Provider: provider},
		BaseURL:   requestcontext.EffectiveBaseURL(ctx, trace.servingBaseURL()),
		Err:       err,
		Diagnosis: inference.AttemptDiagnosis{ErrorClass: string(classifyTurnError(err, "", 0))},
	}
	a.Diagnosis.FailureOwner = string(classifyFailureOwner(failureOwnerInput{Err: err}))
	return attributeAttempt(a, 0, err)
}

func attributeAttempt(a tracedAttempt, retries int, err error) error {
	prefix := attributionPrefix(a, retries)
	message := attributedTerminalMessage(a, prefix)
	if message == "" && prefix == "" {
		return err
	}
	return attributedError{err: err, message: message, prefix: prefix}
}

// attributionPrefix states what the provider did, e.g. "Cortex
// returned 429 (rate limited) after 2 router retries".
func attributionPrefix(a tracedAttempt, retries int) string {
	if a.owner() == FailureOwnerRouter {
		return ""
	}
	var outcome string
	switch status := upstreamStatus(a.Err); {
	case status == http.StatusTooManyRequests:
		outcome = "returned 429 (rate limited)"
	case status == providerOverloadedStatus:
		outcome = "returned 529 (overloaded)"
	case status >= 500:
		outcome = "returned " + strconv.Itoa(status) + " (server error)"
	case status >= 400:
		outcome = "returned " + strconv.Itoa(status) + " (" + failureWordingFor(a).badge + ")"
	default:
		switch TurnErrorClass(a.Diagnosis.ErrorClass) {
		case TurnErrorTimeout:
			outcome = "timed out"
		case TurnErrorStreamStalled:
			outcome = "stalled mid-stream"
		case TurnErrorStreamCut:
			outcome = "cut the stream"
		default:
			return ""
		}
	}
	prefix := a.providerName() + " " + outcome
	switch {
	case retries == 1:
		prefix += " after 1 router retry"
	case retries > 1:
		prefix += " after " + strconv.Itoa(retries) + " router retries"
	}
	return prefix
}

func terminalMessageFor(a tracedAttempt) string {
	return attributedTerminalMessage(a, attributionPrefix(a, 0))
}

func attributedTerminalMessage(a tracedAttempt, prefix string) string {
	wording := failureWordingFor(a).terminal
	provider := a.providerName()
	switch a.owner() {
	case FailureOwnerProvider:
		if prefix != "" {
			if msg := upstreamErrorText(a.Err); msg != "" {
				return prefix + ": " + msg
			}
			return prefix + " (provider issue)."
		}
		return "Provider " + provider + " failed: " + wording + "."
	case FailureOwnerRouter:
		return "Router error: " + strings.TrimPrefix(provider+" "+wording, "The provider ") + "."
	case FailureOwnerClient:
		return "Request rejected by " + provider + ": " + wording + "."
	default:
		return ""
	}
}

const maxUpstreamErrorText = 500

// Reads error.message, then message, from a JSON upstream body.
func upstreamErrorText(err error) string {
	var body []byte
	var resp *providers.UpstreamErrorResponse
	var statusErr *providers.UpstreamStatusError
	switch {
	case errors.As(err, &resp):
		body = resp.Body
	case errors.As(err, &statusErr):
		body = statusErr.Body
	}
	if !gjson.ValidBytes(body) {
		return ""
	}
	for _, path := range []string{"error.message", "message"} {
		if msg := gjson.GetBytes(body, path); msg.Type == gjson.String {
			text := strings.TrimSpace(msg.String())
			if len(text) > maxUpstreamErrorText {
				text = strings.ToValidUTF8(text[:maxUpstreamErrorText], "") + "…"
			}
			return text
		}
	}
	return ""
}

// TerminalErrorMessage is the client-facing message for a failed dispatch the
// handler renders generically: the owner-attributed message when dispatch
// recorded one, else the generic upstream failure text.
func TerminalErrorMessage(err error) string {
	var attributed attributedError
	if errors.As(err, &attributed) && attributed.message != "" {
		return attributed.message
	}
	return upstreamCallFailedMessage
}

// attributedErrorBody prefixes the error message of an upstream error body
// with err's attribution. Status, error type and every other field are kept,
// so client retry decisions are unchanged.
func attributedErrorBody(body []byte, err error) []byte {
	var attributed attributedError
	if !errors.As(err, &attributed) || attributed.prefix == "" || !gjson.ValidBytes(body) {
		return body
	}
	for _, path := range []string{"error.message", "message"} {
		msg := gjson.GetBytes(body, path)
		if msg.Type != gjson.String {
			continue
		}
		if strings.HasPrefix(msg.String(), attributed.prefix) {
			return body
		}
		text := attributed.prefix
		if msg.String() != "" {
			text += ": " + msg.String()
		}
		out, _ := sjson.SetBytes(body, path, text)
		return out
	}
	return body
}

// attributedStreamFailure is the message for a stream failure with no
// upstream body: the attribution when known, else the generic text.
func attributedStreamFailure(err error) string {
	var attributed attributedError
	if errors.As(err, &attributed) && attributed.prefix != "" {
		return attributed.prefix + "."
	}
	return "upstream stream failed"
}
