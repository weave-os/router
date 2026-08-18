// The claimed-tool-unavailable detector flags a routed model that says a
// declared tool is unavailable. It persists source="auto" negative feedback
// after capture, without influencing routing or pins.
package proxy

import (
	"bytes"
	"context"
	"strings"
	"time"

	"workweave/router/internal/auth"
	"workweave/router/internal/observability"
	"workweave/router/internal/router/sessionpin"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/tidwall/gjson"
)

// claimedUnavailablePhrases are the loose substrings matched against the
// lowercased ±160-byte window around a declared tool name.
// Package var so tests pin the exact list.
var claimedUnavailablePhrases = []string{
	"not available",
	"isn't available",
	"is unavailable",
	"no such tool",
	"not in my",
	"not directly callable",
	"not callable",
	"don't have access",
	"do not have access",
	"have no access",
	"no mechanism to load",
	"not a loadable",
	"not exposed",
	"not in my available toolset",
}

// claimedTool... knobs for the claimed-tool-unavailable tracker — size/TTL
// mirror the spiral detector's shape (spiral_detection.go:172-175).
const (
	// claimedToolFiredCacheSize bounds the per-replica dedup LRU.
	claimedToolFiredCacheSize = 4096
	// claimedToolFiredCacheTTL is how long a fired (session, role, tool)
	// stays suppressed on this replica; the DB row is the durable record.
	claimedToolFiredCacheTTL = 24 * time.Hour
	// claimedToolWindowBytes is the lowercased window scanned around each
	// declared tool-name occurrence — wide enough to catch multi-clause denials
	// without matching stray "not available" elsewhere in a long reply.
	claimedToolWindowBytes = 160
	// claimedToolNoPrecedeBytes is how far before an occurrence "no " /
	// "there is no " (the "no <tool> tool" signal) is checked.
	claimedToolNoPrecedeBytes = 24
	// claimedToolMaxFindings caps findings per response so one reply can't
	// spam the dedup keys.
	claimedToolMaxFindings = 4
	// claimedToolScanMaxBytes bounds the text extracted from captured bytes.
	claimedToolScanMaxBytes = 262144
)

// claimedToolTracker de-dupes automatic negative feedback fires per
// (session, role, tool) on this replica, mirroring spiralTracker's shape.
type claimedToolTracker struct {
	fired *lru.LRU[string, struct{}]
}

func newClaimedToolTracker() *claimedToolTracker {
	return &claimedToolTracker{
		fired: lru.NewLRU[string, struct{}](claimedToolFiredCacheSize, nil, claimedToolFiredCacheTTL),
	}
}

func claimedToolFiredKey(sessionKey [sessionpin.SessionKeyLen]byte, role, tool string) string {
	return string(sessionKey[:]) + "\x00" + role + "\x00" + tool
}

// claimedToolUnavailableFromBody extracts response text (SSE or JSON) and
// returns declared tools the model claims unavailable. Malformed input fails
// open (no findings) — the capture cap can cut mid-frame.
func claimedToolUnavailableFromBody(respBody []byte, streaming bool, availableTools []string) []string {
	if !streaming {
		return detectClaimedToolUnavailable(nonStreamingText(respBody), availableTools)
	}
	var text strings.Builder
	text.Grow(len(respBody) / 2)
	scanCap := claimedToolScanMaxBytes
	for _, rawLine := range bytes.Split(respBody, []byte("\n")) {
		if !bytes.HasPrefix(rawLine, []byte("data: ")) {
			continue
		}
		data := bytes.TrimRight(rawLine[len("data: "):], "\r")
		if string(data) == "[DONE]" {
			continue
		}
		frame := gjson.ParseBytes(data)
		if frame.Get("type").String() != "content_block_delta" {
			continue
		}
		if frame.Get("delta.type").String() != "text_delta" {
			continue
		}
		delta := frame.Get("delta.text").String()
		if delta == "" {
			continue
		}
		if len(delta) > scanCap {
			delta = delta[:scanCap]
		}
		text.WriteString(delta)
		scanCap -= len(delta)
		if scanCap <= 0 {
			break
		}
	}
	return detectClaimedToolUnavailable(text.String(), availableTools)
}

// nonStreamingText gathers text from a single JSON response body: content
// blocks with type == "text". Any other shape yields no text (fail open).
func nonStreamingText(respBody []byte) string {
	content := gjson.GetBytes(respBody, "content")
	if !content.IsArray() {
		return ""
	}
	var text strings.Builder
	text.Grow(len(respBody) / 2)
	scanCap := claimedToolScanMaxBytes
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "text" {
			return true
		}
		blockText := block.Get("text").String()
		if len(blockText) > scanCap {
			blockText = blockText[:scanCap]
		}
		text.WriteString(blockText)
		scanCap -= len(blockText)
		return scanCap > 0
	})
	return text.String()
}

// detectClaimedToolUnavailable scans text for each declared tool name and
// reports names the model claims unavailable, capped at claimedToolMaxFindings.
// Pure — unit-test directly.
func detectClaimedToolUnavailable(text string, availableTools []string) []string {
	if text == "" || len(availableTools) == 0 {
		return nil
	}
	var findings []string
	for _, tool := range availableTools {
		if tool == "" {
			continue
		}
		if containsClaimedUnavailable(text, tool) {
			findings = append(findings, tool)
			if len(findings) >= claimedToolMaxFindings {
				break
			}
		}
	}
	return findings
}

// containsClaimedUnavailable reports whether any word-boundary occurrence of
// tool in text sits next to an unavailable-claim. Iterates all occurrences:
// the model may mention the tool normally and deny it later in the same reply.
func containsClaimedUnavailable(text, tool string) bool {
	lower := strings.ToLower(text)
	for offset := 0; offset < len(text); {
		idx := strings.Index(text[offset:], tool)
		if idx < 0 {
			break
		}
		idx += offset
		start, end := idx, idx+len(tool)
		if !leftIdentifierEdge(text, start) || !rightIdentifierEdge(text, end) {
			offset = idx + len(tool)
			continue
		}
		if windowClaimsUnavailable(lower, start, end) {
			return true
		}
		offset = idx + len(tool)
	}
	return false
}

// leftIdentifierEdge reports whether i is a clean left identifier edge: text
// start, or preceded by a byte that is not identifier-class.
func leftIdentifierEdge(s string, i int) bool {
	return i == 0 || !isIdentifierClass(s[i-1])
}

// rightIdentifierEdge reports whether i (just past an occurrence) is a clean
// right identifier edge: text end, or followed by a byte that is not
// identifier-class.
func rightIdentifierEdge(s string, i int) bool {
	return i >= len(s) || !isIdentifierClass(s[i])
}

// isIdentifierClass is the identifier-boundary class: ASCII letter, digit, or
// underscore. Bytes >= 0x80 count as boundaries (a surrounding non-ASCII
// character must never break a match).
func isIdentifierClass(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// windowClaimsUnavailable checks a ±claimedToolWindowBytes window around
// each occurrence for unavailable-claim phrases, plus the "no <tool>" precede
// signal via endsWithNoPhrase.
func windowClaimsUnavailable(lower string, occStart, occEnd int) bool {
	// The direct precedent: "...no " / "...there is no " ending immediately
	// before the name. Covers "no <name> tool" without needing a window phrase.
	before := occStart
	if before > claimedToolNoPrecedeBytes {
		before = claimedToolNoPrecedeBytes
	}
	if endsWithNoPhrase(lower[occStart-before : occStart]) {
		return true
	}
	lo := occStart - claimedToolWindowBytes
	if lo < 0 {
		lo = 0
	}
	hi := occEnd + claimedToolWindowBytes
	if hi > len(lower) {
		hi = len(lower)
	}
	window := lower[lo:hi]
	for _, phrase := range claimedUnavailablePhrases {
		if strings.Contains(window, phrase) {
			return true
		}
	}
	return false
}

// endsWithNoPhrase reports whether the lowercased text ending at the tool
// name ends with "no " or "there is no " — i.e. the name was introduced by a
// durative negation ("There is no ToolSearch tool in my available toolset").
func endsWithNoPhrase(lower string) bool {
	pre := strings.TrimRight(lower, " \t")
	return strings.HasSuffix(pre, "no") || strings.HasSuffix(pre, "there is no")
}

// maybeReportClaimedToolUnavailable persists one source="auto" negative
// RouterFeedbackEvent per (session, role, tool). Best-effort, off the response
// path; no-ops on nil deps or missing respBody/tools.
func (s *Service) maybeReportClaimedToolUnavailable(
	ctx context.Context,
	respBody []byte,
	streaming bool,
	availableTools []string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	requestedModel string,
	servedModel string,
	requestID string,
	routeID string,
	clientID ClientIdentity,
) {
	if s.feedbackStore == nil || s.claimedToolTracker == nil {
		return
	}
	if installationID == uuid.Nil || len(respBody) == 0 || len(availableTools) == 0 {
		return
	}
	found := claimedToolUnavailableFromBody(respBody, streaming, availableTools)
	if len(found) == 0 {
		return
	}
	log := observability.FromContext(ctx)
	routerUserID := auth.UserIDFrom(ctx)
	for _, tool := range found {
		key := claimedToolFiredKey(sessionKey, role, tool)
		if _, seen := s.claimedToolTracker.fired.Get(key); seen {
			continue
		}
		log.Info("router.claimed_tool_unavailable",
			"tool", tool,
			"served_model", servedModel,
			"requested_model", requestedModel,
			"request_id", requestID,
			"session_key_prefix", shortSessionKey(sessionKey),
			"role", role,
		)
		event := RouterFeedbackEvent{
			InstallationID: installationID.String(),
			SessionKey:     sessionKey[:],
			Role:           role,
			RouterUserID:   routerUserID,
			ClientApp:      clientID.ClientApp,
			SessionID:      clientID.SessionID,
			RequestedModel: requestedModel,
			ServedModel:    servedModel,
			Rating:         "down",
			Feedback:       "claimed-tool-unavailable:" + tool,
			Source:         RouterFeedbackSourceAuto,
			RequestID:      requestID,
			RouteID:        routeID,
		}
		// context.Background(): the request ctx may be canceled post-stream;
		// a canceled ctx would silently drop the row from the auto corpus.
		if err := s.feedbackStore.InsertRouterFeedback(context.Background(), event); err != nil {
			log.Error("router.claimed_tool_unavailable: feedback insert failed", "err", err)
			continue // leave the LRU unset so the next turn retries
		}
		s.claimedToolTracker.fired.Add(key, struct{}{})
	}
}
