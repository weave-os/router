package proxy

import (
	"errors"
	"strconv"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

const upstreamCallFailedMessage = "Upstream call failed."

var providerDisplayNames = map[string]string{
	providers.ProviderAnthropic:        "Anthropic",
	providers.ProviderAnthropicGateway: "Anthropic",
	providers.ProviderOpenAI:           "OpenAI",
	providers.ProviderOpenAIGateway:    "OpenAI",
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
		return failureWording{"was overloaded", withStatus("overloaded")}
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
	return translate.RescueDetailPrefix + providerDisplayName(a.Target.Provider) + " " + failureWordingFor(a).badge + " (" + string(owner) + " issue)\n\n"
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
// attempt that ended the turn; it unwraps to the attempt's error.
type attributedError struct {
	err     error
	message string
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
	message := terminalMessageFor(last)
	if message == "" {
		return err
	}
	return attributedError{err: err, message: message}
}

func terminalMessageFor(a tracedAttempt) string {
	wording := failureWordingFor(a).terminal
	provider := providerDisplayName(a.Target.Provider)
	switch a.owner() {
	case FailureOwnerProvider:
		return "Provider " + provider + " failed: " + wording + "."
	case FailureOwnerRouter:
		return "Router error: " + strings.TrimPrefix(provider+" "+wording, "The provider ") + "."
	case FailureOwnerClient:
		return "Request rejected by " + provider + ": " + wording + "."
	default:
		return ""
	}
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
