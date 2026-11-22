package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme"

	"wails-gui/internal/tasklink"
)

const (
	testFrontLabel = "ajtezl5ck4yovehh26r7b2lxck63lavp2wnr4skhtlisflvcxy2q"
	testFrontID    = "5aa6fe739da0e805c969d0dfa3cc667644c1a3a8b622b2d9891bcabeb61f3335"
	testLinkID     = "02664cafa25730ea90e7d7a3f0e97712bdb582afd59b1e49479ad122aea2be35"
)

func TestParseFrontLinkLine(t *testing.T) {
	good := `{"endpointId":"` + testLinkID + `","label":"` + testFrontLabel + `","hostname":"` + testFrontLabel + `.agents.isoform.ai"}`
	info, err := parseFrontLinkLine(good, "agents.isoform.ai")
	if err != nil || info.Hostname != testFrontLabel+".agents.isoform.ai" {
		t.Fatalf("good line: %+v %v", info, err)
	}
	for _, bad := range []string{
		"not json",
		strings.Replace(good, ".agents.isoform.ai", ".evil.example", 1),
		strings.Replace(good, testFrontLabel+`"`, testFrontLabel[:51]+`"`, 1),
		strings.Replace(good, testLinkID, "xyz", 1),
	} {
		if _, err := parseFrontLinkLine(bad, "agents.isoform.ai"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestFrontAllowedIDsMustBeEndpointIDs(t *testing.T) {
	t.Setenv(frontIDsEnv, "")
	if ids, err := frontAllowedIDs(); err != nil || len(ids) != 1 || ids[0] != frontDefaultIDs[0] {
		t.Fatalf("default: %v %v", ids, err)
	}
	if !frontIDRE.MatchString(frontDefaultIDs[0]) {
		t.Fatal("the built-in front id is not an endpoint id")
	}
	defer func(saved []string) { frontDefaultIDs = saved }(frontDefaultIDs)
	frontDefaultIDs = nil
	if _, err := frontAllowedIDs(); !errors.Is(err, errFrontNoIDs) {
		t.Fatalf("empty: %v", err)
	}
	t.Setenv(frontIDsEnv, testFrontID+", nope")
	if _, err := frontAllowedIDs(); err == nil {
		t.Fatal("a bad id was accepted")
	}
	t.Setenv(frontIDsEnv, " "+strings.ToUpper(testFrontID)+" ")
	if ids, err := frontAllowedIDs(); err != nil || len(ids) != 1 || ids[0] != testFrontID {
		t.Fatalf("ids = %v %v", ids, err)
	}
}

func TestFrontCertSourceNeverLoopsOnFailure(t *testing.T) {
	var calls int
	now := time.Unix(1_000_000, 0)
	src := &frontCertSource{
		get: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			calls++
			return nil, errors.New("rate limited")
		},
		now: func() time.Time { return now },
	}
	hello := &tls.ClientHelloInfo{ServerName: "x.agents.isoform.ai", SupportedProtos: []string{"h2"}}
	for i := 0; i < 20; i++ {
		if _, err := src.GetCertificate(hello); err == nil {
			t.Fatal("a failure reported success")
		}
	}
	if calls != 1 {
		t.Fatalf("the CA was asked %d times within the pause, want 1", calls)
	}
	// The challenge handshake is still answered during the pause.
	if _, err := src.GetCertificate(&tls.ClientHelloInfo{SupportedProtos: []string{acme.ALPNProto}}); err == nil || calls != 2 {
		t.Fatalf("challenge: calls=%d err=%v", calls, err)
	}
	now = now.Add(frontCertBackoff + time.Second)
	_, _ = src.GetCertificate(hello)
	if calls != 3 {
		t.Fatalf("after the pause calls=%d, want 3", calls)
	}
}

func selfSignedFor(t *testing.T, host string) (*tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// fakeFrontLink writes a script that records its arguments, prints the
// ready line and waits, the way isoform-front-link does.
func fakeFrontLink(t *testing.T) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "isoform-front-link")
	line := `{"endpointId":"` + testLinkID + `","label":"` + testFrontLabel + `","hostname":"` + testFrontLabel + `.agents.isoform.ai"}`
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\necho '" + line + "'\nexec sleep 600\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func TestFrontServesTaskLinksOverTLSForItsHostnameOnly(t *testing.T) {
	bin, argsFile := fakeFrontLink(t)
	t.Setenv(frontLinkBinEnv, bin)
	t.Setenv(frontIDsEnv, testFrontID)
	t.Setenv(frontSuffixEnv, "")
	t.Setenv(taskLinkBaseURLEnv, "")
	t.Setenv(taskLinkAddrEnv, "127.0.0.1:0")
	host := testFrontLabel + ".agents.isoform.ai"
	cert, pool := selfSignedFor(t, host)
	var certCalls atomic.Int32
	on := &atomic.Bool{}
	on.Store(true)
	a := &App{}
	a.front.dir = t.TempDir()
	a.front.enabled = on.Load
	a.front.certFunc = func(hostname, _ string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hostname != host {
			t.Errorf("certificate asked for %q", hostname)
		}
		return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { certCalls.Add(1); return cert, nil }
	}
	a.taskLinks.dir = t.TempDir()
	t.Cleanup(a.stopFront)
	t.Cleanup(a.shutdownTaskLinks)

	if err := a.startFront(); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--allow " + testFrontID, "--forward 127.0.0.1:", "--key-file " + filepath.Join(a.front.dir, "link.key")} {
		if !strings.Contains(string(args), want) {
			t.Errorf("link args %q lack %q", args, want)
		}
	}
	if got := a.frontHostname(); got != host {
		t.Fatalf("hostname = %q", got)
	}
	link, err := a.createTaskLink(tasklink.Link{ID: "task-1", AgentID: "claude", Text: "Front test task."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "https://"+host+tasklink.PathPrefix) {
		t.Fatalf("link = %q, want the front hostname", link)
	}

	// Dial the loopback TLS listener as the link would forward an agent.
	a.front.mu.Lock()
	addr := a.front.listener.Addr().String()
	a.front.mu.Unlock()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: host},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}, Timeout: 10 * time.Second}
	resp, err := client.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Front test task.") {
		t.Fatalf("GET = %d %q", resp.StatusCode, body)
	}
	if certCalls.Load() == 0 {
		t.Fatal("TLS did not use the computer's certificate")
	}
	// Another Host header on the same TLS connection gets nothing.
	req, _ := http.NewRequest(http.MethodGet, link, nil)
	req.Host = "other.example"
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other host = %d, want 404", resp.StatusCode)
	}

	a.stopFront()
	if a.frontHostname() != "" {
		t.Fatal("hostname kept after stop")
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("the TLS listener still answers after stop")
	}
}

func TestFrontStaysOffWithoutTheSwitchOrIDs(t *testing.T) {
	a := &App{}
	a.front.dir = t.TempDir()
	a.front.enabled = func() bool { return false }
	if err := a.startFront(); !errors.Is(err, errFrontOff) {
		t.Fatalf("switch off: %v", err)
	}
	a.front.enabled = func() bool { return true }
	t.Setenv(frontIDsEnv, "")
	defer func(saved []string) { frontDefaultIDs = saved }(frontDefaultIDs)
	frontDefaultIDs = nil
	if err := a.startFront(); !errors.Is(err, errFrontNoIDs) {
		t.Fatalf("no ids: %v", err)
	}
}

// TestFrontUsesLetsEncryptProductionUnlessStagingIsAsked: production by
// default, staging only on purpose, each with its own certificate cache.
func TestFrontUsesLetsEncryptProductionUnlessStagingIsAsked(t *testing.T) {
	for value, want := range map[string][2]string{
		"":                                  {frontACMEProduction, frontACMECacheProduction},
		"production":                        {frontACMEProduction, frontACMECacheProduction},
		"Staging":                           {frontACMEStaging, frontACMECacheStaging},
		frontACMEStaging:                    {frontACMEStaging, frontACMECacheStaging},
		frontACMEProduction:                 {frontACMEProduction, frontACMECacheProduction},
		"https://ca.example.test/directory": {"https://ca.example.test/directory", frontACMECacheCustom},
	} {
		t.Setenv(frontACMEDirectoryEnv, value)
		if dir, cache := frontACMEDirectory(); dir != want[0] || cache != want[1] {
			t.Errorf("%q = %s %s, want %v", value, dir, cache, want)
		}
	}
}
