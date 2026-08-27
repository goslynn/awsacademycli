package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/goslynn/awsacademycli/internal/awscreds"
	"github.com/goslynn/awsacademycli/internal/canvas"
	"github.com/goslynn/awsacademycli/internal/config"
	"github.com/goslynn/awsacademycli/internal/httpx"
	"github.com/goslynn/awsacademycli/internal/state"
	"github.com/goslynn/awsacademycli/internal/ui"
	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	var (
		courseID    string
		email       string
		profile     string
		region      string
		useProcess  bool
		skipProcess bool
		useDefault  bool
		skipDefault bool
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure the tool for the first time",
		Long: `Saves your AWS Academy credentials and locates your lab.

It asks for the username and the password, checks that they work by logging in
for real, looks up the Learner Lab course and item in your account, and leaves
everything saved in ` + config.Path() + ` with permissions 0600.

It also offers two conveniences, both optional:

  - configuring credential_process in ~/.aws/config, which is the recommended
    way: the AWS CLI asks this tool for credentials when it needs them, so you
    never end up with expired credentials written to disk;
  - pointing the default AWS profile at your lab, so you do not have to type
    --profile on every command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			if email == "" {
				var err error
				if email, err = ui.Prompt(ctx, os.Stdin, "AWS Academy email: "); err != nil {
					return err
				}
			}
			if email == "" {
				return errors.New("an email is required")
			}

			password, err := ui.PromptPassword(ctx, "Password (not shown): ")
			if err != nil {
				return err
			}
			if password == "" {
				return errors.New("a password is required")
			}

			cfg := &config.Config{
				Email:      email,
				Password:   password,
				AWSProfile: profile,
				Region:     region,
				CourseID:   courseID,
			}

			// We validate before saving anything: there is no point leaving a
			// password on disk that we already know does not work.
			fmt.Fprintln(os.Stderr, "\nTesting the login…")
			client, err := httpx.New()
			if err != nil {
				return err
			}
			cv := canvas.New(client, config.DefaultCanvasBaseURL)
			user, err := cv.Login(ctx, email, password)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s logged in successfully as %s\n", mark(true), user.Name)

			if err := cfg.Save(); err != nil {
				return err
			}
			sess := &state.Session{Cookies: client.ExportCookies(), UserID: user.ID, UserName: user.Name}
			if err := sess.Save(); err != nil {
				return err
			}

			// We look up the lab now so that the first 'start' does not run
			// into surprises, and so we can name the course we found.
			fmt.Fprintln(os.Stderr, "\nLooking for your lab…")
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			// If there are several courses we ask now, while we are in an
			// interactive session. Discovering it at the first 'start' would
			// force the user to sort it out right when they wanted to work.
			if cfg.CourseID == "" {
				if courses, cErr := app.canvas.Courses(ctx); cErr == nil && len(courses) > 1 {
					chosen, cErr := chooseCourse(ctx, courses)
					if cErr != nil {
						return cErr
					}
					cfg.CourseID = strconv.FormatInt(chosen.ID, 10)
					app.cfg.CourseID = cfg.CourseID
					if err := cfg.Save(); err != nil {
						return err
					}
				}
			}

			disc, err := app.Discover(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s could not locate it automatically: %v\n", mark(false), err)
				fmt.Fprintln(os.Stderr, "  The configuration was saved anyway; try 'awsacademy courses'.")
			} else {
				fmt.Fprintf(os.Stderr, "%s %s\n", mark(true), disc.CourseName)
				fmt.Fprintf(os.Stderr, "  item: %s\n", disc.ItemTitle)
			}

			if !skipProcess {
				if err := setupCredentialProcess(ctx, cfg.AWSProfile, cfg.Region, useProcess); err != nil {
					return err
				}
			}
			if !skipDefault {
				if err := applyDefaultProfile(ctx, cfg.Region, useDefault); err != nil {
					return err
				}
			}

			fmt.Fprintf(os.Stderr, "\nDone. Configuration in %s\n", config.Path())
			fmt.Fprintln(os.Stderr, "Try:  awsacademy start")
			return nil
		},
	}

	cmd.Flags().StringVar(&courseID, "course-id", "", "pin this course and do not ask")
	cmd.Flags().StringVar(&email, "email", "", "AWS Academy email (asked for if omitted)")
	cmd.Flags().StringVar(&profile, "profile", config.DefaultAWSProfile, "AWS CLI profile to maintain")
	cmd.Flags().StringVar(&region, "region", "us-east-1", "default AWS region")
	cmd.Flags().BoolVar(&useProcess, "credential-process", false,
		"configure credential_process without asking")
	cmd.Flags().BoolVar(&skipProcess, "no-credential-process", false,
		"do not touch ~/.aws/config")
	cmd.Flags().BoolVar(&useDefault, "default-profile", false,
		"use the lab as the default AWS profile, without asking")
	cmd.Flags().BoolVar(&skipDefault, "no-default-profile", false,
		"do not touch the default AWS profile")
	return cmd
}

// setupCredentialProcess declares this binary as the profile's provider.
func setupCredentialProcess(ctx context.Context, profile, region string, assumeYes bool) error {
	if !assumeYes {
		fmt.Fprintf(os.Stderr, `
I can configure the %q profile so that the AWS CLI asks me for credentials
when it needs them (credential_process). That way they renew themselves and no
expired credentials are left in ~/.aws/credentials.

`, profile)
		ok, err := ui.Confirm(ctx, os.Stdin, "Configure it? [Y/n]: ", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "All right: use 'awsacademy start --write-credentials'.")
			return nil
		}
	}

	// This is the detail that breaks the setup when overlooked: within a single
	// profile, the static keys in ~/.aws/credentials win over the
	// credential_process in config. Leaving them there would turn the provider
	// into decoration and the user would keep using dead credentials without
	// understanding why.
	if awscreds.HasStaticCredentials(profile) {
		fmt.Fprintf(os.Stderr, `
Careful: the %q profile already has keys written in %s.
Those keys take priority over credential_process, so they have to go for this
to be of any use.

`, profile, awscreds.CredentialsPath())
		ok, err := ui.Confirm(ctx, os.Stdin, "Delete them? [Y/n]: ", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr,
				"Nothing touched. While those keys are there, credential_process will not be used.")
			return nil
		}
		if err := awscreds.RemoveSharedCredentials(profile); err != nil {
			return err
		}
	}

	if err := awscreds.ConfigureCredentialProcess(profile, selfCommand(), region); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s %s configured in %s\n", mark(true), profile, awscreds.ConfigPath())
	return nil
}
