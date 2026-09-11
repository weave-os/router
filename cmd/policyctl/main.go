// Command policyctl compiles, validates, publishes, promotes, rolls back, and
// inspects atomic Go-owned HMM policy releases.
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
