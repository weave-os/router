// Package banned shows each comment shape the ban rejects.
package banned

/* A block comment
   counts each line. */

//go:generate echo allowed

// Exported carries a doc comment.
//
//lint:ignore U1000 directive-shaped suppressions are banned too
func Exported() {}

// AvailableTools keeps its summary banned.
//
// Deprecated: use Tools.
// The deprecated paragraph continues.
var AvailableTools = []string{}

func trailing() int {
	return 1 // trailing comment
}

// Deprecated without a colon is prose, not a marker.
var insecure = true

// SPDX-License-Identifier: Apache-2.0
