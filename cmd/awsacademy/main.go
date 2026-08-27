// Command awsacademy controls the AWS Academy Learner Lab from the terminal.
//
// It brings the lab up and down and keeps the AWS CLI profile stocked with
// fresh credentials, following underneath the same round trip a person with a
// browser would: Canvas login, LTI launch and lab control on Vocareum.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/goslynn/awsacademycli/internal/cli"
)

// version is set by the linker in release builds:
//
//	go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/awsacademy
var version = "dev"

func main() {
	// The first Ctrl+C cancels the work in progress, which may be a wait of
	// several minutes, and lets the tool exit in an orderly fashion.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Installing a handler disables the default termination, so if something
	// failed to honour the cancellation the process would hang and become
	// unkillable with Ctrl+C. Restoring the normal behaviour as soon as the
	// first signal arrives makes a second Ctrl+C always work.
	go func() {
		<-ctx.Done()
		stop()
	}()

	os.Exit(cli.ExecuteContext(ctx, version))
}
