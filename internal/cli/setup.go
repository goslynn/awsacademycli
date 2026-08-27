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
		Short: "Configura la herramienta por primera vez",
		Long: `Guarda tus credenciales de AWS Academy y localiza tu laboratorio.

Pide el usuario y la contraseña, comprueba que funcionan haciendo login de
verdad, busca en tu cuenta el curso y el ítem del Learner Lab, y deja todo
guardado en ` + config.Path() + ` con permisos 0600.

También ofrece dos comodidades, ambas opcionales:

  - configurar credential_process en ~/.aws/config, que es la forma recomendada:
    el AWS CLI le pide las credenciales a esta herramienta cuando las necesita,
    así que nunca te quedan credenciales vencidas escritas en disco;
  - apuntar el perfil de AWS por defecto a tu laboratorio, para no escribir
    --profile en cada comando.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			if email == "" {
				var err error
				if email, err = ui.Prompt(ctx, os.Stdin, "Email de AWS Academy: "); err != nil {
					return err
				}
			}
			if email == "" {
				return errors.New("hace falta un email")
			}

			password, err := ui.PromptPassword(ctx, "Contraseña (no se muestra): ")
			if err != nil {
				return err
			}
			if password == "" {
				return errors.New("hace falta una contraseña")
			}

			cfg := &config.Config{
				Email:      email,
				Password:   password,
				AWSProfile: profile,
				Region:     region,
				CourseID:   courseID,
			}

			// Validamos antes de guardar nada: no tiene sentido dejar en disco
			// una contraseña que ya sabemos que no sirve.
			fmt.Fprintln(os.Stderr, "\nProbando el login…")
			client, err := httpx.New()
			if err != nil {
				return err
			}
			cv := canvas.New(client, config.DefaultCanvasBaseURL)
			user, err := cv.Login(ctx, email, password)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s login correcto como %s\n", mark(true), user.Name)

			if err := cfg.Save(); err != nil {
				return err
			}
			sess := &state.Session{Cookies: client.ExportCookies(), UserID: user.ID, UserName: user.Name}
			if err := sess.Save(); err != nil {
				return err
			}

			// Buscamos el laboratorio ahora para que el primer 'start' no se
			// tope con sorpresas, y para poder nombrar el curso encontrado.
			fmt.Fprintln(os.Stderr, "\nBuscando tu laboratorio…")
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			// Si hay varios cursos preguntamos ahora, mientras estamos en una
			// sesión interactiva. Descubrirlo al primer 'start' obligaría al
			// usuario a resolverlo justo cuando quería trabajar.
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
				fmt.Fprintf(os.Stderr, "%s no pude localizarlo automáticamente: %v\n", mark(false), err)
				fmt.Fprintln(os.Stderr, "  La configuración quedó guardada igual; probá 'awsacademy courses'.")
			} else {
				fmt.Fprintf(os.Stderr, "%s %s\n", mark(true), disc.CourseName)
				fmt.Fprintf(os.Stderr, "  ítem: %s\n", disc.ItemTitle)
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

			fmt.Fprintf(os.Stderr, "\nListo. Configuración en %s\n", config.Path())
			fmt.Fprintln(os.Stderr, "Probá:  awsacademy start")
			return nil
		},
	}

	cmd.Flags().StringVar(&courseID, "course-id", "", "fijar este curso y no preguntar")
	cmd.Flags().StringVar(&email, "email", "", "email de AWS Academy (si no, se pregunta)")
	cmd.Flags().StringVar(&profile, "profile", config.DefaultAWSProfile, "perfil de AWS CLI a mantener")
	cmd.Flags().StringVar(&region, "region", "us-east-1", "region de AWS por defecto")
	cmd.Flags().BoolVar(&useProcess, "credential-process", false,
		"configurar credential_process sin preguntar")
	cmd.Flags().BoolVar(&skipProcess, "no-credential-process", false,
		"no tocar ~/.aws/config")
	cmd.Flags().BoolVar(&useDefault, "default-profile", false,
		"usar el laboratorio como perfil de AWS por defecto, sin preguntar")
	cmd.Flags().BoolVar(&skipDefault, "no-default-profile", false,
		"no tocar el perfil por defecto de AWS")
	return cmd
}

// setupCredentialProcess declara este binario como proveedor del perfil.
func setupCredentialProcess(ctx context.Context, profile, region string, assumeYes bool) error {
	if !assumeYes {
		fmt.Fprintf(os.Stderr, `
Puedo configurar el perfil %q para que el AWS CLI me pida las credenciales
cuando las necesite (credential_process). Así se renuevan solas y no quedan
credenciales vencidas en ~/.aws/credentials.

`, profile)
		ok, err := ui.Confirm(ctx, os.Stdin, "¿Configurarlo? [S/n]: ", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "De acuerdo: usá 'awsacademy start --write-credentials'.")
			return nil
		}
	}

	// Este es el detalle que rompe la configuración si se pasa por alto: dentro
	// de un mismo perfil, las claves estáticas de ~/.aws/credentials ganan
	// sobre el credential_process de ~/.aws/config. Dejarlas convertiría al
	// proveedor en decorado y el usuario seguiría usando credenciales muertas
	// sin entender por qué.
	if awscreds.HasStaticCredentials(profile) {
		fmt.Fprintf(os.Stderr, `
Ojo: el perfil %q ya tiene claves escritas en %s.
Esas claves tienen prioridad sobre credential_process, así que hay que quitarlas
para que esto sirva de algo.

`, profile, awscreds.CredentialsPath())
		ok, err := ui.Confirm(ctx, os.Stdin, "¿Las borro? [S/n]: ", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr,
				"No toco nada. Mientras esas claves estén ahí, credential_process no se usará.")
			return nil
		}
		if err := awscreds.RemoveSharedCredentials(profile); err != nil {
			return err
		}
	}

	if err := awscreds.ConfigureCredentialProcess(profile, selfCommand(), region); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s %s configurado en %s\n", mark(true), profile, awscreds.ConfigPath())
	return nil
}
