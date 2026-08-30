package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goslynn/awsacademycli/internal/awscreds"
	"github.com/goslynn/awsacademycli/internal/canvas"
	"github.com/goslynn/awsacademycli/internal/state"
	"github.com/goslynn/awsacademycli/internal/ui"
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
	// Neither the countdown nor the spend travels in the status response, and
	// each lives at its own endpoint, so both are asked for only when the lab
	// is up and there is something to report. A failure in either is not
	// allowed to sink the rest of the report.
	if st.Running() {
		if detail, _, err := lab.Details(ctx); err == nil {
			st = detail
		}
		if budget, err := lab.Budget(ctx); err == nil {
			st.BudgetUsed, st.BudgetTotal = budget.Used, budget.Total
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
		printBudget("    ", r.Lab.BudgetUsed, r.Lab.BudgetTotal)
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

// budgetWidth is how wide the spend gauge is drawn. Twenty cells give a
// resolution of 5%, which is as much as the number below it deserves.
const budgetWidth = 20

// printBudget draws the lab spend as a gauge.
//
// The budget is the other clock: when it runs out the lab stops, and unlike the
// session countdown it does not come back the next day. A bar shows how close
// that is in a way that two dollar figures do not.
func printBudget(indent string, used, total float64) {
	const label = "budget       "

	if total <= 0 {
		// Some labs publish the spend without the cap. The number alone is
		// still worth printing; a gauge without a maximum is not.
		if used > 0 {
			fmt.Printf("%s%s$%.2f used\n", indent, label, used)
		}
		return
	}

	fraction := used / total
	bar := ui.Paint(budgetColour(fraction), ui.Bar(fraction, budgetWidth))
	fmt.Printf("%s%s%s %3.0f%%  $%.2f of $%.2f\n",
		indent, label, bar, fraction*100, used, total)

	if fraction >= 0.9 {
		// The spend is reported with some delay, so it can land above the cap;
		// by then the warning is about something that has already happened.
		note := fmt.Sprintf("$%.2f left: the lab stops when the budget runs out", total-used)
		if used >= total {
			note = "no budget left: the lab will not come back up"
		}
		// Aligned under the bar, because it is the bar it is explaining.
		fmt.Printf("%s%s%s\n", indent, strings.Repeat(" ", len(label)), ui.Paint(ui.Red, note))
	}
}

// budgetColour maps how much of the budget is gone onto the usual three
// levels. The thresholds are deliberately pessimistic: by the time 75% is
// spent it is worth knowing, not when there is nothing left.
func budgetColour(fraction float64) ui.Colour {
	switch {
	case fraction >= 0.9:
		return ui.Red
	case fraction >= 0.75:
		return ui.Yellow
	default:
		return ui.Green
	}
}
