package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wails-gui/internal/pii"
)

func TestMobileBriefFeedRechecksStoredSourcesAndCachedAudioURLs(t *testing.T) {
	for _, reason := range []string{"sensitive app", "historical pause", "closed eyes", "missing source", "privacy lookup failure", "missing brief", "missing app"} {
		t.Run(reason, func(t *testing.T) {
			app, svc, stored := newStoredMobileBriefPrivacyFixture(t)
			wav := testWAV(1)
			stored.Audio.Mime = mobileBriefAudioMimeWAV
			stored.Audio.Bytes = len(wav)
			stored.Audio.binding = mobileBriefAudioBinding(app.mobileBriefForDelivery(stored), "en")
			audioPath := filepath.Join(svc.store.dir, stored.BriefID+mobileBriefAudioExtWAV)
			if err := os.WriteFile(audioPath, wav, 0600); err != nil {
				t.Fatal(err)
			}
			if err := svc.store.save(stored, svc.now()); err != nil {
				t.Fatal(err)
			}

			// Exercise the production feed callback and HTTP handler against
			// real stored documents and SQLite, without taking the fixed port.
			secret, err := svc.feed.loadSecret()
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			svc.feed.listener = listener
			base := "/feed/" + secret
			request := func(method, path string) *httptest.ResponseRecorder {
				t.Helper()
				response := httptest.NewRecorder()
				svc.feed.serve(response, httptest.NewRequest(method, base+path, nil))
				return response
			}
			enclosure := "/audio/" + stored.BriefID + mobileBriefAudioExtWAV
			before := request(http.MethodGet, "/brief.xml")
			if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), "Protected narrative text") || !strings.Contains(before.Body.String(), enclosure) {
				t.Fatalf("allowed episode missing from feed: %d %s", before.Code, before.Body.String())
			}
			before = request(http.MethodGet, enclosure)
			if before.Code != http.StatusOK || !bytes.Equal(before.Body.Bytes(), wav) {
				t.Fatalf("allowed audio was not served: %d", before.Code)
			}

			switch reason {
			case "sensitive app":
				pii.SetUserSensitiveApps(map[string]string{"com.example.test": "TestApp"})
			case "historical pause":
				app.saveObservationPauses([]observationPause{{Start: 100_000, End: 160_000}})
			case "closed eyes":
				app.closedEyes.enabled.Store(true)
			case "missing source":
				if err := app.activity.store.DeleteSession("brief-delivery-source"); err != nil {
					t.Fatal(err)
				}
			case "privacy lookup failure":
				if err := app.activity.store.Close(); err != nil {
					t.Fatal(err)
				}
			case "missing brief":
				if err := os.Remove(filepath.Join(svc.store.dir, stored.BriefID+".json")); err != nil {
					t.Fatal(err)
				}
			case "missing app":
				svc.app = nil
			}
			after := request(http.MethodGet, "/brief.xml")
			if after.Code != http.StatusOK || strings.Contains(after.Body.String(), "<item>") || strings.Contains(after.Body.String(), "Protected") || strings.Contains(after.Body.String(), enclosure) {
				t.Fatalf("withdrawn episode remains in feed: %d %s", after.Code, after.Body.String())
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				audio := request(method, enclosure)
				if audio.Code != http.StatusNotFound || bytes.Contains(audio.Body.Bytes(), wav) {
					t.Fatalf("cached enclosure %s returned held audio: %d", method, audio.Code)
				}
				if audio.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("feed responses must not be cached")
				}
			}
			data, err := os.ReadFile(audioPath)
			if err != nil || !bytes.Equal(data, wav) {
				t.Fatal("the delivery read changed the stored recording")
			}
		})
	}
}
