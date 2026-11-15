package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontCTWatchAlertsOnlyOnKeysTheComputerNeverHeld(t *testing.T) {
	host := testFrontLabel + ".agents.isoform.ai"
	dir := t.TempDir()
	cert, _ := selfSignedFor(t, host)
	// autocert caches the key and chain in acme/<hostname>.
	if err := os.MkdirAll(filepath.Join(dir, "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	if err := os.WriteFile(filepath.Join(dir, "acme", host), pemData, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.Leaf.RawSubjectPublicKeyInfo)
	ours := hex.EncodeToString(sum[:])

	issuances := []map[string]any{
		{"id": "1", "pubkey_sha256": ours, "dns_names": []string{host}, "issuer": map[string]string{"friendly_name": "Let's Encrypt"}},
		{"id": "2", "pubkey_sha256": "ab", "dns_names": []string{host}, "not_before": "2026-11-14T00:00:00Z", "issuer": map[string]string{"friendly_name": "Some CA"}},
		{"id": "3", "pubkey_sha256": "cd", "dns_names": []string{"other.agents.isoform.ai"}},
	}
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(issuances)
	}))
	defer srv.Close()
	var notified []string
	w := &frontCTWatch{hostname: host, dir: dir, api: srv.URL, notify: func(_, body, _ string) { notified = append(notified, body) }}

	fresh, err := w.check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].ID != "2" || fresh[0].Issuer != "Some CA" {
		t.Fatalf("fresh = %+v, want only issuance 2", fresh)
	}
	if len(notified) != 1 {
		t.Fatalf("notified %d times", len(notified))
	}
	if want := "domain=" + host; !strings.Contains(gotQuery, want) {
		t.Fatalf("query %q lacks %q", gotQuery, want)
	}
	// The same certificate never alerts twice.
	if fresh, _ := w.check(context.Background()); len(fresh) != 0 || len(notified) != 1 {
		t.Fatalf("second check fresh=%v notified=%d", fresh, len(notified))
	}
	// A key the computer once held stays known after its cache file goes.
	_ = os.Remove(filepath.Join(dir, "acme", host))
	issuances = append(issuances, map[string]any{"id": "4", "pubkey_sha256": ours, "dns_names": []string{host}})
	if fresh, _ := w.check(context.Background()); len(fresh) != 0 {
		t.Fatalf("an old own key alerted: %+v", fresh)
	}
	alerts, _ := readFrontCTAlerts(dir)
	if len(alerts) != 1 {
		t.Fatalf("alerts = %+v", alerts)
	}
}

// TestFrontCTWatchReadsTheRealAPI queries Cert Spotter for a public name
// and checks the answer parses. Opt in: ISOFORM_FRONT_CT_REAL=1.
func TestFrontCTWatchReadsTheRealAPI(t *testing.T) {
	if os.Getenv("ISOFORM_FRONT_CT_REAL") != "1" {
		t.Skip("set ISOFORM_FRONT_CT_REAL=1 to query the real CT API")
	}
	w := &frontCTWatch{hostname: "letsencrypt.org"}
	got, err := w.lookup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID == "" || len(got[0].PubkeySHA256) != 64 || len(got[0].DNSNames) == 0 {
		t.Fatalf("unexpected answer: %+v", got)
	}
}
