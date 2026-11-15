package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestDreamSynthesisPreservesVersionRefsAndReadProvenance(t *testing.T) {
	for _, frozen := range []bool{false, true} {
		name := "live source"
		if frozen {
			name = "frozen source"
		}
		t.Run(name, func(t *testing.T) {
			app := newMCPReadTestApp(t)
			seedMCPReadActivity(t, app, "dream-source", "Editor", "com.example.editor", "ordinary", "", "")
			writeMCPReadMemory(t, "dream-input", "dream-source", "versioned source detail")
			entry, err := app.GetMemoryEntry("dream-input")
			if err != nil {
				t.Fatal(err)
			}
			refs := []string{taskContextSourceRef(taskContextSourceMemoryKind, entry.ID, entry.ModTime, entry.Content)}
			service := &MemoryDreamService{app: app}
			var id string
			switch frozen {
			case true:
				provenance := memoryReadProvenanceForFile(filepath.Join(configDirOrFallback(), "memory"), entry.ID, entry.Content)
				id, err = service.writeSynthesizedMemoryWithProvenance("Merged detail", "mergeprivacyneedle versioned detail", "user_insight", []string{entry.ID}, []memoryReadProvenance{provenance}, refs...)
			case false:
				id, err = service.writeSynthesizedMemory("Merged detail", "mergeprivacyneedle versioned detail", "user_insight", []string{entry.ID}, refs...)
			}
			if err != nil {
				t.Fatal(err)
			}
			merged, err := app.GetMemoryEntry(id)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := parseOKFDocument(merged.Content); !ok {
				t.Fatal("synthesis lost the OKF document")
			}
			if got := memoryDerivedSourceRefs(merged.Content); !reflect.DeepEqual(got, refs) {
				t.Fatalf("versioned source refs=%v, want %v", got, refs)
			}
			provenance := memoryReadProvenanceFromContent(merged.Content)
			if provenance.Withheld || !reflect.DeepEqual(provenance.Sessions, []string{"dream-source"}) {
				t.Fatalf("synthesis lost read provenance: %+v", provenance)
			}
			if got := callMCPReadTest(t, app, "memory_search", map[string]any{"query": "mergeprivacyneedle"}); len(got.Items) != 1 {
				t.Fatalf("readable synthesis withheld: %+v", got)
			}
			if err := app.store.SetUIState(app.ctx, "sensitive_apps", `{"com.example.editor":"Editor"}`); err != nil {
				t.Fatal(err)
			}
			if got := callMCPReadTest(t, app, "memory_search", map[string]any{"query": "mergeprivacyneedle"}); len(got.Items) != 0 {
				t.Fatalf("synthesis lost its live source gate: %+v", got)
			}
		})
	}
}

func TestObservedMemoryWritesComposeCallerContextAndReadProvenance(t *testing.T) {
	for _, writer := range []string{"generator", "activity"} {
		for _, cancelled := range []bool{false, true} {
			name := writer + "/active context"
			if cancelled {
				name = writer + "/cancelled optional probe"
			}
			t.Run(name, func(t *testing.T) {
				app := newMCPReadTestApp(t)
				at := seedMCPReadActivity(t, app, "merge-observation", "Editor", "com.example.editor", "ordinary", "", "")
				provenance := memoryReadProvenance{Version: 2, Kinds: []string{"activity"}, Sessions: []string{"merge-observation"}}
				input := &mcMemoryGenerationResponse{Title: "Observed planning", Dimension: "user_insight", MemoryPoints: []string{"contextprivacyneedle tracks planning"}, inputProvenance: &provenance}
				var calls atomic.Int32
				app.memoryCanonicalizer = func(string, string) (string, error) {
					calls.Add(1)
					return `{"version":"isoform-memory-semantics-v1","status":"no_facts","facts":[]}`, nil
				}
				ctx, cancel := context.WithCancel(app.ctx)
				defer cancel()
				if cancelled {
					cancel()
				}
				write := func() (string, error) {
					switch writer {
					case "generator":
						return (&MemoryGenerator{app: app}).saveMemoryFileContext(ctx, memorySourceConversation, "merge-thread", input)
					default:
						session := ActivitySessionRow{ID: "merge-observation", StartTime: at, AppNames: `["Editor"]`}
						_, err := (&ActivityTriager{app: app}).saveActivityMemoryContext(ctx, session, sessionGroup{Sessions: []ActivitySessionRow{session}}, input)
						return activityMemoryEntryID(session.ID), err
					}
				}
				id, err := write()
				if err != nil {
					t.Fatal(err)
				}
				wantCalls := int32(1)
				if cancelled {
					wantCalls = 0
				}
				if calls.Load() != wantCalls {
					t.Fatalf("optional probe calls=%d, want %d", calls.Load(), wantCalls)
				}
				entry, err := app.GetMemoryEntry(id)
				if err != nil {
					t.Fatal(err)
				}
				gotProvenance := memoryReadProvenanceFromContent(entry.Content)
				if gotProvenance.Withheld || !reflect.DeepEqual(gotProvenance.Sessions, provenance.Sessions) {
					t.Fatalf("context-aware writer lost read provenance: %+v", gotProvenance)
				}
				if err := app.store.SetUIState(app.ctx, "sensitive_apps", `{"com.example.editor":"Editor"}`); err != nil {
					t.Fatal(err)
				}
				if got := callMCPReadTest(t, app, "memory_list", map[string]any{}); len(got.Items) != 0 {
					t.Fatalf("context-aware writer lost the source gate: %+v", got)
				}
				if err := app.store.SetUIState(app.ctx, "sensitive_apps", `{}`); err != nil {
					t.Fatal(err)
				}
				if got := callMCPReadTest(t, app, "memory_list", map[string]any{}); len(got.Items) != 1 {
					t.Fatalf("fixture lineage was not readable before forgetting: %+v", got)
				}
				dir, err := memoryDir()
				if err != nil {
					t.Fatal(err)
				}
				if err := recordMemoryTombstone(dir, id, []string{entry.Content}, time.Now()); err != nil {
					t.Fatal(err)
				}
				if _, err := write(); !errors.Is(err, errMemoryForgotten) {
					t.Fatalf("readable provenance bypassed observed-write suppression: %v", err)
				}
				if calls.Load() != wantCalls {
					t.Fatal("forgotten observation reached optional semantic preparation")
				}
			})
		}
	}
}

func TestMemoryUpdateFallbackLogsUsePrivateFields(t *testing.T) {
	for _, flag := range []string{"", "1"} {
		for _, failure := range []string{"invalid", "missing", "unreadable"} {
			t.Run(failure+"/debug="+flag, func(t *testing.T) {
				app := newMCPReadTestApp(t)
				t.Setenv(backgroundTranscriptDebugEnv, flag)
				output, _ := isolateActivityPrivacyLogs(t)
				const secret = "private-update-target-marker"
				id := secret
				switch failure {
				case "invalid":
					id = "../" + secret
				case "unreadable":
					dir, err := memoryDir()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(dir, id+".md"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				response := &mcMemoryGenerationResponse{Title: "Updated preference", Dimension: "user_insight", MemoryPoints: []string{"Keeps the original preference"}}
				if _, action, err := (&MemoryGenerator{app: app}).updateMemoryFileDetailed(app.ctx, id, memorySourceConversation, "fallback-source", response, ""); err != nil || action != BackgroundMaterializationCreated {
					t.Fatalf("fallback changed: action=%s err=%v", action, err)
				}
				assertActivityLogContentVisibility(t, output.String(), flag == "1", secret)
			})
		}
	}
}
