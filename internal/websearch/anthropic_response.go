package websearch

import (
	"fmt"
	"strings"
)

// resultBlockType is the block Anthropic wraps each hit in inside a
// web_search_tool_result.
const resultBlockType = "web_search_result"

// Message is a synthesized Anthropic Messages response for a search turn the
// router executed itself. Field order mirrors the wire shape.
type Message struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Role         string  `json:"role"`
	Model        string  `json:"model"`
	Content      []Block `json:"content"`
	StopReason   string  `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
	Usage        Usage   `json:"usage"`
}

// Block is one content block of a synthesized message.
type Block struct {
	Type string `json:"type"`
	// Text is set on text blocks.
	Text string `json:"text,omitempty"`
	// ID and Input are set on server_tool_use blocks.
	ID    string       `json:"id,omitempty"`
	Name  string       `json:"name,omitempty"`
	Input *SearchInput `json:"input,omitempty"`
	// ToolUseID and Content are set on web_search_tool_result blocks.
	ToolUseID string        `json:"tool_use_id,omitempty"`
	Content   []ResultBlock `json:"content,omitempty"`
}

// SearchInput is the input recorded on a server_tool_use block.
type SearchInput struct {
	Query string `json:"query"`
}

// ResultBlock is one hit inside a web_search_tool_result block.
type ResultBlock struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	PageAge string `json:"page_age,omitempty"`
}

// Usage reports the synthesized message's token accounting. Server-tool
// requests are counted separately by Anthropic; we report the searches we ran
// so a client rendering usage sees a non-zero count.
type Usage struct {
	InputTokens   int              `json:"input_tokens"`
	OutputTokens  int              `json:"output_tokens"`
	ServerToolUse *ServerToolUsage `json:"server_tool_use,omitempty"`
}

// ServerToolUsage counts server-side tool invocations.
type ServerToolUsage struct {
	WebSearchRequests int `json:"web_search_requests"`
}

// SynthesizeMessage builds the Anthropic response for a search the router
// executed: the server_tool_use the model would have emitted, the results
// Anthropic would have appended, and the answer text.
func SynthesizeMessage(msgID, model, toolName string, q Query, resp Response, inputTokens int) Message {
	if toolName == "" {
		toolName = "web_search"
	}
	toolUseID := "srvtoolu_" + strings.TrimPrefix(msgID, "msg_")
	results := make([]ResultBlock, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.URL == "" {
			continue
		}
		title := r.Title
		if title == "" {
			title = r.URL
		}
		results = append(results, ResultBlock{Type: resultBlockType, Title: title, URL: r.URL, PageAge: r.PageAge})
	}
	text := answerText(resp)
	return Message{
		ID:    msgID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
		Content: []Block{
			{Type: "server_tool_use", ID: toolUseID, Name: toolName, Input: &SearchInput{Query: q.Text}},
			{Type: "web_search_tool_result", ToolUseID: toolUseID, Content: results},
			{Type: "text", Text: text},
		},
		StopReason: "end_turn",
		Usage: Usage{
			InputTokens:   inputTokens,
			OutputTokens:  len(text) / 4,
			ServerToolUse: &ServerToolUsage{WebSearchRequests: 1},
		},
	}
}

// answerText renders the assistant prose for a search turn: the backend's own
// synthesis when it produced one, else a linked digest of the hits so the
// caller always gets citable text.
func answerText(resp Response) string {
	var b strings.Builder
	if summary := strings.TrimSpace(resp.Summary); summary != "" {
		b.WriteString(summary)
	}
	if len(resp.Results) == 0 {
		if b.Len() == 0 {
			return "No web search results were found for this query."
		}
		return b.String()
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("Sources:")
	for i, r := range resp.Results {
		if r.URL == "" {
			continue
		}
		title := r.Title
		if title == "" {
			title = r.URL
		}
		fmt.Fprintf(&b, "\n%d. %s — %s", i+1, title, r.URL)
		if snippet := strings.TrimSpace(r.Snippet); snippet != "" {
			fmt.Fprintf(&b, "\n   %s", snippet)
		}
	}
	return b.String()
}

// TextOf returns the message's assistant prose, for logging and tests.
func (m Message) TextOf() string {
	for _, block := range m.Content {
		if block.Type == "text" {
			return block.Text
		}
	}
	return ""
}
