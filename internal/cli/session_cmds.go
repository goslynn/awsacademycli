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
		Short: "Abre sesión en AWS Academy",
		Long: `Abre sesión en Canvas y guarda las cookies.

Normalmente no hace falta llamarlo: los demás comandos se autentican solos
cuando la sesión guardada caducó.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			if force {
				// Con --force queremos probar la contraseña de verdad, no
				// revalidar la sesión que ya teníamos.
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
			fmt.Printf("Sesión abierta como %s\n", user.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "reautenticar aunque la sesión siga viva")
	return cmd
}

func newLogoutCmd() *cobra.Command {
	var alsoDiscovery bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Borra la sesión guardada",
		Long: `Elimina las cookies guardadas en disco.

No cierra la sesión en el servidor ni toca tu configuración: la próxima
operación volverá a autenticarse con las credenciales guardadas.`,
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
			fmt.Println("Sesión borrada.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&alsoDiscovery, "reset-discovery", false,
		"olvidar además qué curso y qué ítem son el laboratorio")
	return cmd
}
