package requestcontext

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRoutingThreadIdentityValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers http.Header
		body    string
		status  ThreadIdentityStatus
	}{
		{"missing", nil, `{}`, ThreadIdentityMissing},
		{"conversation only", http.Header{"Session-Id": {"shared"}}, `{}`, ThreadIdentityMissing},
		{"explicit headers", http.Header{ConversationIDHeader: {"root"}, ThreadIDHeader: {"child"}}, `{}`, ThreadIdentityAvailable},
		{"body metadata", nil, `{"weave_routing":{"conversation_id":"root","thread_id":"child"}}`, ThreadIdentityAvailable},
		{"matching sources", http.Header{ThreadIDHeader: {"child"}}, `{"weave_routing":{"thread_id":"child"}}`, ThreadIdentityAvailable},
		{"conflicting sources", http.Header{ThreadIDHeader: {"parent"}}, `{"weave_routing":{"thread_id":"child"}}`, ThreadIdentityConflicting},
		{"duplicate header", http.Header{ThreadIDHeader: {"parent", "child"}}, `{"weave_routing":{"thread_id":"parent"}}`, ThreadIdentityConflicting},
		{"malformed metadata", nil, `{"weave_routing":42}`, ThreadIdentityInvalid},
		{"numeric thread", nil, `{"weave_routing":{"thread_id":42}}`, ThreadIdentityInvalid},
		{"duplicate field", nil, `{"weave_routing":{"thread_id":"a","thread_id":"b"}}`, ThreadIdentityInvalid},
		{"duplicate metadata", nil, `{"weave_routing":{"thread_id":"a"},"weave_routing":{"thread_id":"b"}}`, ThreadIdentityConflicting},
		{"triple metadata", nil, `{"weave_routing":{"thread_id":"a"},"weave_routing":{"thread_id":"b"},"weave_routing":{"thread_id":"c"},"messages":[]}`, ThreadIdentityConflicting},
		{"oversized", http.Header{ThreadIDHeader: {strings.Repeat("x", MaxClientIdentifierLen+1)}}, `{}`, ThreadIdentityInvalid},
		{"invalid conversation", http.Header{"Session-Id": {"bad\x01id"}, ThreadIDHeader: {"child"}}, `{}`, ThreadIdentityInvalid},
		{"self parent", nil, `{"weave_routing":{"thread_id":"child","parent_thread_id":"child"}}`, ThreadIdentityInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			for name, values := range test.headers {
				for _, value := range values {
					headers.Add(name, value)
				}
			}
			identity, clean, err := RoutingIdentityFromRequest(headers, []byte(test.body))
			require.NoError(t, err)
			require.Equal(t, test.status, identity.Status)
			require.False(t, gjson.GetBytes(clean, RoutingIdentityField).Exists())
			scope := identity.AutomaticScope("credential", "installation")
			require.Equal(t, test.status == ThreadIdentityAvailable, scope.Persistent)
			if !scope.Persistent {
				require.Zero(t, scope.Key)
			}
		})
	}
}

func TestNestedOpenCodeThreadIdentity(t *testing.T) {
	for _, legacyHeader := range []string{"Session-Id", ClaudeCodeSessionHeader} {
		t.Run(legacyHeader, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set(OpenCodeSessionHeader, "grandchild")
			headers.Set(OpenCodeParentSessionHeader, "child")
			identity := ThreadIdentityFromHeaders(headers)
			require.Equal(t, ThreadIdentityAvailable, identity.Status)
			require.Equal(t, "child", identity.ParentThreadID)
			require.Empty(t, identity.ConversationID)
			scope := identity.AutomaticScope("credential", "installation")
			headers.Set(legacyHeader, "unrelated-session")
			identity = ThreadIdentityFromHeaders(headers)
			require.Empty(t, identity.ConversationID)
			require.Equal(t, scope.Key, identity.AutomaticScope("credential", "installation").Key)
			headers.Set(ConversationIDHeader, "root")
			identity = ThreadIdentityFromHeaders(headers)
			require.Equal(t, "root", identity.ConversationID)
			require.Equal(t, "grandchild", identity.ThreadID)
		})
	}
}

func TestAutomaticScopeOwnershipBoundaries(t *testing.T) {
	identity := ThreadIdentity{ConversationID: "root", ThreadID: "child", Source: ThreadSourceExplicit, Status: ThreadIdentityAvailable}
	initial := identity.AutomaticScope("credential", "installation")
	require.True(t, initial.Persistent)
	require.NotEqual(t, initial.Key, identity.AutomaticScope("other-credential", "installation").Key)
	require.NotEqual(t, initial.Key, identity.AutomaticScope("credential", "other-installation").Key)
	identity.ThreadID = "fork"
	require.NotEqual(t, initial.Key, identity.AutomaticScope("credential", "installation").Key)
	identity.ThreadID = "child"
	identity.ParentThreadID = "parent"
	require.Equal(t, initial.Key, identity.AutomaticScope("credential", "installation").Key)
}
