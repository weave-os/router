package main

import (
	"context"
	"fmt"
	"os"

	"weave-os/router/internal/config"
)

func main() {
	config.ConfigureLogging()
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
