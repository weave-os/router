package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResponsesFeedbackPartClassifiesFragmentedNotices(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		answer bool
	}{
		{"blank", " \n\t", false},
		{"ordinary answer", "answer", true},
		{"router notice", routerNoticeSentinel + "selected model\n\n", false},
		{"notice with whitespace", " \n" + routerNoticeSentinel + "selected model\n\n \t", false},
		{"legacy notice", legacyRoutingMarkerPrefix + "model\n\n", false},
		{"multiple notices", routerNoticeSentinel + "first\n\n" + legacyRoutingMarkerPrefix + "second\n\n", false},
		{"notice and answer", routerNoticeSentinel + "selected model\n\nanswer", true},
		{"legacy notice and answer", legacyRoutingMarkerPrefix + "model\n\nanswer", true},
		{"answer mentions a notice", "The prefix is " + routerNoticeSentinel + "example\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var part responsesFeedbackPart
			for _, character := range tc.text {
				part.append(string(character))
			}
			assert.Equal(t, tc.answer, part.answerSeen)
			assert.Equal(t, tc.text, part.text.String())
		})
	}
}
