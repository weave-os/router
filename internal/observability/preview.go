package observability

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

func Preview(s string, n int) string {
	if s == "" || n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	limit := n - len(ellipsis)
	if limit <= 0 {
		return trimToRuneBoundary(s[:n])
	}
	cut := trimToRuneBoundary(s[:limit])
	if i := strings.LastIndexAny(cut, " \n\t"); i > n/2 {
		cut = cut[:i]
	}
	return cut + ellipsis
}

func trimToRuneBoundary(s string) string {
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
