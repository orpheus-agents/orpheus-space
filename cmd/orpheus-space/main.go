package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/orpheus-agents/orpheus-space/internal/cli"
)

var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv, version)
	cancel()
	os.Exit(code)
}
