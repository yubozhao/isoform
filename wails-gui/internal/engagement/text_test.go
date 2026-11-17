package engagement

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTextCopyCoversEveryKind(t *testing.T) {
	for _, lang := range Languages {
		for _, kind := range AllKinds() {
			set, _ := Copy(kind, lang)
			if !strings.HasPrefix(set.Text, "Isoform") {
				t.Fatalf("%s %s text must start with Isoform: %q", kind, lang, set.Text)
			}
			if strings.Contains(set.Text, "!") || strings.Contains(set.Text, "\n") {
				t.Fatalf("%s %s text must be one calm line: %q", kind, lang, set.Text)
			}
			text, ok := TextMessage(kind, lang, nil)
			if !ok || utf8.RuneCountInString(text) > TextLimit || strings.Contains(text, "{") {
				t.Fatalf("%s %s text renders badly: %q", kind, lang, text)
			}
			// Every code the kind accepts is named in its text, in order.
			at := 0
			for _, reply := range TextReplies(kind) {
				i := strings.Index(set.Text[at:], " "+reply.Code+" ")
				if i < 0 {
					t.Fatalf("%s %s text does not offer %s in order: %q", kind, lang, reply.Code, set.Text)
				}
				at += i + 1
			}
		}
	}
}

func TestTextRepliesAreTheKindsOwnUnguardedActions(t *testing.T) {
	for kind, replies := range textReplies {
		spec := mustSpec(t, kind)
		seen := map[string]bool{}
		for _, reply := range replies {
			if seen[reply.Code] || len(reply.Code) != 1 {
				t.Fatalf("%s: code %q repeated or too long", kind, reply.Code)
			}
			seen[reply.Code] = true
			found := false
			for _, action := range spec.Actions {
				if action.ID == reply.Action {
					found = true
					if action.Authenticated {
						t.Fatalf("%s: %s needs the owner check and must not be answerable by text", kind, action.ID)
					}
				}
			}
			if !found {
				t.Fatalf("%s: %s is not one of the kind's actions", kind, reply.Action)
			}
		}
	}
}

func TestMatchTextReply(t *testing.T) {
	cases := []struct {
		kind  Kind
		reply string
		want  string
		ok    bool
	}{
		{KindWorkBlocked, "A", "answer", true},
		{KindWorkBlocked, " l ", "later", true},
		{KindWorkBlocked, "1", "answer", true},
		{KindWorkBlocked, "2.", "later", true},
		{KindWorkBlocked, "3", "", false},
		{KindWorkBlocked, "S", "", false},
		{KindCommitmentSlipping, "t!", "", false},
		{KindCommitmentSlipping, "N", "", false},
		{KindSuggestion, "n", "dismiss", true},
		{KindSuggestion, "Y", "", false},
		{KindLearnedToConfirm, "k", "keep", true},
		{KindBriefReady, "1", "", false},
		{KindGoalCheckIn, "going well", "", false},
		{KindGoalCheckIn, "0", "", false},
	}
	for _, c := range cases {
		got, ok := MatchTextReply(c.kind, c.reply)
		if got != c.want || ok != c.ok {
			t.Fatalf("%s %q: %q %t, want %q %t", c.kind, c.reply, got, ok, c.want, c.ok)
		}
	}
}

func TestTextOffersRepliesRequiresTheActualCompleteInstructions(t *testing.T) {
	for _, lang := range Languages {
		for _, kind := range AllKinds() {
			body, _ := TextMessage(kind, lang, map[string]string{"title": strings.Repeat("Quarterly review ", 20), "question": "the budget code."})
			want := len(TextReplies(kind)) > 0
			if got := TextOffersReplies(kind, lang, body); got != want {
				t.Errorf("%s %s offered codes=%t, want %t: %q", lang, kind, got, want, body)
			}
			generic, _ := GenericText(kind, lang)
			if TextOffersReplies(kind, lang, generic) {
				t.Errorf("%s %s generic text opened a code question: %q", lang, kind, generic)
			}
			if want && TextOffersReplies(kind, lang, string([]rune(body)[:len([]rune(body))-1])) {
				t.Errorf("%s %s incomplete instructions opened a code question", lang, kind)
			}
		}
	}
}

func TestTextMessageFitsAndKeepsTheCodes(t *testing.T) {
	long := strings.Repeat("Quarterly planning review ", 12)
	text, ok := TextMessage(KindWorkBlocked, "en", map[string]string{"title": long, "question": "the\nbudget\tcode."})
	if !ok || utf8.RuneCountInString(text) > TextLimit {
		t.Fatalf("too long: %d %q", utf8.RuneCountInString(text), text)
	}
	if !strings.HasSuffix(text, "Reply A to answer in chat, L for later.") || !strings.Contains(text, "needs the budget code.") {
		t.Fatalf("codes or question lost: %q", text)
	}
	if !strings.Contains(text, "...") {
		t.Fatalf("the long title was not marked as cut: %q", text)
	}
	zh, ok := TextMessage(KindCommitmentSlipping, "zh-CN", map[string]string{"title": strings.Repeat("季度规划评审", 30), "due": "明天"})
	if !ok || utf8.RuneCountInString(zh) > TextLimit || !strings.HasSuffix(zh, "打开 Isoform 查看。") {
		t.Fatalf("zh text %d %q", utf8.RuneCountInString(zh), zh)
	}
	short, _ := TextMessage(KindCapturePaused, "en", map[string]string{"what": "Screen capture", "duration": "2 hours"})
	if short != "Isoform: Screen capture has been off for 2 hours. Turn it back on in Isoform." {
		t.Fatalf("plain text %q", short)
	}
	// Two long parameters are both cut until the text fits.
	both, _ := TextMessage(KindWaitingOnReply, "en", map[string]string{"name": strings.Repeat("N", 120), "headline_subject": strings.Repeat("S", 120)})
	if utf8.RuneCountInString(both) > TextLimit || !strings.HasSuffix(both, ".") {
		t.Fatalf("two long params %d %q", utf8.RuneCountInString(both), both)
	}
	if _, ok := TextMessage(Kind("unknown"), "en", nil); ok {
		t.Fatal("an unknown kind rendered a text")
	}
	if TextHint("fr") != TextHint("en") || !strings.HasPrefix(TextHint("zh-CN"), "Isoform") {
		t.Fatal("hint languages")
	}
}

// No text message or email stands in for a notification (notify v1.0.0 rule
// 14): with no phone to reach, the computer shows it.
func TestChannelForKindNeverTextsOrEmails(t *testing.T) {
	now := at(10, 0)
	urgent := Verdict{Mode: ModeInterrupt, Interruption: InterruptionTimeSensitive}
	for _, p := range []Presence{
		{SMSActive: true, MacIdle: time.Hour},
		{TextAvailable: true, MacIdle: time.Hour},
		{EmailActive: true, MacIdle: time.Hour, PhoneRegistered: true, PushFailed: true},
	} {
		if got := ChannelForKind(KindMeetingPrep, urgent, p, now); got != ChannelDesktop {
			t.Errorf("%+v: %s, want the computer", p, got)
		}
	}
}

func TestTextMessageDropsLinksAndPhoneNumbers(t *testing.T) {
	cases := []struct {
		kind   Kind
		params map[string]string
		gone   []string
		kept   []string
	}{
		{KindWorkFinished, map[string]string{"title": "Sign in at https://evil.example.com/login now"}, []string{"https://", "evil.example"}, []string{"Sign in at", "now"}},
		{KindWorkFinished, map[string]string{"title": "See bit.ly/x1 or www.example.org"}, []string{"bit.ly", "www."}, []string{"See", "or"}},
		{KindWaitingOnReply, map[string]string{"name": "Ana", "headline_subject": "Call me at (415) 555-0199"}, []string{"555", "0199"}, []string{"Ana", "Call me at"}},
		{KindWorkFinished, map[string]string{"title": "report.pdf for Q3 2026"}, nil, []string{"report.pdf", "Q3 2026"}},
	}
	for _, c := range cases {
		text, ok := TextMessage(c.kind, "en", c.params)
		if !ok {
			t.Fatalf("no text for %s", c.kind)
		}
		for _, gone := range c.gone {
			if strings.Contains(text, gone) {
				t.Fatalf("%q kept %q", text, gone)
			}
		}
		for _, kept := range c.kept {
			if !strings.Contains(text, kept) {
				t.Fatalf("%q lost %q", text, kept)
			}
		}
		if textLinkPattern.MatchString(text) || !strings.HasPrefix(text, "Isoform:") {
			t.Fatalf("%q would be refused by the relay", text)
		}
		for _, run := range textPhoneRunPattern.FindAllString(text, -1) {
			digits := 0
			for _, r := range run {
				if r >= '0' && r <= '9' {
					digits++
				}
			}
			if digits >= textPhoneDigits {
				t.Fatalf("%q still carries a phone number", text)
			}
		}
	}
}

// TestTextPatternsMatchTheRelay keeps the Mac's copy of the relay's link and
// phone patterns (relay/internal/sms/text.go) the same as the original.
func TestTextPatternsMatchTheRelay(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "relay", "internal", "sms", "text.go"))
	if err != nil {
		t.Skipf("relay source not beside this module: %v", err)
	}
	relay := string(raw)
	// The relay builds the link pattern around its TLD constant.
	linkSource := strings.Replace(textLinkPattern.String(), textLinkTLDs, "` + linkTLDs + `", 1)
	for _, want := range []string{linkSource, textPhoneRunPattern.String(), `linkTLDs = "` + textLinkTLDs + `"`, "phoneDigits = 7"} {
		if !strings.Contains(relay, want) {
			t.Fatalf("the relay's text.go does not carry %s", want)
		}
	}
}
