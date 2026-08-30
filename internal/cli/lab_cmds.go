package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/goslynn/awsacademycli/internal/awscreds"
	"github.com/goslynn/awsacademycli/internal/state"
	"github.com/goslynn/awsacademycli/internal/vocareum"
	"github.com/spf13/cobra"
)

func newStartCmd() *cobra.Command {
	var (
		timeout     time.Duration
		writeShared bool
		noWait      bool
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Bring the lab up and refresh the AWS credentials",
		Long: `Brings the Learner Lab up, waits until it is ready and saves its credentials.

It is idempotent: if the lab is already running it does not restart it, it only
refreshes the credentials. You can call it as many times as you like.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			lab, disc, err := app.OpenLab(ctx)
			if err != nil {
				return err
			}
			progress("lab: %s", disc.CourseName)

			st, err := lab.Status(ctx)
			if err != nil {
				return err
			}

			if st.Running() {
				progress("already running, refreshing the credentials")
			} else {
				progress("starting…")
				if err := lab.Start(ctx); err != nil {
					return err
				}
				if noWait {
					if flagJSON {
						return printJSON(map[string]any{"state": string(vocareum.StateStarting)})
					}
					fmt.Println("Start requested. Check the progress with: awsacademy status")
					return nil
				}
				st, err = lab.WaitForRunning(ctx, timeout, func(s vocareum.Status) {
					progress("  state: %s", s.State)
				})
				if err != nil {
					return err
				}
			}

			// Vocareum serves credentials and countdown in the same response.
			detail, creds, err := lab.Details(ctx)
			if err != nil {
				return err
			}
			st = detail
			if budget, err := lab.Budget(ctx); err == nil {
				st.BudgetUsed, st.BudgetTotal = budget.Used, budget.Total
			}
			if creds.Region == "" {
				creds.Region = app.cfg.Region
			}
			if err := creds.Save(); err != nil {
				return err
			}

			profile := app.cfg.AWSProfile
			target := "credential_process"
			// We write the shared file if the user asked for it or if that is
			// how their profile is configured; otherwise the cache is enough.
			if writeShared || awscreds.CredentialProcessCommand(profile) == "" {
				if err := awscreds.WriteSharedCredentials(profile, creds); err != nil {
					return err
				}
				target = awscreds.CredentialsPath()
			}

			return reportStart(ctx, app, st, creds, profile, target)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute,
		"how long to wait for the lab to be ready")
	cmd.Flags().BoolVar(&writeShared, "write-credentials", false,
		"also write the profile into ~/.aws/credentials")
	cmd.Flags().BoolVar(&noWait, "no-wait", false,
		"request the start and exit without waiting")
	return cmd
}

func reportStart(ctx context.Context, app *App, st *vocareum.Status,
	creds *state.Credentials, profile, target string) error {

	region := awscreds.ProfileRegion(profile)
	if region == "" {
		region = app.cfg.Region
	}
	identity, valErr := awscreds.Validate(ctx, creds, region)

	if flagJSON {
		out := map[string]any{
			"state":        string(st.State),
			"profile":      profile,
			"written_to":   target,
			"remaining":    st.Remaining.Round(time.Second).String(),
			"budget_used":  st.BudgetUsed,
			"budget_total": st.BudgetTotal,
		}
		if identity != nil {
			out["arn"] = identity.ARN
			out["account"] = identity.Account
		}
		if valErr != nil {
			out["validation_error"] = valErr.Error()
		}
		return printJSON(out)
	}

	fmt.Printf("\nLab ready.\n")
	if st.Remaining > 0 {
		fmt.Printf("  remaining    %s of session\n", st.Remaining.Round(time.Second))
	}
	printBudget("  ", st.BudgetUsed, st.BudgetTotal)
	fmt.Printf("  profile      %s -> %s\n", profile, target)
	if valErr != nil {
		// Not fatal: the credentials are saved and this may be a network
		// problem, but the user has to hear about it.
		fmt.Printf("  %s could not verify them against AWS: %v\n", mark(false), valErr)
		return nil
	}
	fmt.Printf("  %s %s\n", mark(true), identity.ARN)
	return nil
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the lab",
		Long: `Stops the Learner Lab.

The resources you created survive between sessions, but the credentials stop
working until the next 'start'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			lab, _, err := app.OpenLab(ctx)
			if err != nil {
				return err
			}
			if err := lab.Stop(ctx); err != nil {
				return err
			}

			// The cached credentials are now worthless: keeping them would only
			// make the next command fail in a confusing way.
			if creds, err := state.LoadCredentials(); err == nil {
				creds.Expiration = time.Now()
				_ = creds.Save()
			}

			if flagJSON {
				return printJSON(map[string]any{"state": string(vocareum.StateStopping)})
			}
			fmt.Println("Lab stopped.")
			return nil
		},
	}
}

func newCredsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "creds",
		Short: "Emit the credentials in the credential_process format",
		Long: `Writes the lab credentials as JSON on stdout, in the format the AWS
CLI's credential_process expects.

It is meant to be invoked by the AWS CLI, not used by hand. It is declared in
~/.aws/config:

  [profile academy]
  credential_process = awsacademy creds

It does not bring the lab up when it is down: every 'aws' command would hang
for several minutes waiting. It fails fast and tells you what to do.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			creds, err := state.LoadCredentials()
			if err != nil || creds.Expired() {
				// The user sees this message through the AWS CLI, so it has to
				// name the exact command that fixes it.
				return fmt.Errorf(
					"no valid lab credentials: run 'awsacademy start'")
			}
			out, err := awscreds.ProcessOutput(creds)
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		},
	}
}

// progress reports progress on stderr, so as not to pollute a --json output
// that a script may be piping.
func progress(format string, args ...any) {
	if flagJSON {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
