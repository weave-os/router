//go:build !windows

package allowed

import _ "embed"

//go:embed allowed.go
var source string

// Deprecated: use source.
var Source = source

func insecure() bool {
	return true // codeql[go/disabled-certificate-check]
}
