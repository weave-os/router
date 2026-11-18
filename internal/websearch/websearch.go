package websearch

import (
	"context"
	"errors"

	"github.com/tidwall/gjson"
)

const DefaultMaxResults = 10

type Query struct {
	Text       string
	MaxResults int
}

type Result struct {
	Title   string
	URL     string
	Snippet string
	PageAge string
}

type Response struct {
	Model   string
	Usage   *Usage
	Results []Result
	Summary string
}

type Executor interface {
	Search(ctx context.Context, q Query) (Response, error)
}

var ErrCallLimitExceeded = errors.New("web search max_uses exceeded")

const MaxNativeSearchOperations = 10

func CallLimit(body []byte) int {
	if value := gjson.GetBytes(body, "tools.0.max_uses").Int(); value > 0 {
		if value > MaxNativeSearchOperations {
			return MaxNativeSearchOperations
		}
		return int(value)
	}
	return 1
}
