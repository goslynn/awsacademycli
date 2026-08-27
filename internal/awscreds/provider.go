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

// processOutput is the AWS CLI's credential_process contract.
// See: https://docs.aws.amazon.com/cli/latest/topic/config-vars.html
type processOutput struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	// Expiration tells the CLI when to ask again. Without it, it would cache
	// the credentials forever and keep using them once dead.
	Expiration string `json:"Expiration,omitempty"`
}

// ProcessOutput serialises the credentials in the format the AWS CLI expects.
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

// Identity is what sts:GetCallerIdentity returns.
type Identity struct {
	Account string
	ARN     string
	UserID  string
}

// Validate checks against AWS that a set of credentials really works.
//
// It uses a static provider on purpose, rather than the default chain: if the
// profile is configured with credential_process, resolving it through the chain
// would make this binary invoke itself. Besides, what we want to know is
// whether these particular credentials work.
func Validate(ctx context.Context, creds *state.Credentials, region string) (*Identity, error) {
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken)),
		// Without this the SDK would still read ~/.aws looking for other
		// settings and could run into the credential_process again.
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

// ReadSharedCredentials reads the credentials written in the profile.
// It lets `status` say whether what the user has on disk is still valid.
func ReadSharedCredentials(profile string) (*state.Credentials, error) {
	f, err := loadINI(CredentialsPath())
	if err != nil {
		return nil, err
	}
	sec := f.Section(profile)
	id := sec.Key("aws_access_key_id").String()
	if id == "" {
		return nil, fmt.Errorf("profile %q has no credentials in %s", profile, CredentialsPath())
	}
	return &state.Credentials{
		AccessKeyID:     id,
		SecretAccessKey: sec.Key("aws_secret_access_key").String(),
		SessionToken:    sec.Key("aws_session_token").String(),
	}, nil
}
