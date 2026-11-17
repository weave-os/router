package websearch

import (
	"fmt"
	"strings"
)

const resultBlockType = "web_search_result"

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

type Block struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     *SearchInput   `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   *[]ResultBlock `json:"content,omitempty"`
}

type SearchInput struct {
	Query string `json:"query"`
}

type ResultBlock struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	PageAge string `json:"page_age,omitempty"`
}

type Usage struct {
	CacheReadInputTokens int              `json:"cache_read_input_tokens,omitempty"`
	InputTokens          int              `json:"input_tokens"`
	OutputTokens         int              `json:"output_tokens"`
	ServerToolUse        *ServerToolUsage `json:"server_tool_use,omitempty"`
}

type ServerToolUsage struct {
	WebSearchRequests int `json:"web_search_requests"`
}

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
	usage := Usage{InputTokens: inputTokens, OutputTokens: len(text) / 4, ServerToolUse: &ServerToolUsage{WebSearchRequests: 1}}
	if resp.Usage != nil {
		usage = *resp.Usage
	}
	return Message{
		ID:    msgID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
		Content: []Block{
			{Type: "server_tool_use", ID: toolUseID, Name: toolName, Input: &SearchInput{Query: q.Text}},
			{Type: "web_search_tool_result", ToolUseID: toolUseID, Content: &results},
			{Type: "text", Text: text},
		},
		StopReason: "end_turn",
		Usage:      usage,
	}
}

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

func (m Message) TextOf() string {
	for _, block := range m.Content {
		if block.Type == "text" {
			return block.Text
		}
	}
	return ""
}
