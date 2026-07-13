package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/langrenjh-alt/GROK-GO/internal/app"
	"github.com/langrenjh-alt/GROK-GO/internal/buildinfo"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		info := buildinfo.Current()
		fmt.Printf("grok-go %s (%s, %s)\n", info.Version, info.Commit, info.Date)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "grok-go:", err)
		os.Exit(1)
	}
}
