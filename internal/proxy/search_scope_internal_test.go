package proxy

import (
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchUseTracker_DecaysAfterConfiguredTurns(t *testing.T) {
	tracker := newSearchUseTracker()

	assert.False(t, tracker.observe("s1", -1, 3), "never used stays inactive")
	assert.True(t, tracker.observe("s1", 0, 3), "current-turn use activates")
	assert.True(t, tracker.observe("s1", -1, 3), "1st turn after use stays active")
	assert.True(t, tracker.observe("s1", -1, 3), "2nd turn after use stays active")
	assert.False(t, tracker.observe("s1", -1, 3), "decays after 3 turns without use")
	assert.False(t, tracker.observe("s1", -1, 3), "stays decayed")
}

func TestSearchUseTracker_BodyRecencySeedsCounter(t *testing.T) {
	tracker := newSearchUseTracker()

	assert.True(t, tracker.observe("s1", 1, 3), "history use 1 turn ago is active")
	assert.True(t, tracker.observe("s1", 2, 3))
	assert.False(t, tracker.observe("s1", 3, 3), "history use decayTurns ago is decayed")
}

func TestSearchUseTracker_RefreshOnNewUse(t *testing.T) {
	tracker := newSearchUseTracker()

	assert.True(t, tracker.observe("s1", 0, 1))
	assert.False(t, tracker.observe("s1", -1, 1), "decayTurns=1 decays on the next turn")
	assert.True(t, tracker.observe("s1", 0, 1), "new use re-activates")
}

func TestSearchUseTracker_SessionsAreIndependent(t *testing.T) {
	tracker := newSearchUseTracker()

	assert.True(t, tracker.observe("s1", 0, 3))
	assert.False(t, tracker.observe("s2", -1, 3), "other session unaffected")
}

func scopeService(enabled bool) *Service {
	return &Service{
		scopedSearchRequirement:     enabled,
		searchRequirementDecayTurns: DefaultSearchRequirementDecayTurns,
		searchUse:                   newSearchUseTracker(),
	}
}

func advertisedOnlyEnv(t *testing.T) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(`{
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[{"type":"text","text":"hi"}]},
			{"role":"user","content":"write a function"}
		]
	}`))
	require.NoError(t, err)
	return env
}

func searchUseEnv(t *testing.T) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(`{
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[
			{"role":"user","content":"look this up"},
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"weather"}}
			]},
			{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}]}
		]
	}`))
	require.NoError(t, err)
	return env
}

func TestScopeSearchRequirement_FlagOffLeavesAdvertisementEnforced(t *testing.T) {
	reqs := router.TranslationRequirements{SourceFormat: router.WireFormatAnthropic, CitationsOrSearch: true}
	got := scopeService(false).scopeSearchRequirement([sessionpin.SessionKeyLen]byte{1}, advertisedOnlyEnv(t), reqs)
	assert.True(t, got.CitationsOrSearch)
}

func TestScopeSearchRequirement_AdvertisedOnlyReturnsToPolicyRouting(t *testing.T) {
	reqs := router.TranslationRequirements{SourceFormat: router.WireFormatAnthropic, CitationsOrSearch: true}
	got := scopeService(true).scopeSearchRequirement([sessionpin.SessionKeyLen]byte{1}, advertisedOnlyEnv(t), reqs)
	assert.False(t, got.CitationsOrSearch)
}

func TestScopeSearchRequirement_ActualUseKeepsRequirement(t *testing.T) {
	reqs := router.TranslationRequirements{SourceFormat: router.WireFormatAnthropic, CitationsOrSearch: true}
	got := scopeService(true).scopeSearchRequirement([sessionpin.SessionKeyLen]byte{1}, searchUseEnv(t), reqs)
	assert.True(t, got.CitationsOrSearch)
}

func TestScopeSearchRequirement_RecentUseDecays(t *testing.T) {
	svc := scopeService(true)
	key := [sessionpin.SessionKeyLen]byte{2}
	reqs := router.TranslationRequirements{SourceFormat: router.WireFormatAnthropic, CitationsOrSearch: true}

	got := svc.scopeSearchRequirement(key, searchUseEnv(t), reqs)
	assert.True(t, got.CitationsOrSearch, "search turn")
	for i := 0; i < DefaultSearchRequirementDecayTurns-1; i++ {
		got = svc.scopeSearchRequirement(key, advertisedOnlyEnv(t), reqs)
		assert.True(t, got.CitationsOrSearch, "turn %d after use stays capable", i+1)
	}
	got = svc.scopeSearchRequirement(key, advertisedOnlyEnv(t), reqs)
	assert.False(t, got.CitationsOrSearch, "requirement decays after N turns without use")
}

func TestScopeSearchRequirement_GeminiUnscoped(t *testing.T) {
	env, err := translate.ParseGemini([]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	require.NoError(t, err)
	reqs := router.TranslationRequirements{SourceFormat: router.WireFormatGemini, CitationsOrSearch: true}
	got := scopeService(true).scopeSearchRequirement([sessionpin.SessionKeyLen]byte{3}, env, reqs)
	assert.True(t, got.CitationsOrSearch)
}
