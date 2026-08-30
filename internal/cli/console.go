package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/goslynn/awsacademycli/internal/awscreds"
	"github.com/goslynn/awsacademycli/internal/browser"
	"github.com/spf13/cobra"
)

func newConsoleCmd() *cobra.Command {
	var (
		printOnly bool
		region    string
	)
	cmd := &cobra.Command{
		Use:     "console [service|url]",
		Aliases: []string{"web"},
		Short:   "Open the lab's AWS console in the browser",
		Long: `Opens the AWS Management Console of the running lab, already signed in.

This is the console itself, not the Vocareum page that has the Start Lab
button: the lab credentials are exchanged for a browser session through AWS's
federation endpoint, which is the same thing the lab's own "AWS" button does.

It uses the credentials already saved; if they have expired it fetches fresh
ones from the lab. It does not bring the lab up: if it is stopped, run
'awsacademy start' first.

With an argument it opens straight at a service, so the first click is one you
do not have to make:

  awsacademy console            the console home
  awsacademy console ec2        the EC2 console
  awsacademy console s3         the S3 console
  awsacademy console https://…  any console URL

The sign-in URL carries a token that grants access to the lab account. --print
writes it on stdout instead of opening it; treat it like a password.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			creds, err := app.LabCredentials(ctx)
			if err != nil {
				return err
			}

			// The console has to open where the resources are, so the region is
			// taken from the same places the AWS CLI would look, in the same order.
			if region == "" {
				region = awscreds.ProfileRegion(app.cfg.AWSProfile)
			}
			if region == "" {
				region = creds.Region
			}
			if region == "" {
				region = app.cfg.Region
			}

			var service string
			if len(args) == 1 {
				service = args[0]
			}
			destination := awscreds.ConsoleDestination(region, service)

			progress("signing in to the AWS console…")
			url, err := awscreds.ConsoleURL(ctx, creds, destination)
			if err != nil {
				return err
			}

			if flagJSON {
				out := map[string]any{"url": url, "region": region, "opened": false}
				if !printOnly {
					if err := browser.Open(url); err != nil {
						out["error"] = err.Error()
					} else {
						out["opened"] = true
					}
				}
				return printJSON(out)
			}

			if printOnly {
				// Alone on stdout, so it can be piped straight into a browser.
				fmt.Println(url)
				return nil
			}

			openErr := browser.Open(url)
			if openErr != nil {
				// Not a failure: the URL is what the user actually wanted, and
				// on a machine with no desktop it is the only useful answer.
				if errors.Is(openErr, browser.ErrUnavailable) {
					fmt.Fprintln(os.Stderr, "No browser here; open this URL yourself:")
				} else {
					fmt.Fprintf(os.Stderr, "Could not open the browser (%v). The URL is:\n", openErr)
				}
				fmt.Println(url)
				return nil
			}

			fmt.Printf("AWS console opened in the browser (region %s).\n", region)
			fmt.Printf("  %s the session lasts as long as the lab does\n", mark(true))
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false,
		"print the sign-in URL instead of opening it")
	cmd.Flags().StringVar(&region, "region", "",
		"region the console opens on (defaults to the profile's)")
	return cmd
}
