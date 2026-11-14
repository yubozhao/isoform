package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"wails-gui/storage"

	"yansu-agent/agent/tools"
)

func TestWebSearchQueryRefusal(t *testing.T) {
	names := []string{"Dana Whitfield", "Bo Example"}
	for _, tc := range []struct {
		query   string
		refused bool
	}{
		{"Vision Pro price in Japan 2026", false},
		{"Wails v2 WebView devtools on macOS", false},
		{"what did Danaher report this quarter", false},
		{"", true},
		{strings.Repeat("word ", 50), true},
		{"invoice from dana@example.com", true},
		{"who owns +1 415 555 0132", true},
		{"order 4111 1111 1111 1111 status", true},
		{"tracking 1Z999 order 88231457", true},
		{"https://shop.example.com/order?id=123", true},
		{"Dana Whitfield new job", true},
		{"dana whitfield linkedin", true},
		{"bo example portfolio", true},
	} {
		got := webSearchQueryRefusal(tc.query, names)
		if (got != "") != tc.refused {
			t.Errorf("query %q: refusal %q, want refused=%v", tc.query, got, tc.refused)
		}
	}
}

func webSearchTestApp(t *testing.T, people ...string) *App {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "yansu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, name := range people {
		if _, err := store.CreatePrincipal(t.Context(), storage.PrincipalRow{Kind: "contact", DisplayName: name}); err != nil {
			t.Fatal(err)
		}
	}
	return &App{store: store}
}

func stubWebSearch(t *testing.T, ready bool, answer string, meta researchMeta, err error) *[]string {
	t.Helper()
	var prompts []string
	prevRun, prevReady := chatWebSearchRun, chatWebSearchReady
	chatWebSearchRun = func(_ context.Context, prompt string) (string, researchMeta, error) {
		prompts = append(prompts, prompt)
		return answer, meta, err
	}
	chatWebSearchReady = func() bool { return ready }
	t.Cleanup(func() { chatWebSearchRun, chatWebSearchReady = prevRun, prevReady })
	return &prompts
}

func searchCall(query string) tools.ToolCall {
	return tools.ToolCall{ID: "toolu_1", Input: map[string]any{"query": query}}
}

func TestChatWebSearchAnswersWithItsLinks(t *testing.T) {
	links := make([]string, 0, 10)
	for i := range 10 {
		links = append(links, fmt.Sprintf("https://news.example.com/%d", i))
	}
	prompts := stubWebSearch(t, true, "Prices start at 599,800 yen.", researchMeta{Via: researchViaClaudeCode, Results: 2, URLs: links}, nil)
	tool := &chatWebSearchTool{app: webSearchTestApp(t)}
	result, err := tool.Execute(t.Context(), searchCall("Vision Pro   price in Japan"))
	if err != nil || result.IsError {
		t.Fatalf("search failed: %v %+v", err, result)
	}
	if !strings.HasPrefix(result.Content, "Prices start at 599,800 yen.") || !strings.Contains(result.Content, links[7]) || strings.Contains(result.Content, links[8]) {
		t.Fatalf("answer or its first 8 links missing: %q", result.Content)
	}
	if len(*prompts) != 1 || !strings.Contains((*prompts)[0], `"Vision Pro price in Japan"`) {
		t.Fatalf("the sandboxed search got %q", *prompts)
	}
}

func TestChatWebSearchKeepsPersonalDetailsOnTheComputer(t *testing.T) {
	prompts := stubWebSearch(t, true, "unused", researchMeta{Results: 1}, nil)
	tool := &chatWebSearchTool{app: webSearchTestApp(t, "Dana Whitfield", "Lee")}
	for _, query := range []string{"Dana Whitfield new job", "mail dana@example.com", "account 99887766"} {
		result, err := tool.Execute(t.Context(), searchCall(query))
		if err != nil || !result.IsError || !strings.HasPrefix(result.Content, "Not searched:") {
			t.Fatalf("query %q was not refused: %v %+v", query, err, result)
		}
	}
	if len(*prompts) != 0 {
		t.Fatalf("a refused query left the computer: %q", *prompts)
	}
	// A single first name is not treated as private.
	if result, _ := tool.Execute(t.Context(), searchCall("Lee Kuan Yew biography")); result.IsError {
		t.Fatalf("a public query was refused: %q", result.Content)
	}
}

func TestChatWebSearchSaysPlainlyWhenItCannot(t *testing.T) {
	t.Run("not signed in", func(t *testing.T) {
		prompts := stubWebSearch(t, false, "", researchMeta{}, nil)
		result, _ := (&chatWebSearchTool{app: webSearchTestApp(t)}).Execute(t.Context(), searchCall("Go 1.26 release notes"))
		if !result.IsError || !strings.Contains(result.Content, "can't search the web right now") || len(*prompts) != 0 {
			t.Fatalf("got %+v, prompts %q", result, *prompts)
		}
	})
	t.Run("search failed", func(t *testing.T) {
		stubWebSearch(t, true, "", researchMeta{}, errors.New("the search took too long"))
		result, _ := (&chatWebSearchTool{app: webSearchTestApp(t)}).Execute(t.Context(), searchCall("Go 1.26 release notes"))
		if !result.IsError || !strings.Contains(result.Content, "didn't work") {
			t.Fatalf("got %+v", result)
		}
	})
	t.Run("nothing returned", func(t *testing.T) {
		stubWebSearch(t, true, "I could not find it.", researchMeta{}, nil)
		result, _ := (&chatWebSearchTool{app: webSearchTestApp(t)}).Execute(t.Context(), searchCall("Go 1.26 release notes"))
		if !result.IsError || !strings.Contains(result.Content, "returned nothing") {
			t.Fatalf("got %+v", result)
		}
	})
}

func TestWebSearchContextIsForTheMainChatOnly(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []promptOrigin{promptOriginMac, promptOriginPhone} {
		got := rig.app.injectWebSearchToolContext(main.LocalID, origin)
		if !strings.Contains(got, "with SearchWeb, not opencli") || !strings.Contains(got, "The query leaves this computer") {
			t.Fatalf("origin %q context = %q", origin, got)
		}
	}
	for _, origin := range []promptOrigin{promptOriginPanelAsk, promptOriginPhoneAsk, promptOriginResume} {
		if got := rig.app.injectWebSearchToolContext(main.LocalID, origin); got != "" {
			t.Fatalf("origin %q got the search context", origin)
		}
	}
	if got := rig.app.injectWebSearchToolContext("some-other-chat", promptOriginMac); got != "" {
		t.Fatal("another chat got the search context")
	}
}
