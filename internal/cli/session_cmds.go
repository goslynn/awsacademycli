package cli

import (
	"fmt"

	"github.com/goslynn/awsacademycli/internal/state"
	"github.com/spf13/cobra"
)

func newLoginCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Open a session on AWS Academy",
		Long: `Opens a session on Canvas and saves the cookies.

Calling it is not normally necessary: the other commands authenticate on their
own when the saved session has expired.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			if force {
				// With --force we want to test the password for real, not
				// revalidate the session we already had.
				if err := state.ClearSession(); err != nil {
					return err
				}
			}
			user, err := app.EnsureSession(cmd.Context())
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(map[string]any{
					"authenticated": true,
					"user":          user.Name,
					"user_id":       user.ID,
				})
			}
			fmt.Printf("Session opened as %s\n", user.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "reauthenticate even if the session is still alive")
	return cmd
}

func newLogoutCmd() *cobra.Command {
	var alsoDiscovery bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Delete the saved session",
		Long: `Removes the cookies saved on disk.

It does not close the session on the server and does not touch your
configuration: the next operation will authenticate again with the saved
credentials.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := state.ClearSession(); err != nil {
				return err
			}
			if alsoDiscovery {
				if err := state.ClearDiscovery(); err != nil {
					return err
				}
			}
			if flagJSON {
				return printJSON(map[string]any{"cleared": true})
			}
			fmt.Println("Session deleted.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&alsoDiscovery, "reset-discovery", false,
		"also forget which course and item are the lab")
	return cmd
}
