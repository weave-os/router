package toolcheck

import "strings"

// Deleting a non-first member retains whitespace before its comma. Immutable
// gap joins keep successive deletions from repeatedly copying that whitespace.
type argumentGap struct {
	raw         string
	left, right *argumentGap
}

func joinArgumentGaps(left, right *argumentGap) *argumentGap {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	return &argumentGap{left: left, right: right}
}

func (gap *argumentGap) writeTo(output *strings.Builder) {
	if gap == nil {
		return
	}
	gap.left.writeTo(output)
	output.WriteString(gap.raw)
	gap.right.writeTo(output)
}
