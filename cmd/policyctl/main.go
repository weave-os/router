package main

import (
	"context"
	"fmt"
	"os"

	"weave-os/router/cmd/logconfig"
)

func main() {
	logconfig.Configure()
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
