package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"wails-gui/internal/platform/nativeprompt"
)

const (
	messagesBundleID        = "com.apple.MobileSMS"
	messagesOsascriptPath   = "/usr/bin/osascript"
	messagesOpenPath        = "/usr/bin/open"
	messagesSendTimeout     = 10 * time.Second
	messagesLaunchWait      = 5 * time.Second
	messagesServiceIMessage = "iMessage"
	messagesServiceSMS      = "SMS"
)

// messagesErrorCodePattern matches the "(-1728)" at the end of an AppleScript
// error line. The code is kept and the rest of the line is dropped, because it
// quotes the number.
var messagesErrorCodePattern = regexp.MustCompile(`\((-?[0-9]+)\)\s*$`)

type messagesFailureKind int

const (
	messagesFailed messagesFailureKind = iota
	messagesTimedOut
	messagesNotAllowed
	messagesSignedOut
)

// messagesFailure is one failed osascript run. Its text carries the error
// code and never the number or the body, so it is safe to store and show.
type messagesFailure struct {
	kind messagesFailureKind
	code string
}

func (f messagesFailure) Error() string {
	switch f.kind {
	case messagesTimedOut:
		return "Messages did not answer within 10 seconds"
	case messagesNotAllowed:
		return "automation for Messages is not allowed; turn it on in System Settings > Privacy & Security > Automation"
	case messagesSignedOut:
		return errTextMessagesSignedOut.Error()
	default:
		if f.code == "" {
			return "Messages could not send the text"
		}
		return fmt.Sprintf("Messages could not send the text (error %s)", f.code)
	}
}

func (f messagesFailure) Unwrap() error {
	if f.kind == messagesNotAllowed {
		return errTextMessagesNotAllowed
	}
	if f.kind == messagesSignedOut {
		return errTextMessagesSignedOut
	}
	return nil
}

// messagesSender sends through Messages.app with osascript. run is injectable
// for tests; a zero messagesSender runs osascript.
type messagesSender struct {
	run func(ctx context.Context, script string) error
}

// Send tries the iMessage service first. It falls back to the SMS service only
// for an ordinary failure (see messagesFailure). A permission refusal and a
// timeout do not fall back: SMS would be refused too, and a timed-out send may
// already have gone out. SMS needs Text Message Forwarding on the iPhone.
func (s messagesSender) Send(ctx context.Context, to, body string) error {
	if !textChannelNumberPattern.MatchString(to) {
		return errTextChannelNumber
	}
	run := s.run
	if run == nil {
		if err := prepareMessagesSend(); err != nil {
			return err
		}
		run = runMessagesScript
	}
	err := run(ctx, messagesSendScript(messagesServiceIMessage, to, body))
	if err == nil {
		return nil
	}
	var failure messagesFailure
	if !errors.As(err, &failure) || failure.kind != messagesFailed {
		return err
	}
	if smsErr := run(ctx, messagesSendScript(messagesServiceSMS, to, body)); smsErr != nil {
		return fmt.Errorf("%w; the SMS service also failed: %w", err, smsErr)
	}
	return nil
}

// messagesSendScript builds the AppleScript for one service. The service name
// is one of the constants above. The number is checked by the caller and the
// body is escaped by appleScriptString.
//
//	tell application "Messages"
//		set targetService to first service whose service type = iMessage
//		set targetBuddy to buddy "+14155550123" of targetService
//		send "Yansu needs a decision. Open Isoform." to targetBuddy
//	end tell
func messagesSendScript(service, to, body string) string {
	return "tell application \"Messages\"\n" +
		"\tif not (exists service whose enabled is true) then error \"Messages is not signed in\" number -10001\n" +
		"\tset targetService to first service whose service type = " + service + "\n" +
		"\tset targetBuddy to buddy " + appleScriptString(to) + " of targetService\n" +
		"\tsend " + appleScriptString(body) + " to targetBuddy\n" +
		"end tell"
}

// appleScriptString quotes s as an AppleScript string literal. Backslash and
// double quote are escaped, newline, carriage return and tab become \n, \r and
// \t, and other control characters become spaces.
func appleScriptString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// messagesAutomationDenied matches the osascript text for a refused (-1743) or
// undecided (-1744) Apple event. The hyphenated codes cannot come from an E.164
// number, which has no hyphens.
func messagesAutomationDenied(output string) bool {
	return strings.Contains(output, "-1743") ||
		strings.Contains(output, "-1744") ||
		strings.Contains(output, "Not authorized to send Apple events") ||
		strings.Contains(output, "not allowed to send Apple events")
}

// messagesErrorCode returns the trailing error code of an osascript error, such
// as "-1728", or "" when there is none.
func messagesErrorCode(output string) string {
	match := messagesErrorCodePattern.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return ""
	}
	return match[1]
}

// runMessagesScript runs one script with osascript under a 10 second timeout.
// The parent context's error wins over the classification, so a cancelled
// caller never falls back to SMS.
//
//isoform:destination text_messages
func runMessagesScript(ctx context.Context, script string) error {
	if !nativeprompt.Allowed() && messagesOsascriptPath == "/usr/bin/osascript" {
		return messagesFailure{kind: messagesNotAllowed}
	}
	runCtx, cancel := context.WithTimeout(ctx, messagesSendTimeout)
	defer cancel()
	out, err := exec.CommandContext(runCtx, messagesOsascriptPath, "-e", script).CombinedOutput()
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return messagesFailure{kind: messagesTimedOut}
	}
	return messagesScriptFailure(string(out))
}

func messagesScriptFailure(output string) error {
	if messagesAutomationDenied(output) {
		return messagesFailure{kind: messagesNotAllowed}
	}
	if messagesErrorCode(output) == "-10001" {
		return messagesFailure{kind: messagesSignedOut}
	}
	return messagesFailure{kind: messagesFailed, code: messagesErrorCode(output)}
}
