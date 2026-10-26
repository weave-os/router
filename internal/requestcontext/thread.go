package requestcontext

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	ConversationIDHeader = "X-Weave-Conversation-Id"
	ThreadIDHeader       = "X-Weave-Thread-Id"
	ParentThreadIDHeader = "X-Weave-Parent-Thread-Id"
	RoutingIdentityField = "weave_routing"
)

type ThreadIdentitySource string

const (
	ThreadSourceUnavailable ThreadIdentitySource = "unavailable"
	ThreadSourceExplicit    ThreadIdentitySource = "explicit"
	ThreadSourceOpenCode    ThreadIdentitySource = "opencode"
	ThreadSourcePiTicket    ThreadIdentitySource = "pi_ticket"
	ThreadSourceClassifier  ThreadIdentitySource = "classifier"
)

type ThreadIdentityStatus string

const (
	ThreadIdentityAvailable   ThreadIdentityStatus = "available"
	ThreadIdentityMissing     ThreadIdentityStatus = "missing"
	ThreadIdentityInvalid     ThreadIdentityStatus = "invalid"
	ThreadIdentityConflicting ThreadIdentityStatus = "conflicting"
)

type ThreadIdentity struct {
	ConversationID string
	ThreadID       string
	ParentThreadID string
	Source         ThreadIdentitySource
	Status         ThreadIdentityStatus
}

type AutomaticStateScope struct {
	Key        [16]byte
	Persistent bool
	Source     ThreadIdentitySource
	Status     ThreadIdentityStatus
}

type threadIdentityContextKey struct{}

func WithThreadIdentity(ctx context.Context, identity ThreadIdentity) context.Context {
	return context.WithValue(ctx, threadIdentityContextKey{}, identity)
}

func ThreadIdentityFromContext(ctx context.Context) ThreadIdentity {
	if identity, ok := ctx.Value(threadIdentityContextKey{}).(ThreadIdentity); ok {
		return identity
	}
	return ClientIdentityFrom(ctx).RoutingThread
}

func validRoutingIdentifier(identifier string) bool {
	return identifier != "" && len(identifier) <= MaxClientIdentifierLen &&
		strings.TrimSpace(identifier) == identifier && strings.IndexFunc(identifier, unicode.IsControl) == -1
}

func ThreadIdentityFromHeaders(headers http.Header) ThreadIdentity {
	identity := ThreadIdentity{Source: ThreadSourceUnavailable, Status: ThreadIdentityMissing}
	for _, header := range []string{ConversationIDHeader, ThreadIDHeader, ParentThreadIDHeader, OpenCodeSessionHeader, OpenCodeParentSessionHeader, ClaudeCodeSessionHeader, "Session-Id"} {
		values := headers.Values(header)
		if len(values) > 1 {
			identity.Status = ThreadIdentityConflicting
			return identity
		}
		if len(values) == 1 && !validRoutingIdentifier(values[0]) {
			identity.Status = ThreadIdentityInvalid
			return identity
		}
	}
	identity.ConversationID = headers.Get(ConversationIDHeader)
	if identity.ConversationID == "" {
		for _, name := range [...]string{ClaudeCodeSessionHeader, "Session-Id"} {
			if value := headers.Get(name); value != "" {
				identity.ConversationID = value
				break
			}
		}
	}
	identity.ThreadID = headers.Get(ThreadIDHeader)
	identity.ParentThreadID = headers.Get(ParentThreadIDHeader)
	identity.Source = ThreadSourceExplicit
	if nativeThread := headers.Get(OpenCodeSessionHeader); nativeThread != "" {
		if identity.ThreadID != "" && identity.ThreadID != nativeThread {
			identity.Status = ThreadIdentityConflicting
			return identity
		}
		identity.ThreadID = nativeThread
		if parent := headers.Get(OpenCodeParentSessionHeader); parent != "" {
			if identity.ParentThreadID != "" && identity.ParentThreadID != parent {
				identity.Status = ThreadIdentityConflicting
				return identity
			}
			identity.ParentThreadID = parent
		} else if identity.ConversationID == "" {
			identity.ConversationID = nativeThread
		}
		identity.Source = ThreadSourceOpenCode
	}
	if identity.ThreadID == "" {
		identity.Source = ThreadSourceUnavailable
		return identity
	}
	if identity.ParentThreadID == identity.ThreadID {
		identity.Status = ThreadIdentityInvalid
		return identity
	}
	identity.Status = ThreadIdentityAvailable
	return identity
}

func RoutingIdentityFromRequest(headers http.Header, body []byte) (ThreadIdentity, []byte, error) {
	metadata := gjson.GetBytes(body, RoutingIdentityField)
	if !metadata.Exists() {
		return ThreadIdentityFromHeaders(headers), body, nil
	}
	clean, err := sjson.DeleteBytes(body, RoutingIdentityField)
	if err != nil {
		return ThreadIdentity{}, nil, err
	}
	if gjson.GetBytes(clean, RoutingIdentityField).Exists() {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return ThreadIdentity{}, nil, err
		}
		delete(fields, RoutingIdentityField)
		clean, err = json.Marshal(fields)
		return ThreadIdentity{Source: ThreadSourceUnavailable, Status: ThreadIdentityConflicting}, clean, err
	}
	merged := headers.Clone()
	if merged == nil {
		merged = make(http.Header)
	}
	invalid := !metadata.IsObject()
	seenFields := make(map[string]bool)
	metadata.ForEach(func(key, _ gjson.Result) bool {
		if seenFields[key.String()] {
			invalid = true
		}
		seenFields[key.String()] = true
		return true
	})
	if headerIdentity := ThreadIdentityFromHeaders(headers); headerIdentity.Status == ThreadIdentityConflicting || headerIdentity.Status == ThreadIdentityInvalid {
		return headerIdentity, clean, nil
	}
	for field, header := range map[string]string{"conversation_id": ConversationIDHeader, "thread_id": ThreadIDHeader, "parent_thread_id": ParentThreadIDHeader} {
		value := metadata.Get(field)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.String || !validRoutingIdentifier(value.String()) {
			invalid = true
			continue
		}
		if existing := merged.Get(header); existing != "" && existing != value.String() {
			return ThreadIdentity{Source: ThreadSourceUnavailable, Status: ThreadIdentityConflicting}, clean, nil
		}
		merged.Set(header, value.String())
	}
	if invalid {
		return ThreadIdentity{Source: ThreadSourceUnavailable, Status: ThreadIdentityInvalid}, clean, nil
	}
	return ThreadIdentityFromHeaders(merged), clean, nil
}

func (identity ThreadIdentity) AutomaticScope(credentialIdentity, installationID string) AutomaticStateScope {
	scope := AutomaticStateScope{Source: identity.Source, Status: identity.Status}
	if identity.Status != ThreadIdentityAvailable || !validRoutingIdentifier(identity.ThreadID) || credentialIdentity == "" ||
		(identity.ConversationID != "" && !validRoutingIdentifier(identity.ConversationID)) ||
		(identity.ParentThreadID != "" && (!validRoutingIdentifier(identity.ParentThreadID) || identity.ParentThreadID == identity.ThreadID)) {
		return scope
	}
	payload, _ := json.Marshal(struct {
		Domain       string
		Installation string
		Conversation string
		Thread       string
	}{"automatic-thread-v2", installationID, identity.ConversationID, identity.ThreadID})
	hash := hmac.New(sha256.New, []byte(credentialIdentity))
	hash.Write(payload)
	copy(scope.Key[:], hash.Sum(nil))
	scope.Persistent = true
	return scope
}
