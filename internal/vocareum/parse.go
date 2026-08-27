// Package vocareum controls the lab behind the LTI launch.
//
// Vocareum is a PHP application with jQuery: its buttons call flat endpoints
// under /util/*.php, so it can be driven with plain HTTP without needing a
// browser. Its own login does have a reCAPTCHA, but we never go through it: we
// always come in via the LTI launch from Canvas, which does not cross it.
package vocareum

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goslynn/awsacademycli/internal/state"
)

// The "AWS Details" panel shows a block in INI format, ready to paste into
// ~/.aws/credentials. We read it from there instead of trying to rebuild it.
var (
	reAccessKey    = regexp.MustCompile(`(?i)aws_access_key_id\s*=\s*([A-Z0-9]{16,})`)
	reSecretKey    = regexp.MustCompile(`(?i)aws_secret_access_key\s*=\s*([A-Za-z0-9/+=]{20,})`)
	reSessionToken = regexp.MustCompile(`(?i)aws_session_token\s*=\s*([A-Za-z0-9/+=]{50,})`)
)

// ParseCredentials extracts the STS credentials from the block Vocareum shows.
//
// It accepts the text as-is, with or without a profile header and with any
// spacing around the '=', because the exact format varies between labs.
func ParseCredentials(text string) (*state.Credentials, error) {
	find := func(re *regexp.Regexp, what string) (string, error) {
		m := re.FindStringSubmatch(text)
		if m == nil {
			return "", fmt.Errorf("could not find %s in the AWS Details panel", what)
		}
		return strings.TrimSpace(m[1]), nil
	}

	accessKey, err := find(reAccessKey, "aws_access_key_id")
	if err != nil {
		return nil, err
	}
	secretKey, err := find(reSecretKey, "aws_secret_access_key")
	if err != nil {
		return nil, err
	}
	// The session token is what tells an active lab apart from permanent
	// credentials; without it, something went wrong while reading.
	sessionToken, err := find(reSessionToken, "aws_session_token")
	if err != nil {
		return nil, err
	}

	return &state.Credentials{
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
		SessionToken:    sessionToken,
		FetchedAt:       time.Now(),
	}, nil
}

// reLabStatus captures the a=getawsstatus response, which is plain text:
// "Lab status: ready<br>".
var reLabStatus = regexp.MustCompile(`(?i)lab\s+status\s*:\s*([a-z ]+)`)

// ParseLabStatus interprets the status word Vocareum returns.
func ParseLabStatus(text string) (LabState, bool) {
	m := reLabStatus.FindStringSubmatch(text)
	if m == nil {
		return StateUnknown, false
	}
	word := strings.ToLower(strings.TrimSpace(m[1]))
	switch {
	case strings.HasPrefix(word, "ready"):
		return StateRunning, true
	case strings.HasPrefix(word, "start"), strings.HasPrefix(word, "provision"),
		strings.HasPrefix(word, "creating"), strings.HasPrefix(word, "pending"),
		strings.HasPrefix(word, "in progress"):
		return StateStarting, true
	case strings.HasPrefix(word, "stopping"), strings.HasPrefix(word, "terminating"),
		strings.HasPrefix(word, "ending"):
		return StateStopping, true
	case strings.HasPrefix(word, "stopped"), strings.HasPrefix(word, "not "),
		strings.HasPrefix(word, "off"), strings.HasPrefix(word, "ended"):
		return StateStopped, true
	}
	return StateUnknown, false
}

// reExpiry captures the exact expiry instant, which Vocareum publishes as a
// Unix timestamp in a hidden span. It is preferable to deducing it from the
// countdown: it is the one that really governs when the credentials die.
var reExpiry = regexp.MustCompile(`id=["']vlab-expiretime["'][^>]*>\s*(\d{9,})`)

// ParseExpiry returns the instant at which the lab session expires.
func ParseExpiry(text string) (time.Time, bool) {
	m := reExpiry.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// reRemainingLabeled captures the countdown together with its label. Telling it
// apart from the "Accumulated lab time", which appears on the same page and
// measures something else, matters.
var reRemainingLabeled = regexp.MustCompile(`(?i)remaining session time\s*:\s*(\d{1,3}):([0-5]\d):([0-5]\d)`)

// reClock recognises the session countdown: "3:59:30", "03:59", "3h 59m".
var (
	reClockHMS = regexp.MustCompile(`\b(\d{1,2}):([0-5]\d):([0-5]\d)\b`)
	reClockHM  = regexp.MustCompile(`\b(\d{1,2}):([0-5]\d)\b`)
	reClockTxt = regexp.MustCompile(`(?i)\b(\d{1,3})\s*h(?:ours?)?\b(?:\s*(\d{1,2})\s*m)?`)
)

// ParseRemaining interprets the lab session countdown.
//
// It is the number that really matters: it says how long until the credentials
// die and unsaved work is lost.
func ParseRemaining(text string) (time.Duration, bool) {
	// The labelled form first: there are several clocks on the lab page and
	// only one counts what is left of the session.
	if m := reRemainingLabeled.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		s, _ := strconv.Atoi(m[3])
		return time.Duration(h)*time.Hour + time.Duration(min)*time.Minute + time.Duration(s)*time.Second, true
	}
	if m := reClockHMS.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		s, _ := strconv.Atoi(m[3])
		return time.Duration(h)*time.Hour + time.Duration(min)*time.Minute + time.Duration(s)*time.Second, true
	}
	if m := reClockTxt.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		return time.Duration(h)*time.Hour + time.Duration(min)*time.Minute, true
	}
	if m := reClockHM.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		return time.Duration(h)*time.Hour + time.Duration(min)*time.Minute, true
	}
	return 0, false
}

// reBudget recognises the accumulated spend: "$12.34 used of $100".
var reBudget = regexp.MustCompile(`\$\s*([0-9]+(?:\.[0-9]+)?)`)

// ParseBudget returns the lab's spend and cap, in dollars.
//
// The lab is cut off when the budget runs out, so it is worth seeing before it
// happens, not after.
func ParseBudget(text string) (used, total float64, ok bool) {
	m := reBudget.FindAllStringSubmatch(text, 2)
	if len(m) == 0 {
		return 0, 0, false
	}
	used, err := strconv.ParseFloat(m[0][1], 64)
	if err != nil {
		return 0, 0, false
	}
	if len(m) > 1 {
		total, _ = strconv.ParseFloat(m[1][1], 64)
	}
	return used, total, true
}
