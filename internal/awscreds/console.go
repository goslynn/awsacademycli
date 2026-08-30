package awscreds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/goslynn/awsacademycli/internal/state"
)

// federationEndpoint mints browser sessions out of temporary credentials. It is
// the same mechanism the lab's own "AWS" button uses, and the only supported way
// to reach the console without a password: the Learner Lab account has no
// console user to log in as.
//
// See: https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_enable-console-custom-url.html
//
// It is a variable rather than a constant only so the tests can point it at a
// local server; nothing else assigns to it.
var federationEndpoint = "https://signin.aws.amazon.com/federation"

// consoleIssuer is where the browser is sent back to if the console session
// expires. It identifies who minted the sign-in, so it names this tool.
const consoleIssuer = "https://github.com/goslynn/awsacademycli"

// ErrNotTemporary means the credentials cannot be federated into a console
// session. Only STS credentials can, and lab credentials always are.
var ErrNotTemporary = errors.New("the console can only be opened with temporary (lab) credentials")

// ConsoleURL turns lab credentials into a URL that opens the AWS console
// already signed in.
//
// The URL it returns carries a sign-in token and is therefore a credential in
// its own right: anyone who gets hold of it gets the lab's console.
func ConsoleURL(ctx context.Context, creds *state.Credentials, destination string) (string, error) {
	if creds == nil || creds.SessionToken == "" {
		return "", ErrNotTemporary
	}

	session, err := json.Marshal(map[string]string{
		"sessionId":    creds.AccessKeyID,
		"sessionKey":   creds.SecretAccessKey,
		"sessionToken": creds.SessionToken,
	})
	if err != nil {
		return "", err
	}

	// No SessionDuration is requested on purpose: it is rejected for
	// role credentials, and the console session should die with the lab
	// session anyway.
	token, err := signinToken(ctx, string(session))
	if err != nil {
		return "", err
	}

	q := url.Values{
		"Action":      {"login"},
		"Issuer":      {consoleIssuer},
		"Destination": {destination},
		"SigninToken": {token},
	}
	return federationEndpoint + "?" + q.Encode(), nil
}

// signinToken exchanges the credentials for a one-shot browser token.
func signinToken(ctx context.Context, session string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	q := url.Values{"Action": {"getSigninToken"}, "Session": {session}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		federationEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach the AWS federation endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		// AWS answers this in plain text, and what it says is the diagnosis:
		// expired credentials give "Error retrieving SigninToken".
		return "", fmt.Errorf("AWS refused to open a console session (HTTP %d): %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		SigninToken string `json:"SigninToken"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.SigninToken == "" {
		return "", errors.New("the AWS federation endpoint did not return a sign-in token")
	}
	return out.SigninToken, nil
}

// globalConsoleHost holds the services whose console does not live on the
// regional host. Sending them there gives a redirect at best and a 404 at worst.
var globalConsoleHost = map[string]string{
	"s3":            "s3.console.aws.amazon.com",
	"iam":           "us-east-1.console.aws.amazon.com",
	"billing":       "us-east-1.console.aws.amazon.com",
	"route53":       "us-east-1.console.aws.amazon.com",
	"cloudfront":    "us-east-1.console.aws.amazon.com",
	"organizations": "us-east-1.console.aws.amazon.com",
}

// ConsoleDestination is the page the console opens on.
//
// With no service it is the console's home. With one it is that service's
// home, which saves the click that everyone makes straight afterwards. A
// service given as a full URL is passed through: the map below cannot cover
// every console, and a deep link to a specific resource is a fair thing to want.
func ConsoleDestination(region, service string) string {
	if region == "" {
		region = "us-east-1"
	}
	service = strings.TrimSpace(service)

	switch {
	case service == "":
		return fmt.Sprintf("https://%s.console.aws.amazon.com/console/home?region=%s", region, region)
	case strings.HasPrefix(strings.ToLower(service), "https://"):
		// Returned as given: console deep links carry case-sensitive fragments.
		return service
	}

	service = strings.ToLower(service)
	host, ok := globalConsoleHost[service]
	if !ok {
		host = region + ".console.aws.amazon.com"
	}
	return fmt.Sprintf("https://%s/%s/home?region=%s", host, service, region)
}
