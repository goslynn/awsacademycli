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
		Short: "Levanta el laboratorio y actualiza las credenciales de AWS",
		Long: `Levanta el Learner Lab, espera a que esté listo y guarda sus credenciales.

Es idempotente: si el laboratorio ya está corriendo no lo reinicia, solo
refresca las credenciales. Podés llamarlo cuantas veces quieras.`,
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
			progress("laboratorio: %s", disc.CourseName)

			st, err := lab.Status(ctx)
			if err != nil {
				return err
			}

			if st.Running() {
				progress("ya estaba corriendo, refresco las credenciales")
			} else {
				progress("arrancando…")
				if err := lab.Start(ctx); err != nil {
					return err
				}
				if noWait {
					if flagJSON {
						return printJSON(map[string]any{"state": string(vocareum.StateStarting)})
					}
					fmt.Println("Arranque pedido. Consultá el progreso con: awsacademy status")
					return nil
				}
				st, err = lab.WaitForRunning(ctx, timeout, func(s vocareum.Status) {
					progress("  estado: %s", s.State)
				})
				if err != nil {
					return err
				}
			}

			// Vocareum sirve credenciales y contador en la misma respuesta.
			detail, creds, err := lab.Details(ctx)
			if err != nil {
				return err
			}
			st = detail
			if creds.Region == "" {
				creds.Region = app.cfg.Region
			}
			if err := creds.Save(); err != nil {
				return err
			}

			profile := app.cfg.AWSProfile
			target := "credential_process"
			// Escribimos el fichero compartido si el usuario lo pidió o si es
			// como su perfil está configurado; si no, la caché ya alcanza.
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
		"cuánto esperar a que el laboratorio esté listo")
	cmd.Flags().BoolVar(&writeShared, "write-credentials", false,
		"escribir además el perfil en ~/.aws/credentials")
	cmd.Flags().BoolVar(&noWait, "no-wait", false,
		"pedir el arranque y salir sin esperar")
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
			"state":       string(st.State),
			"profile":     profile,
			"written_to":  target,
			"remaining":   st.Remaining.Round(time.Second).String(),
			"budget_used": st.BudgetUsed,
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

	fmt.Printf("\nLaboratorio listo.\n")
	if st.Remaining > 0 {
		fmt.Printf("  queda        %s de sesión\n", st.Remaining.Round(time.Second))
	}
	if st.BudgetTotal > 0 {
		fmt.Printf("  presupuesto  $%.2f de $%.2f\n", st.BudgetUsed, st.BudgetTotal)
	}
	fmt.Printf("  perfil       %s -> %s\n", profile, target)
	if valErr != nil {
		// No es fatal: las credenciales están guardadas y puede ser un
		// problema de red, pero el usuario tiene que enterarse.
		fmt.Printf("  %s no pude verificarlas contra AWS: %v\n", mark(false), valErr)
		return nil
	}
	fmt.Printf("  %s %s\n", mark(true), identity.ARN)
	return nil
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Detiene el laboratorio",
		Long: `Detiene el Learner Lab.

Los recursos que hayas creado siguen existiendo entre sesiones, pero las
credenciales dejan de servir hasta el próximo 'start'.`,
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

			// Las credenciales cacheadas ya no valen nada: dejarlas solo
			// serviría para que el próximo comando falle de forma confusa.
			if creds, err := state.LoadCredentials(); err == nil {
				creds.Expiration = time.Now()
				_ = creds.Save()
			}

			if flagJSON {
				return printJSON(map[string]any{"state": string(vocareum.StateStopping)})
			}
			fmt.Println("Laboratorio detenido.")
			return nil
		},
	}
}

func newCredsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "creds",
		Short: "Emite las credenciales en el formato de credential_process",
		Long: `Escribe las credenciales del laboratorio como JSON en stdout, en el
formato que espera credential_process del AWS CLI.

Está pensado para que lo invoque el AWS CLI, no para usarlo a mano. Se declara
en ~/.aws/config:

  [profile academy]
  credential_process = awsacademy creds

No levanta el laboratorio si está apagado: cada comando 'aws' se quedaría
colgado varios minutos esperando. Falla rápido y te dice qué hacer.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			creds, err := state.LoadCredentials()
			if err != nil || creds.Expired() {
				// Este mensaje lo ve el usuario a través del AWS CLI, así que
				// tiene que decir exactamente qué comando lo arregla.
				return fmt.Errorf(
					"no hay credenciales vigentes del laboratorio: ejecutá 'awsacademy start'")
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

// progress informa el avance por stderr, para no ensuciar una salida --json
// que un script pueda estar canalizando.
func progress(format string, args ...any) {
	if flagJSON {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
