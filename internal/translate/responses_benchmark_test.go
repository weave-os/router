package translate_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"weave-os/router/internal/translate"
)

var responsesCleanupBenchmarkSink []byte

func BenchmarkResponsesCleanup(b *testing.B) {
	for _, messageCount := range []int{128, 256, 512, 1024} {
		b.Run("unchanged/"+strconv.Itoa(messageCount), func(b *testing.B) {
			body := responsesFooterCleanBody(messageCount)
			cleanResponsesBenchmark(b, body, messageCount, "answer", true, translate.StripRoutingBadgeFromResponsesInput)
		})
		b.Run("badge-retained/"+strconv.Itoa(messageCount), func(b *testing.B) {
			body := responsesBenchmarkBody(messageCount, true, false)
			cleanResponsesBenchmark(b, body, messageCount, "answer", false, translate.StripRoutingBadgeFromResponsesInput)
		})
		b.Run("badge-only/"+strconv.Itoa(messageCount), func(b *testing.B) {
			body := responsesBenchmarkBody(messageCount, false, false)
			cleanResponsesBenchmark(b, body, 0, "", false, translate.StripRoutingBadgeFromResponsesInput)
		})
		b.Run("empty-shell/"+strconv.Itoa(messageCount), func(b *testing.B) {
			body := responsesEmptyShellBody(messageCount)
			cleanResponsesBenchmark(b, body, messageCount, "", false, translate.StripRoutingBadgeFromResponsesInput)
		})
		b.Run("footer-clean/"+strconv.Itoa(messageCount), func(b *testing.B) {
			body := responsesFooterCleanBody(messageCount)
			cleanResponsesBenchmark(b, body, messageCount, "answer", true, translate.StripFeedbackFooterFromResponsesInput)
		})
		b.Run("footer-one-message/"+strconv.Itoa(messageCount), func(b *testing.B) {
			body := responsesFooterOneMessageBody(messageCount)
			cleanResponsesBenchmark(b, body, 1, "answer", false, translate.StripFeedbackFooterFromResponsesInput)
		})
		b.Run("footer-across-messages/"+strconv.Itoa(messageCount), func(b *testing.B) {
			body := responsesFooterAcrossMessagesBody(messageCount)
			cleanResponsesBenchmark(b, body, messageCount, "answer", false, translate.StripFeedbackFooterFromResponsesInput)
		})
	}
}

func cleanResponsesBenchmark(b *testing.B, body []byte, expectedMessages int, expectedText string, expectSame bool, clean func([]byte) ([]byte, error)) {
	b.Helper()
	cleaned, err := clean(body)
	if err != nil {
		b.Fatal(err)
	}
	if len(gjson.GetBytes(cleaned, "input").Array()) != expectedMessages {
		b.Fatalf("fixture produced %d messages, want %d", len(gjson.GetBytes(cleaned, "input").Array()), expectedMessages)
	}
	if expectedText != "" && gjson.GetBytes(cleaned, "input.0.content.0.text").Str != expectedText {
		b.Fatalf("fixture produced text %q, want %q", gjson.GetBytes(cleaned, "input.0.content.0.text").Str, expectedText)
	}
	if expectSame && !bytes.Equal(cleaned, body) {
		b.Fatal("clean fixture changed")
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cleaned, err := clean(body)
		if err != nil {
			b.Fatal(err)
		}
		responsesCleanupBenchmarkSink = cleaned
	}
}

func responsesEmptyShellBody(count int) []byte {
	var body strings.Builder
	body.WriteString(`{"input":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]},`)
		body.WriteString(`{"type":"function_call","call_id":"call_`)
		body.WriteString(strconv.Itoa(i))
		body.WriteString(`","name":"run","arguments":"{}"}`)
	}
	body.WriteString(`]}`)
	return []byte(body.String())
}

func responsesFooterCleanBody(count int) []byte {
	var body strings.Builder
	body.WriteString(`{"input":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`)
	}
	body.WriteString(`]}`)
	return []byte(body.String())
}

func responsesBenchmarkBody(count int, retainBody, footer bool) []byte {
	const badge = "\u2063\u2060\u2063\u2060**Weave Router** — model-a ← model-b\n\n"
	const feedback = "\n\n_Weave Router feedback:_ `$rf +` good · `$rf -` poor"
	var body strings.Builder
	body.WriteString(`{"input":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		text := badge
		if retainBody {
			text += "answer"
		}
		if footer {
			text = "answer" + feedback
		}
		body.WriteString(`{"type":"message","role":"assistant","content":[`)
		body.WriteString(`{"type":"output_text","text":`)
		body.WriteString(strconv.Quote(text))
		body.WriteString(`}]}`)
	}
	body.WriteString(`]}`)
	return []byte(body.String())
}

func responsesFooterAcrossMessagesBody(count int) []byte {
	const feedback = "\n\n_Weave Router feedback:_ `$rf +` good · `$rf -` poor"
	var body strings.Builder
	body.WriteString(`{"input":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":`)
		body.WriteString(strconv.Quote("answer" + feedback))
		body.WriteString(`}]}`)
	}
	body.WriteString(`]}`)
	return []byte(body.String())
}

func responsesFooterOneMessageBody(partCount int) []byte {
	const feedback = "\n\n_Weave Router feedback:_ `$rf +` good · `$rf -` poor"
	var body strings.Builder
	body.WriteString(`{"input":[{"type":"message","role":"assistant","content":[`)
	for i := 0; i < partCount; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`{"type":"output_text","text":`)
		body.WriteString(strconv.Quote("answer" + feedback))
		body.WriteString(`}`)
	}
	body.WriteString(`]}]}`)
	return []byte(body.String())
}
