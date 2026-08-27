package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/goslynn/awsacademycli/internal/awscreds"
	"github.com/goslynn/awsacademycli/internal/ui"
	"github.com/spf13/cobra"
)

func newDefaultProfileCmd() *cobra.Command {
	var undo bool
	cmd := &cobra.Command{
		Use:   "default-profile",
		Short: "Use the lab as the default AWS profile",
		Long: `Points the "default" profile in ~/.aws/config at this credential provider.

This is the portable way to avoid typing --profile on every command: the
default profile lives in an AWS configuration file, so it behaves the same on
any distribution, with any shell, and on macOS and Windows too. It uses no
environment variables and does not touch your shell startup files.

After this, both forms work:

  aws sts get-caller-identity
  aws sts get-caller-identity --profile academy

To undo it:

  awsacademy default-profile --undo`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			if undo {
				return undoDefaultProfile()
			}
			return applyDefaultProfile(cmd.Context(), app.cfg.Region, false)
		},
	}
	cmd.Flags().BoolVar(&undo, "undo", false, "stop being the default profile")
	return cmd
}

// applyDefaultProfile points the default profile at this binary.
func applyDefaultProfile(ctx context.Context, region string, assumeYes bool) error {
	command := selfCommand()

	if awscreds.IsDefaultProfileOurs(command) {
		fmt.Fprintf(os.Stderr, "%s you are already the default profile in %s\n",
			mark(true), awscreds.ConfigPath())
		return nil
	}

	// Never clobber silently: the default profile may be someone's work
	// profile, and breaking it without warning would be far worse than having
	// to pass --profile.
	if conflict := awscreds.DefaultProfileConflict(command); conflict != "" {
		fmt.Fprintf(os.Stderr, `
The default AWS profile is already in use: %s

If I replace it, the AWS commands that today carry no --profile would start
using the lab.

`, conflict)
		ok, err := ui.Confirm(ctx, os.Stdin, "Replace it anyway? [y/N]: ", false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "Nothing touched. Keep using --profile academy.")
			return nil
		}
	} else if !assumeYes {
		fmt.Fprintf(os.Stderr, `
I can point the default AWS profile at your lab, so that you do not have to
type --profile on every command.

It is done in %s, so it works with any shell and on any
system; it does not touch environment variables.

`, awscreds.ConfigPath())
		ok, err := ui.Confirm(ctx, os.Stdin, "Configure it? [y/N]: ", false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "All right. You can do it later with 'awsacademy default-profile'.")
			return nil
		}
	}

	if err := awscreds.ConfigureDefaultProfile(command, region); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s default profile configured in %s\n",
		mark(true), awscreds.ConfigPath())
	fmt.Fprintln(os.Stderr, "  You can now use 'aws' without --profile.")
	return nil
}

func undoDefaultProfile() error {
	removed, err := awscreds.RemoveDefaultProfile(selfCommand())
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintf(os.Stderr, "%s the default profile did not point at this tool\n", mark(true))
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s you are no longer the default profile\n", mark(true))
	return nil
}
