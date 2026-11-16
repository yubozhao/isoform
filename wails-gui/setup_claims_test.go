package main

import (
	"strings"
	"testing"

	"wails-gui/storage"
)

func TestParseSetupClaimsTakesSourceAndDayFromTheCitedLine(t *testing.T) {
	evidence := []setupClaimEvidence{
		{Source: "calendar", At: 100, Line: "Calendar: 3 meeting(s) this week with Lena"},
		{Source: "mail", At: 200, Line: "Mail: 4 message(s) from Priya"},
	}
	raw := `Sure: {"claims":[
		{"text":"You meet Lena most weeks, about the Halcyon import.","evidence":1},
		{"text":"Priya runs Thursday's review.","evidence":2},
		{"text":"Made up","evidence":9},
		{"text":"You meet Lena most weeks, about the Halcyon import.","evidence":1},
		{"text":"Your key is sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","evidence":2}
	]}`
	got := parseSetupClaims(raw, evidence)
	if len(got) != 2 {
		t.Fatalf("claims = %+v, want two", got)
	}
	if got[0].Source != "calendar" || got[0].At != 100 || got[1].Source != "mail" || got[1].At != 200 {
		t.Fatalf("evidence not taken from the cited lines: %+v", got)
	}
	if parseSetupClaims("no json", evidence) != nil {
		t.Fatal("a reply without JSON must give no claims")
	}
}

func TestParseSetupClaimsKeepsAtMostFive(t *testing.T) {
	evidence := []setupClaimEvidence{{Source: "mail", At: 1, Line: "x"}}
	var items []string
	for _, name := range []string{"Ana", "Ben", "Cai", "Dee", "Eli", "Fay", "Gus"} {
		items = append(items, `{"text":"You work with `+name+` on the launch.","evidence":1}`)
	}
	got := parseSetupClaims(`{"claims":[`+strings.Join(items, ",")+`]}`, evidence)
	if len(got) != setupClaimsCap {
		t.Fatalf("claims = %d, want %d", len(got), setupClaimsCap)
	}
}

func TestSetupClaimsMailLinesReadOnlyInboxMailToThePerson(t *testing.T) {
	rows := []storage.MailMessageRow{
		{Mailbox: storage.MailboxInbox, ToMe: true, SenderEmail: "priya@example.com", SenderName: "Priya", Subject: "Thursday", At: 3},
		{Mailbox: storage.MailboxInbox, ToMe: true, SenderEmail: "Priya@example.com", SenderName: "Priya", Subject: "Agenda", At: 5},
		{Mailbox: storage.MailboxInbox, ToMe: true, SenderEmail: "lena@example.com", SenderName: "Lena", Subject: "Import", At: 9},
		{Mailbox: storage.MailboxInbox, ToMe: false, SenderEmail: "list@example.com", SenderName: "List", Subject: "Digest", At: 9},
		{Mailbox: "sent", ToMe: true, SenderEmail: "me@example.com", SenderName: "Me", Subject: "Re", At: 9},
	}
	got := setupClaimsMailLines(rows)
	if len(got) != 2 {
		t.Fatalf("lines = %+v, want Priya and Lena", got)
	}
	if !strings.Contains(got[0].Line, "2 message(s) from Priya") || got[0].At != 5 {
		t.Fatalf("first line = %+v, want Priya with two messages", got[0])
	}
	if strings.Contains(got[0].Line+got[1].Line, "Digest") {
		t.Fatal("mail not sent to the person must not be read")
	}
}

func TestSetupClaimsPromptNumbersTheLinesAndAsksForTheLanguage(t *testing.T) {
	prompt := buildSetupClaimsPrompt([]setupClaimEvidence{{Line: "Calendar: one"}, {Line: "Mail: two"}}, "English")
	for _, want := range []string{"L1. Calendar: one", "L2. Mail: two", "Write every text value in English"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
}

func TestReviseSetupClaimRefusesOtherEntries(t *testing.T) {
	if err := (&App{}).ReviseSetupClaim("memory-history-abc", "words"); err == nil {
		t.Fatal("only setup claims can be revised here")
	}
}
