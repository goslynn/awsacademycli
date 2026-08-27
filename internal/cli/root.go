package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/goslynn/awsacademycli/internal/config"
	"github.com/goslynn/awsacademycli/internal/ui"
	"github.com/spf13/cobra"
)

// errQuiet marca un fallo cuyo detalle ya se mostró: cambia el código de
// salida sin imprimir un segundo mensaje.
var errQuiet = errors.New("")

// Opciones globales, compartidas por todos los subcomandos.
var (
	flagJSON      bool
	flagDebugHTTP bool
)

// ExecuteContext corre la CLI. Devuelve el código de salida del proceso.
func ExecuteContext(ctx context.Context, version string) int {
	root := newRootCmd(version)
	if err := root.ExecuteContext(ctx); err != nil {
		if errors.Is(err, errQuiet) {
			return 1
		}
		// Cancelar no es un fallo: no se imprime un error y se sale con el
		// código convencional para una interrupción (128 + SIGINT).
		if errors.Is(err, ui.ErrCancelled) || errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "cancelado")
			return 130
		}
		// Cobra ya imprimió los errores de uso; los demás los damos nosotros
		// con una pista de qué hacer a continuación.
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if errors.Is(err, config.ErrNotConfigured) {
			fmt.Fprintln(os.Stderr, "\nEmpezá por acá:\n  awsacademy setup")
		}
		return 1
	}
	return 0
}

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:     "awsacademy",
		Version: version,
		Short:   "Controla el AWS Academy Learner Lab desde la terminal",
		Long: `awsacademy levanta y baja el Learner Lab de AWS Academy y mantiene
tu perfil de AWS CLI con credenciales frescas.

Hace por vos el recorrido de siempre: entrar a Canvas, abrir el laboratorio,
pulsar Start Lab, esperar a que esté listo y copiar las credenciales. Las
credenciales del laboratorio duran unas pocas horas, así que ese recorrido se
repite varias veces al día; esto lo reduce a un comando.

Para empezar:

  awsacademy setup     una vez, guarda tus credenciales de AWS Academy
  awsacademy start     levanta el laboratorio y actualiza el perfil de AWS
  awsacademy status    dice si podés trabajar y cuánto tiempo te queda
  awsacademy courses   lista tus cursos, por si tenés más de uno
  awsacademy stop      baja el laboratorio

La configuración vive en:
  ` + config.Path(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().BoolVar(&flagJSON, "json", false,
		"salida en JSON, para consumir desde scripts")
	root.PersistentFlags().BoolVar(&flagDebugHTTP, "debug-http", false,
		"traza cada request HTTP por stderr")

	root.AddCommand(
		newSetupCmd(),
		newLoginCmd(),
		newLogoutCmd(),
		newCoursesCmd(),
		newStatusCmd(),
		newStartCmd(),
		newStopCmd(),
		newCredsCmd(),
		newEnvCmd(),
		newDefaultProfileCmd(),
		newDebugCmd(),
	)
	return root
}
