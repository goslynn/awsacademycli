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

// ok indica si todo está en orden: sesión viva, laboratorio corriendo y
// credenciales que funcionan. Es lo que gobierna el código de salida, para
// poder escribir `awsacademy status --json >/dev/null || awsacademy start`.
func (r *statusReport) ok() bool {
	return r.Auth.Authenticated && r.Lab.State == string(vocareum.StateRunning) && r.AWS.Valid
}

// statusReport es lo que informa `status`, en las tres capas que pueden fallar
// por separado: la sesión, el laboratorio y las credenciales de AWS.
type statusReport struct {
	Auth struct {
		Authenticated bool   `json:"authenticated"`
		User          string `json:"user,omitempty"`
		Error         string `json:"error,omitempty"`
	} `json:"auth"`

	Lab struct {
		State string `json:"state"`
		// RemainingSeconds es lo que queda de sesión: el número que decide si
		// conviene empezar algo largo ahora o levantar el laboratorio de nuevo.
		RemainingSeconds int     `json:"remaining_seconds,omitempty"`
		Remaining        string  `json:"remaining,omitempty"`
		BudgetUsed       float64 `json:"budget_used,omitempty"`
		BudgetTotal      float64 `json:"budget_total,omitempty"`
		Course           string  `json:"course,omitempty"`
		Error            string  `json:"error,omitempty"`
	} `json:"lab"`

	AWS struct {
		Profile string `json:"profile"`
		// Source dice de dónde saldrían las credenciales: del fichero
		// compartido o de este binario vía credential_process.
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
		Short: "Muestra el estado de la sesión, del laboratorio y de las credenciales",
		Long: `Informa las tres cosas que pueden estar mal, por separado:

  auth   si la sesión de AWS Academy sigue viva
  lab    si el laboratorio está levantado y cuánto tiempo de sesión queda
  aws    si el perfil de AWS CLI tiene credenciales que de verdad funcionan,
         comprobado contra sts:GetCallerIdentity

Sale con código 0 solo si las tres están bien, así que se puede encadenar:

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
				// El detalle ya se imprimió; un error extra solo repetiría
				// lo mismo, pero el código de salida tiene que reflejarlo.
				return errQuiet
			}
			return nil
		},
	}
}

// collectStatus reúne el informe sin abortar al primer fallo: que la sesión
// esté caída no impide decir qué pasa con las credenciales de AWS, y ver las
// tres capas juntas es justamente lo que permite diagnosticar.
func collectStatus(ctx context.Context) *statusReport {
	r := &statusReport{}
	r.Lab.State = string(vocareum.StateUnknown)

	app, err := newApp(flagDebugHTTP)
	if err != nil {
		// Sin configuración no se puede decir nada de las otras dos capas;
		// decirlo es más útil que dejarlas en blanco.
		r.Auth.Error = err.Error()
		r.Lab.Error = "sin configuración"
		r.AWS.Profile = "?"
		r.AWS.Error = "sin configuración"
		return r
	}
	r.AWS.Profile = app.cfg.AWSProfile

	user, err := app.EnsureSession(ctx)
	switch {
	case err == nil:
		r.Auth.Authenticated = true
		r.Auth.User = user.Name
	case errors.Is(err, canvas.ErrInvalidCredentials):
		r.Auth.Error = "credenciales rechazadas por AWS Academy"
	default:
		r.Auth.Error = err.Error()
	}

	if r.Auth.Authenticated {
		collectLabStatus(ctx, app, r)
	} else {
		r.Lab.Error = "sin sesión"
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
	// El contador no viaja en la respuesta de estado, solo junto a las
	// credenciales, así que se pide únicamente cuando hay algo que contar.
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
		r.AWS.Error = "sin configuración"
		return
	}

	// Se valida la fuente que el AWS CLI usaría de verdad. Con
	// credential_process activo, eso es la caché de este binario; con el modo
	// clásico, lo que haya escrito en ~/.aws/credentials.
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
		r.AWS.Error = fmt.Sprintf("sin credenciales para el perfil %q", profile)
		return
	}
	if creds.Expired() {
		r.AWS.Error = "las credenciales guardadas ya expiraron"
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
		fmt.Printf("  %s sesión viva como %s\n", mark(true), r.Auth.User)
	} else {
		fmt.Printf("  %s %s\n", mark(false), r.Auth.Error)
	}

	fmt.Println("\nLABORATORIO")
	switch {
	case r.Lab.Error != "":
		fmt.Printf("  %s %s\n", mark(false), r.Lab.Error)
	default:
		running := r.Lab.State == string(vocareum.StateRunning)
		fmt.Printf("  %s %s\n", mark(running), r.Lab.State)
		if r.Lab.Course != "" {
			fmt.Printf("    curso        %s\n", r.Lab.Course)
		}
		if r.Lab.Remaining != "" {
			fmt.Printf("    queda        %s de sesión\n", r.Lab.Remaining)
		}
		if r.Lab.BudgetTotal > 0 {
			fmt.Printf("    presupuesto  $%.2f de $%.2f\n", r.Lab.BudgetUsed, r.Lab.BudgetTotal)
		}
	}

	fmt.Println("\nAWS CLI")
	fmt.Printf("    perfil       %s\n", r.AWS.Profile)
	if r.AWS.Source != "" {
		fmt.Printf("    origen       %s\n", r.AWS.Source)
	}
	if r.AWS.Valid {
		fmt.Printf("  %s credenciales válidas\n", mark(true))
		fmt.Printf("    arn          %s\n", r.AWS.ARN)
		fmt.Printf("    cuenta       %s\n", r.AWS.Account)
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

// printJSON escribe una estructura como JSON indentado en stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
