package models

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Isoform's mirror answers 403 to every request (2026-11-15), so downloads
// default to the first upstream source. A catalog entry whose default is the
// mirror could not be downloaded by a new install.
func TestCatalogDefaultsToAnUpstreamSource(t *testing.T) {
	for _, e := range Catalog() {
		if len(e.Files) == 0 {
			continue
		}
		if s := defaultSource(e); s.Kind == "mirror" || strings.HasPrefix(s.URL, "https://model-assets.yansu.app/") {
			t.Errorf("%s downloads from %s by default", e.ID, s.URL)
		}
	}
}

// The daily trial runs this against the real hosts (ISOFORM_MODEL_SOURCES_CHECK=1):
// every file of every downloadable model answers its default source, so a
// source that stops answering is caught the next morning, not by a new user.
// It reads one byte of each file.
func TestCatalogDefaultSourcesAnswer(t *testing.T) {
	if os.Getenv("ISOFORM_MODEL_SOURCES_CHECK") != "1" {
		t.Skip("ISOFORM_MODEL_SOURCES_CHECK not set (reaches the real download hosts)")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	for _, e := range Catalog() {
		if len(e.Files) == 0 || e.SizeBytes == nil {
			continue
		}
		source := defaultSource(e)
		for _, f := range e.Files {
			remote := fileURL(e, f, source)
			req, err := http.NewRequest(http.MethodGet, remote, nil)
			if err != nil {
				t.Errorf("%s: %v", remote, err)
				continue
			}
			req.Header.Set("Range", "bytes=0-0")
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("%s %s: %v", e.ID, remote, err)
				continue
			}
			_ = resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK, http.StatusPartialContent:
			default:
				t.Errorf("%s %s: %d", e.ID, remote, resp.StatusCode)
			}
		}
	}
}
