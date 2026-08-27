# awsacademycli

Control the [AWS Academy Learner Lab](https://awsacademy.instructure.com) from the terminal.

It does the usual round trip for you — log in to Canvas, open the lab, press
*Start Lab*, wait, open *AWS Details* and copy the three keys into
`~/.aws/credentials` — and reduces it to a single command. Because lab
credentials only last a few hours, that round trip repeats several times a day.

```console
$ awsacademy start
lab: AWS Academy Learner Lab
starting…
  state: starting
  state: running

Lab ready.
  remaining    3h58m0s of session
  profile      academy -> credential_process
  ✓ arn:aws:sts::123456789012:assumed-role/voclabs/user1234567
```

## Installation

Requires Go 1.24 or later. No browser and no other dependency.

```console
$ go install github.com/goslynn/awsacademycli/cmd/awsacademy@latest
```

From the repository:

```console
$ CGO_ENABLED=0 go build -o awsacademy ./cmd/awsacademy
```

## Usage

```console
$ awsacademy setup     # once: saves your credentials and locates the lab
$ awsacademy start     # brings the lab up and refreshes the AWS profile
$ awsacademy courses   # lists your courses and pins the one with the lab
$ awsacademy status    # can I work? how much time is left?
$ awsacademy stop      # brings the lab down
```

### Avoiding `--profile` on every command

```console
$ awsacademy default-profile
```

Points the `default` profile in `~/.aws/config` at the same provider. From then
on, `aws sts get-caller-identity` just works, exactly like
`--profile academy`.

It is resolved in the AWS configuration, not in the shell, so it behaves the
same on any distribution, with any shell, and on macOS and Windows too. It uses
no environment variables and never touches your startup files.

> It never silently clobbers someone else's default profile: if static keys,
> another `credential_process`, an SSO session or an assumed role are already
> there, it reports what it found and asks for confirmation. `--undo` reverses
> it, and removes only what it added: settings you wrote by hand stay put.

For a one-off in a single session there is also `eval "$(awsacademy env)"`,
which exports `AWS_PROFILE`.

## How it delivers credentials

By default it configures `credential_process` in `~/.aws/config`:

```ini
[profile academy]
credential_process = /home/you/go/bin/awsacademy creds
region = us-east-1
```

That way the AWS CLI asks this tool for credentials when it needs them, caches
them and renews them on its own, so no expired credentials are left on disk.

> **Mind the precedence.** Within a single profile, the static keys in
> `~/.aws/credentials` take priority over the `credential_process` in
> `~/.aws/config`. If you leave both in place, the provider is decorative and
> you keep using dead credentials. `setup` detects this collision and offers to
> clean it up.

If you prefer the classic mode, `awsacademy start --write-credentials` writes
the profile into `~/.aws/credentials`, preserving your other profiles.

## Files

| Path | Contents |
|---|---|
| `~/.config/awsacademy/config.toml` | AWS Academy credentials and chosen course. Permissions **0600**, required |
| `~/.local/state/awsacademy/session.json` | Session cookies |
| `~/.local/state/awsacademy/discovery.json` | Which course, which item and which endpoints are the lab |
| `~/.local/state/awsacademy/creds.json` | Latest credentials, `credential_process` cache |

Everything under `state/` is a rebuildable cache: it can be deleted without
losing anything.

Instead of storing the password in the clear you can delegate to an external
manager:

```toml
password_command = "pass show aws/academy"
```

## How it works

No browser involved. The complete round trip, as verified against the real
service:

1. **Canvas login.** It serves its login with React, but underneath it is still
   classic Rails: a POST to `/login/canvas` with the CSRF token that arrives in
   a cookie. No captcha. `remember_me` is requested, so the persistent cookie
   lasts weeks and the password is almost never needed.
2. **API discovery.** Canvas exposes its REST API to the session, so the course
   and the lab item are resolved in JSON instead of by scraping menus. A typical
   course has seven external-tool items and nearly all of them mention the
   "learner lab" — the guide, the demos, the FAQ — so they are picked by LTI
   provider, not by title.
3. **LTI 1.3 launch.** The signed form is not in the iframe: Canvas leaves that
   at `about:blank` and fills it in with JavaScript. The real form is hidden in
   the page itself, marked with `data-message-type="tool_launch"`. From there
   the OIDC dance — `oidc_login.php`, `authorize_redirect`, `authorize`, and the
   `id_token` returned in a self-submitting form — is just following redirects
   and resubmitting forms. The same mechanism covers LTI 1.1, so there is no
   need to know in advance which version the course uses.
4. **Vocareum bounce pages.** The provider does not serve the panel directly: it
   returns two pages whose only content is a script that navigates to the next
   one (`launch.php` → `main.php?m=editor` → `main.php?m=clabide`). Following
   them needs no JavaScript engine, only reading the URL.
5. **The lab API.** Vocareum serves everything through `util/vcput.php` and
   distinguishes the operation with `a=`: `startaws`, `endaws`, `getawsstatus`,
   `getaws`. Those URLs carry a `stepid` specific to the session, so they
   **cannot be compiled in as constants**: they are read from the lab page,
   which is where its own buttons declare them.

**None of this is hardcoded.** The course changes every term, all the URLs change
with it, and the session identifiers change on every launch. Everything is
rediscovered when needed.

## Development

```console
$ go test ./...          # none of this touches the network
$ gofmt -l . && go vet ./...
```

The tests stand up a simulated Canvas and Vocareum and exercise the whole chain,
including the auto-submit of the signed form.

### Diagnosing discovery

If the lab stops responding, the first step is to see what the page exposes:

```console
$ awsacademy debug lab --scripts
```

It goes through the LTI launch and prints every endpoint path it finds, plus the
ones the tool is using. No browser required.

When that is not enough, you can capture the real traffic:

```console
$ go run ./cmd/vockit -out capture.json
```

It opens a browser, you do the flow by hand once, and every XHR is dumped with
its URL, method, body and response. Confirmed values go into `discovery.json`,
which takes priority over the compiled-in guesses: fixing them requires no
rebuild.

> The capture contains session cookies and credentials in the clear. Do not
> publish it.

## Notice

It automates access to your own account with your own credentials, following the
same round trip your browser would. Even so, it behaves with deliberate
restraint: one request in flight at a time, no parallelism, backoff on 429 and
5xx, and a User-Agent that identifies itself instead of pretending to be a
browser.
