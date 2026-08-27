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
		Short: "Usa el laboratorio como perfil de AWS por defecto",
		Long: `Apunta el perfil "default" de ~/.aws/config a este proveedor de credenciales.

Es la forma portable de no escribir --profile en cada comando: el perfil por
defecto vive en un fichero de configuración de AWS, así que funciona igual en
cualquier distribución, con cualquier shell, y también en macOS y Windows. No
usa variables de entorno ni toca los ficheros de arranque de tu shell.

Después de esto, ambas formas funcionan:

  aws sts get-caller-identity
  aws sts get-caller-identity --profile academy

Para deshacerlo:

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
	cmd.Flags().BoolVar(&undo, "undo", false, "dejar de ser el perfil por defecto")
	return cmd
}

// applyDefaultProfile apunta el perfil por defecto a este binario.
func applyDefaultProfile(ctx context.Context, region string, assumeYes bool) error {
	command := selfCommand()

	if awscreds.IsDefaultProfileOurs(command) {
		fmt.Fprintf(os.Stderr, "%s ya sos el perfil por defecto en %s\n",
			mark(true), awscreds.ConfigPath())
		return nil
	}

	// Nunca se pisa en silencio: el perfil por defecto puede ser el de trabajo
	// de alguien, y romperlo sin avisar sería mucho peor que pedir --profile.
	if conflict := awscreds.DefaultProfileConflict(command); conflict != "" {
		fmt.Fprintf(os.Stderr, `
El perfil por defecto de AWS ya está en uso: %s

Si lo reemplazo, los comandos de AWS que hoy no llevan --profile pasarían a
usar el laboratorio.

`, conflict)
		ok, err := ui.Confirm(ctx, os.Stdin, "¿Lo reemplazo igual? [s/N]: ", false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "No toco nada. Seguí usando --profile academy.")
			return nil
		}
	} else if !assumeYes {
		fmt.Fprintf(os.Stderr, `
Puedo apuntar el perfil por defecto de AWS a tu laboratorio, para que no tengas
que escribir --profile en cada comando.

Se hace en %s, así que funciona con cualquier shell y en
cualquier sistema; no toca variables de entorno.

`, awscreds.ConfigPath())
		ok, err := ui.Confirm(ctx, os.Stdin, "¿Lo configuro? [s/N]: ", false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "De acuerdo. Podés hacerlo luego con 'awsacademy default-profile'.")
			return nil
		}
	}

	if err := awscreds.ConfigureDefaultProfile(command, region); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s perfil por defecto configurado en %s\n",
		mark(true), awscreds.ConfigPath())
	fmt.Fprintln(os.Stderr, "  Ya podés usar 'aws' sin --profile.")
	return nil
}

func undoDefaultProfile() error {
	removed, err := awscreds.RemoveDefaultProfile(selfCommand())
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintf(os.Stderr, "%s el perfil por defecto no apuntaba a esta herramienta\n", mark(true))
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s ya no sos el perfil por defecto\n", mark(true))
	return nil
}
