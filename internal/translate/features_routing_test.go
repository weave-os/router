package translate_test

import (
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicRoutingFeatures_OnlyUserMessageTextStripsLeadingClientWrappers(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		want string
	}{
		{
			name:  "plain instruction",
			body:  `{"messages":[{"role":"user","content":[{"type":"text","text":"please run the tests"}]}]}`,
			want: "please run the tests",
		},
		{
			name:  "quoted result and instruction in the same text block",
			body:  `{"messages":[{"role":"user","content":[{"type":"text","text":"</summary>\n<result>background task completed</result>\n<usage><total_tokens>0</total_tokens></usage>please run the tests"}]}]}`,
			want: "please run the tests",
		},
		{
			name:  "quoted result and instruction in separate text blocks",
			body:  `{"messages":[{"role":"user","content":[{"type":"text","text":"</summary>\n<result>background task completed</result>\n<usage><total_tokens>0</total_tokens></usage>"},{"type":"text","text":"please run the tests"}]}]}`,
			want: "please run the tests",
		},
		{
			name:  "quoted result without an instruction",
			body:  `{"messages":[{"role":"user","content":[{"type":"text","text":"</summary>\n<result>background task completed</result>\n<usage><total_tokens>0</total_tokens></usage>"}]}]}`,
			want: "",
		},
		{
			name:  "quoted result and instruction in string content",
			body:  `{"messages":[{"role":"user","content":"</summary>\n<result>background task completed</result>\n<usage><total_tokens>0</total_tokens></usage>please run the tests"}]}`,
			want: "please run the tests",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.RoutingFeatures().OnlyUserMessageText)
		})
	}
}
