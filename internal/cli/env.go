package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newEnvCmd() *cobra.Command {
	var unset bool
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Print the command that sets AWS_PROFILE in the current session",
		Long: `Writes on stdout the command that exports AWS_PROFILE, so it can be evaluated:

  eval "$(awsacademy env)"

A program cannot modify the variables of its parent shell, so the export has to
be run by the shell itself. This affects only the current session.

To avoid typing --profile permanently, prefer:

  awsacademy default-profile

which resolves it in the AWS configuration and therefore works with any shell
and on any system, without environment variables.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if unset {
				fmt.Println(unsetLine(detectShell()))
				return nil
			}
			app, err := newApp(false)
			if err != nil {
				return err
			}
			fmt.Println(exportLine(detectShell(), app.cfg.AWSProfile))
			return nil
		},
	}
	cmd.Flags().BoolVar(&unset, "unset", false, "print the command that removes the variable")
	return cmd
}

// detectShell looks at $SHELL to choose the syntax. fish is the only one in
// common use that does not understand `export VAR=value`.
func detectShell() string {
	if strings.Contains(os.Getenv("SHELL"), "fish") {
		return "fish"
	}
	return "posix"
}

func exportLine(shell, profile string) string {
	if shell == "fish" {
		return fmt.Sprintf("set -gx AWS_PROFILE %s", profile)
	}
	return fmt.Sprintf("export AWS_PROFILE=%s", profile)
}

func unsetLine(shell string) string {
	if shell == "fish" {
		return "set -e AWS_PROFILE"
	}
	return "unset AWS_PROFILE"
}
