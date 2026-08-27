package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/goslynn/awsacademycli/internal/config"
	"github.com/goslynn/awsacademycli/internal/ui"
	"github.com/spf13/cobra"
)

// errQuiet marks a failure whose detail has already been shown: it changes the
// exit code without printing a second message.
var errQuiet = errors.New("")

// Global options, shared by every subcommand.
var (
	flagJSON      bool
	flagDebugHTTP bool
)

// ExecuteContext runs the CLI. It returns the process exit code.
func ExecuteContext(ctx context.Context, version string) int {
	root := newRootCmd(version)
	if err := root.ExecuteContext(ctx); err != nil {
		if errors.Is(err, errQuiet) {
			return 1
		}
		// Cancelling is not a failure: no error is printed and we exit with
		// the conventional code for an interrupt (128 + SIGINT).
		if errors.Is(err, ui.ErrCancelled) || errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "cancelled")
			return 130
		}
		// Cobra already printed usage errors; the rest we report ourselves
		// with a hint about what to do next.
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if errors.Is(err, config.ErrNotConfigured) {
			fmt.Fprintln(os.Stderr, "\nStart here:\n  awsacademy setup")
		}
		return 1
	}
	return 0
}

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:     "awsacademy",
		Version: version,
		Short:   "Control the AWS Academy Learner Lab from the terminal",
		Long: `awsacademy brings the AWS Academy Learner Lab up and down and keeps
your AWS CLI profile stocked with fresh credentials.

It does the usual round trip for you: log in to Canvas, open the lab, press
Start Lab, wait until it is ready and copy the credentials. Lab credentials
only last a few hours, so that round trip repeats several times a day; this
reduces it to a single command.

To get started:

  awsacademy setup     once, saves your AWS Academy credentials
  awsacademy start     brings the lab up and refreshes the AWS profile
  awsacademy status    tells you whether you can work and how much time is left
  awsacademy courses   lists your courses, in case you have more than one
  awsacademy stop      brings the lab down

The configuration lives in:
  ` + config.Path(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().BoolVar(&flagJSON, "json", false,
		"JSON output, for consumption from scripts")
	root.PersistentFlags().BoolVar(&flagDebugHTTP, "debug-http", false,
		"trace every HTTP request on stderr")

	root.AddCommand(
		newSetupCmd(),
		newLoginCmd(),
		newLogoutCmd(),
		newCoursesCmd(),
		newStatusCmd(),
		newStartCmd(),
		newStopCmd(),
		newCredsCmd(),
		newEnvCmd(),
		newDefaultProfileCmd(),
		newDebugCmd(),
	)
	return root
}
