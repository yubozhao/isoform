package identity

import (
	"context"
	"testing"

	"wails-gui/storage"
)

// A WeChat reader rendered one name with one and with two spaces between
// words, and each spelling became its own person on Who you know.
func TestNormalizeSenderNameCollapsesSpacing(t *testing.T) {
	cases := map[string]string{
		"Mei  Chen":              "Mei Chen",
		"Mei Chen":               "Mei Chen",
		"Mei　Chen":               "Mei Chen",
		"Mei​ Chen":              "Mei Chen",
		"  Mei \t Chen  10:25":   "Mei Chen",
		"Annlee":                 "Annlee",
		"张三":                     "张三",
		"Mei  Chen Server Owner": "Mei Chen",
	}
	for in, want := range cases {
		if got := NormalizeSenderName(in); got != want {
			t.Errorf("NormalizeSenderName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The BOT and APP badges are the platform saying the sender is software.
// Stripping them made "GitHub APP" a person called GitHub.
func TestSenderIsAutomated(t *testing.T) {
	for _, raw := range []string{"GitHub APP", "Github App", "Yansu BOT", "Jira Cloud APP 10:25", "deploy-bot Bot"} {
		if !SenderIsAutomated(raw) {
			t.Errorf("%q should be automated", raw)
		}
	}
	for _, raw := range []string{"Bot", "App", "Maya Chen", "maya_chen OP Original Poster", "Boss Server Owner", "Happy Apple"} {
		if SenderIsAutomated(raw) {
			t.Errorf("%q is a person", raw)
		}
	}
}

func TestIsSystemSender(t *testing.T) {
	for _, name := range []string{"Slackbot", "slackbot", "文件传输助手", "WeChat  Team", "Google Calendar", "GitHub"} {
		if !IsSystemSender(name) {
			t.Errorf("%q is a platform account", name)
		}
	}
	for _, name := range []string{"Claude", "Devin", "Polly", "Mei Chen", "Linear Li", ""} {
		if IsSystemSender(name) {
			t.Errorf("%q could be a person", name)
		}
	}
}

func TestResolverNeverBucketsSystemOrAutomatedSenders(t *testing.T) {
	ctx := context.Background()
	s := newRealStore(t)
	r := NewResolver(s, true)
	for _, name := range []string{"Slackbot", "文件传输助手"} {
		ref, err := r.ResolveSender(ctx, "slack", "", name)
		if err != nil || ref.Kind != SubjectUnknown {
			t.Fatalf("%q resolved to %+v %v", name, ref, err)
		}
	}
	ref, err := r.ResolveExistingSender(ctx, "slack", "", "Deploy Helper")
	if err != nil || ref.Kind != SubjectUnknown {
		t.Fatalf("an unnamed app must not be created: %+v %v", ref, err)
	}
	// The person named this integration; it still resolves.
	named, err := s.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Deploy Helper"})
	if err != nil {
		t.Fatal(err)
	}
	if ref, err = r.ResolveExistingSender(ctx, "slack", "", "Deploy Helper"); err != nil || ref.PrincipalID != named.ID {
		t.Fatalf("a named sender resolves: %+v %v", ref, err)
	}
	rows, err := s.ListPrincipals(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("principals=%d %v", len(rows), err)
	}
}

func TestResolverFindsSpacingVariantOfExistingPerson(t *testing.T) {
	ctx := context.Background()
	s := newRealStore(t)
	// Rows written before names were collapsed keep the doubled space.
	first, err := s.EnsureAnonymousContact(ctx, "wechat", "", "Mei  Chen")
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(s, true)
	for _, variant := range []string{"Mei Chen", "mei chen", "Mei Chen"} {
		ref, err := r.ResolveSender(ctx, "wechat", "", variant)
		if err != nil || ref.PrincipalID != first.ID {
			t.Fatalf("%q -> %+v %v, want %s", variant, ref, err, first.ID)
		}
	}
	// Spacing is folded, not removed: a different name stays a different person.
	other, err := r.ResolveSender(ctx, "wechat", "", "MeiChen")
	if err != nil || other.PrincipalID == "" || other.PrincipalID == first.ID {
		t.Fatalf("MeiChen merged into Mei Chen: %+v %v", other, err)
	}
	// A display-name match through the alias path folds spacing too.
	contact, err := s.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Sam  Lee"})
	if err != nil {
		t.Fatal(err)
	}
	if ref, err := r.ResolveSender(ctx, "slack", "", "Sam Lee"); err != nil || ref.PrincipalID != contact.ID {
		t.Fatalf("alias spacing: %+v %v", ref, err)
	}
}
