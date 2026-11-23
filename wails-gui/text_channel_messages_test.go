package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"wails-gui/internal/destinations"
)

func TestAppleScriptStringEscapesLiterals(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "plain text", `"plain text"`},
		{"double quotes", `say "hi"`, `"say \"hi\""`},
		{"backslashes", `C:\Users\me`, `"C:\\Users\\me"`},
		{"line breaks and tabs", "one\ntwo\tthree\r", `"one\ntwo\tthree\r"`},
		{"control characters become spaces", "bell\x07x", `"bell x"`},
		{"unicode passes through", "café 批准", `"café 批准"`},
		{"a backslash before a quote", `\"`, `"\\\""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appleScriptString(tc.in); got != tc.want {
				t.Fatalf("appleScriptString(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestMessagesSendScriptTargetsTheServiceAndEscapesTheBody(t *testing.T) {
	script := messagesSendScript(messagesServiceIMessage, testTextNumber, `He said "later". Open Isoform.`)
	want := "tell application \"Messages\"\n" +
		"\tif not (exists service whose enabled is true) then error \"Messages is not signed in\" number -10001\n" +
		"\tset targetService to first service whose service type = iMessage\n" +
		"\tset targetBuddy to buddy \"+14155550123\" of targetService\n" +
		"\tsend \"He said \\\"later\\\". Open Isoform.\" to targetBuddy\n" +
		"end tell"
	if script != want {
		t.Fatalf("script =\n%s\nwant\n%s", script, want)
	}
	sms := messagesSendScript(messagesServiceSMS, testTextNumber, "Isoform needs a decision. Open Isoform.")
	if !strings.Contains(sms, "first service whose service type = SMS") {
		t.Fatalf("SMS script does not target the SMS service:\n%s", sms)
	}
}

func TestMessagesFailureClassification(t *testing.T) {
	if err := messagesScriptFailure("Messages is not signed in (-10001)"); !errors.Is(err, errTextMessagesSignedOut) {
		t.Fatalf("signed out: %v", err)
	}
	denied := "execution error: Not authorized to send Apple events to Messages. (-1743)"
	if !messagesAutomationDenied(denied) {
		t.Fatal("the -1743 refusal must be recognised as a permission problem")
	}
	missingBuddy := `execution error: Can’t get buddy "+14155550123" of service "iMessage". (-1728)`
	if messagesAutomationDenied(missingBuddy) {
		t.Fatal("a missing buddy is an ordinary failure, not a permission problem")
	}
	if got := messagesErrorCode(missingBuddy); got != "-1728" {
		t.Fatalf("error code = %q, want -1728", got)
	}
	if got := messagesErrorCode("no code on this line"); got != "" {
		t.Fatalf("error code without a code = %q", got)
	}

	ordinary := messagesFailure{kind: messagesFailed, code: "-1728"}
	if strings.Contains(ordinary.Error(), "+1415") || !strings.Contains(ordinary.Error(), "-1728") {
		t.Fatalf("failure text must carry the code and not the number: %q", ordinary.Error())
	}
	if !errors.Is(messagesFailure{kind: messagesNotAllowed}, errTextMessagesNotAllowed) {
		t.Fatal("a refused Apple event must wrap errTextMessagesNotAllowed")
	}
	if errors.Is(messagesFailure{kind: messagesTimedOut}, errTextMessagesNotAllowed) {
		t.Fatal("a timeout is not a permission refusal")
	}
}

func TestMessagesSenderFallsBackToSMSOnlyForOrdinaryFailures(t *testing.T) {
	ordinary := messagesFailure{kind: messagesFailed, code: "-1728"}
	cases := []struct {
		name      string
		first     error
		second    error
		wantCalls int
		wantErr   error
		wantFail  bool
	}{
		{name: "iMessage works", wantCalls: 1},
		{name: "an ordinary iMessage failure falls back to SMS", first: ordinary, wantCalls: 2},
		{name: "a refused Apple event does not fall back", first: messagesFailure{kind: messagesNotAllowed}, wantCalls: 1, wantErr: errTextMessagesNotAllowed, wantFail: true},
		{name: "signed out does not fall back", first: messagesFailure{kind: messagesSignedOut}, wantCalls: 1, wantErr: errTextMessagesSignedOut, wantFail: true},
		{name: "a timeout does not fall back", first: messagesFailure{kind: messagesTimedOut}, wantCalls: 1, wantFail: true},
		{name: "both services fail", first: ordinary, second: ordinary, wantCalls: 2, wantFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := observeDestinationCalls(t)
			var scripts []string
			sender := messagesSender{run: func(_ context.Context, script string) error {
				scripts = append(scripts, script)
				if len(scripts) == 1 {
					return tc.first
				}
				return tc.second
			}}
			err := sender.Send(context.Background(), testTextNumber, "Isoform needs a decision. Open Isoform.")
			if err == nil {
				expectDestination(t, calls, destinations.TextMessages, "")
			}
			if len(scripts) != tc.wantCalls {
				t.Fatalf("osascript runs = %d, want %d", len(scripts), tc.wantCalls)
			}
			if tc.wantCalls == 2 && !strings.Contains(scripts[1], "service type = SMS") {
				t.Fatalf("the fallback must use the SMS service:\n%s", scripts[1])
			}
			if tc.wantFail != (err != nil) {
				t.Fatalf("error = %v, want failure %t", err, tc.wantFail)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "+1415") {
				t.Fatalf("a send error must not carry the number: %q", err.Error())
			}
		})
	}
}

func TestMessagesSenderRefusesABadNumberBeforeRunning(t *testing.T) {
	called := false
	sender := messagesSender{run: func(context.Context, string) error {
		called = true
		return nil
	}}
	if err := sender.Send(context.Background(), `+1"; do shell script "id"`, "body"); !errors.Is(err, errTextChannelNumber) {
		t.Fatalf("bad number error = %v", err)
	}
	if called {
		t.Fatal("a bad number must never reach osascript")
	}
}
