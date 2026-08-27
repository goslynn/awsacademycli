package awscreds

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/goslynn/awsacademycli/internal/state"
)

// processOutput es el contrato de credential_process del AWS CLI.
// Ver: https://docs.aws.amazon.com/cli/latest/topic/config-vars.html
type processOutput struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	// Expiration le dice al CLI cuándo volver a preguntar. Sin ella, cachearía
	// las credenciales para siempre y seguiría usándolas ya muertas.
	Expiration string `json:"Expiration,omitempty"`
}

// ProcessOutput serializa las credenciales en el formato que espera el AWS CLI.
func ProcessOutput(creds *state.Credentials) ([]byte, error) {
	out := processOutput{
		Version:         1,
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		SessionToken:    creds.SessionToken,
	}
	if !creds.Expiration.IsZero() {
		out.Expiration = creds.Expiration.UTC().Format(time.RFC3339)
	}
	return json.Marshal(out)
}

// Identity es lo que devuelve sts:GetCallerIdentity.
type Identity struct {
	Account string
	ARN     string
	UserID  string
}

// Validate comprueba contra AWS que unas credenciales sirven de verdad.
//
// Usa un proveedor estático a propósito, en vez de la cadena por defecto: si el
// perfil está configurado con credential_process, resolverlo por la cadena
// haría que este binario se invocara a sí mismo. Además, lo que queremos saber
// es si estas credenciales concretas funcionan.
func Validate(ctx context.Context, creds *state.Credentials, region string) (*Identity, error) {
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken)),
		// Sin esto el SDK aún leería ~/.aws en busca de otros ajustes y podría
		// volver a toparse con el credential_process.
		awsconfig.WithSharedConfigProfile(""),
	)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}
	return &Identity{
		Account: aws.ToString(out.Account),
		ARN:     aws.ToString(out.Arn),
		UserID:  aws.ToString(out.UserId),
	}, nil
}

// ReadSharedCredentials lee las credenciales que hay escritas en el perfil.
// Sirve para que `status` diga si lo que el usuario tiene en disco todavía vale.
func ReadSharedCredentials(profile string) (*state.Credentials, error) {
	f, err := loadINI(CredentialsPath())
	if err != nil {
		return nil, err
	}
	sec := f.Section(profile)
	id := sec.Key("aws_access_key_id").String()
	if id == "" {
		return nil, fmt.Errorf("el perfil %q no tiene credenciales en %s", profile, CredentialsPath())
	}
	return &state.Credentials{
		AccessKeyID:     id,
		SecretAccessKey: sec.Key("aws_secret_access_key").String(),
		SessionToken:    sec.Key("aws_session_token").String(),
	}, nil
}
