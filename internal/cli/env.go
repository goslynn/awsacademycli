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
		Short: "Imprime la orden que fija AWS_PROFILE en la sesión actual",
		Long: `Escribe en stdout la orden que exporta AWS_PROFILE, para evaluarla:

  eval "$(awsacademy env)"

Un programa no puede modificar las variables de su shell padre, así que la
exportación tiene que ejecutarla el propio shell. Esto afecta solo a la sesión
en curso.

Para no escribir --profile de forma permanente, es preferible:

  awsacademy default-profile

que lo resuelve en la configuración de AWS y por tanto funciona con cualquier
shell y en cualquier sistema, sin variables de entorno.`,
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
	cmd.Flags().BoolVar(&unset, "unset", false, "imprimir la orden que quita la variable")
	return cmd
}

// detectShell mira $SHELL para elegir la sintaxis. fish es el único de uso
// común que no entiende `export VAR=valor`.
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
