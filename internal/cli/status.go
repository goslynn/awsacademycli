package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/goslynn/awsacademycli/internal/awscreds"
	"github.com/goslynn/awsacademycli/internal/canvas"
	"github.com/goslynn/awsacademycli/internal/state"
	"github.com/goslynn/awsacademycli/internal/vocareum"
	"github.com/spf13/cobra"
)

// ok reports whether everything is in order: live session, running lab and
// working credentials. It governs the exit code, so that one can write
// `awsacademy status --json >/dev/null || awsacademy start`.
func (r *statusReport) ok() bool {
	return r.Auth.Authenticated && r.Lab.State == string(vocareum.StateRunning) && r.AWS.Valid
}

// statusReport is what `status` reports, across the three layers that can fail
// independently: the session, the lab and the AWS credentials.
type statusReport struct {
	Auth struct {
		Authenticated bool   `json:"authenticated"`
		User          string `json:"user,omitempty"`
		Error         string `json:"error,omitempty"`
	} `json:"auth"`

	Lab struct {
		State string `json:"state"`
		// RemainingSeconds is what is left of the session: the number that
		// decides whether to start something long now or bring the lab up again.
		RemainingSeconds int     `json:"remaining_seconds,omitempty"`
		Remaining        string  `json:"remaining,omitempty"`
		BudgetUsed       float64 `json:"budget_used,omitempty"`
		BudgetTotal      float64 `json:"budget_total,omitempty"`
		Course           string  `json:"course,omitempty"`
		Error            string  `json:"error,omitempty"`
	} `json:"lab"`

	AWS struct {
		Profile string `json:"profile"`
		// Source says where the credentials would come from: the shared file
		// or this binary via credential_process.
		Source  string `json:"source"`
		Valid   bool   `json:"valid"`
		ARN     string `json:"arn,omitempty"`
		Account string `json:"account,omitempty"`
		Error   string `json:"error,omitempty"`
	} `json:"aws"`
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the state of the session, the lab and the credentials",
		Long: `Reports the three things that can be wrong, separately:

  auth   whether the AWS Academy session is still alive
  lab    whether the lab is up and how much session time is left
  aws    whether the AWS CLI profile has credentials that really work,
         checked against sts:GetCallerIdentity

It exits with code 0 only if all three are fine, so it can be chained:

  awsacademy status --json >/dev/null || awsacademy start`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := collectStatus(cmd.Context())
			if flagJSON {
				if err := printJSON(report); err != nil {
					return err
				}
			} else {
				printStatus(report)
			}
			if !report.ok() {
				// The detail was already printed; an extra error would only
				// repeat it, but the exit code has to reflect it.
				return errQuiet
			}
			return nil
		},
	}
}

// collectStatus gathers the report without aborting on the first failure: a
// dead session does not stop us from saying what is going on with the AWS
// credentials, and seeing the three layers together is exactly what makes
// diagnosis possible.
func collectStatus(ctx context.Context) *statusReport {
	r := &statusReport{}
	r.Lab.State = string(vocareum.StateUnknown)

	app, err := newApp(flagDebugHTTP)
	if err != nil {
		// Without configuration nothing can be said about the other two
		// layers; saying so is more useful than leaving them blank.
		r.Auth.Error = err.Error()
		r.Lab.Error = "not configured"
		r.AWS.Profile = "?"
		r.AWS.Error = "not configured"
		return r
	}
	r.AWS.Profile = app.cfg.AWSProfile

	user, err := app.EnsureSession(ctx)
	switch {
	case err == nil:
		r.Auth.Authenticated = true
		r.Auth.User = user.Name
	case errors.Is(err, canvas.ErrInvalidCredentials):
		r.Auth.Error = "credentials rejected by AWS Academy"
	default:
		r.Auth.Error = err.Error()
	}

	if r.Auth.Authenticated {
		collectLabStatus(ctx, app, r)
	} else {
		r.Lab.Error = "no session"
	}

	collectAWSStatus(ctx, app, r)
	return r
}

func collectLabStatus(ctx context.Context, app *App, r *statusReport) {
	lab, disc, err := app.OpenLab(ctx)
	if err != nil {
		r.Lab.Error = err.Error()
		return
	}
	r.Lab.Course = disc.CourseName

	st, err := lab.Status(ctx)
	if err != nil {
		r.Lab.Error = err.Error()
		return
	}
	// The countdown does not travel in the status response, only alongside the
	// credentials, so it is requested only when there is something to count.
	if st.Running() {
		if detail, _, err := lab.Details(ctx); err == nil {
			st = detail
		}
	}

	r.Lab.State = string(st.State)
	if st.Remaining > 0 {
		r.Lab.RemainingSeconds = int(st.Remaining.Seconds())
		r.Lab.Remaining = st.Remaining.Round(time.Second).String()
	}
	r.Lab.BudgetUsed, r.Lab.BudgetTotal = st.BudgetUsed, st.BudgetTotal
}

func collectAWSStatus(ctx context.Context, app *App, r *statusReport) {
	profile := r.AWS.Profile
	if profile == "?" {
		r.AWS.Error = "not configured"
		return
	}

	// We validate the source the AWS CLI would actually use. With
	// credential_process active, that is this binary's cache; in classic mode,
	// whatever is written in ~/.aws/credentials.
	var creds *state.Credentials
	var err error
	if awscreds.CredentialProcessCommand(profile) != "" && !awscreds.HasStaticCredentials(profile) {
		r.AWS.Source = "credential_process"
		creds, err = state.LoadCredentials()
	} else {
		r.AWS.Source = awscreds.CredentialsPath()
		creds, err = awscreds.ReadSharedCredentials(profile)
	}
	if err != nil {
		r.AWS.Error = fmt.Sprintf("no credentials for profile %q", profile)
		return
	}
	if creds.Expired() {
		r.AWS.Error = "the saved credentials have already expired"
		return
	}

	region := awscreds.ProfileRegion(profile)
	if region == "" {
		region = app.cfg.Region
	}
	identity, err := awscreds.Validate(ctx, creds, region)
	if err != nil {
		r.AWS.Error = err.Error()
		return
	}
	r.AWS.Valid = true
	r.AWS.ARN = identity.ARN
	r.AWS.Account = identity.Account
}

func printStatus(r *statusReport) {
	fmt.Println("AUTH")
	if r.Auth.Authenticated {
		fmt.Printf("  %s session alive as %s\n", mark(true), r.Auth.User)
	} else {
		fmt.Printf("  %s %s\n", mark(false), r.Auth.Error)
	}

	fmt.Println("\nLAB")
	switch {
	case r.Lab.Error != "":
		fmt.Printf("  %s %s\n", mark(false), r.Lab.Error)
	default:
		running := r.Lab.State == string(vocareum.StateRunning)
		fmt.Printf("  %s %s\n", mark(running), r.Lab.State)
		if r.Lab.Course != "" {
			fmt.Printf("    course       %s\n", r.Lab.Course)
		}
		if r.Lab.Remaining != "" {
			fmt.Printf("    remaining    %s of session\n", r.Lab.Remaining)
		}
		if r.Lab.BudgetTotal > 0 {
			fmt.Printf("    budget       $%.2f of $%.2f\n", r.Lab.BudgetUsed, r.Lab.BudgetTotal)
		}
	}

	fmt.Println("\nAWS CLI")
	fmt.Printf("    profile      %s\n", r.AWS.Profile)
	if r.AWS.Source != "" {
		fmt.Printf("    source       %s\n", r.AWS.Source)
	}
	if r.AWS.Valid {
		fmt.Printf("  %s valid credentials\n", mark(true))
		fmt.Printf("    arn          %s\n", r.AWS.ARN)
		fmt.Printf("    account      %s\n", r.AWS.Account)
	} else {
		fmt.Printf("  %s %s\n", mark(false), r.AWS.Error)
	}
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// printJSON writes a structure as indented JSON on stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
