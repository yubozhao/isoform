package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/tasklink"
)

// TestFrontEndToEndWithTheRealBinaries runs the Rust front and link (built
// in ../front) with this computer's TLS server: an HTTPS GET to the front's
// port, with the hostname of the link's label, reaches the task link over
// real iroh (n0 discovery, so it needs the internet). Opt in:
//
//	(cd ../front && cargo build --release)
//	ISOFORM_FRONT_E2E=1 go test -run TestFrontEndToEndWithTheRealBinaries -count=1 .
func TestFrontEndToEndWithTheRealBinaries(t *testing.T) {
	if os.Getenv("ISOFORM_FRONT_E2E") != "1" {
		t.Skip("set ISOFORM_FRONT_E2E=1 with ../front built to run")
	}
	bins, _ := filepath.Abs("../front/target/release")
	frontBin, linkBin := filepath.Join(bins, "isoform-front"), filepath.Join(bins, "isoform-front-link")
	dir := t.TempDir()
	const port = "127.0.0.1:18443"
	front := exec.Command(frontBin)
	front.Env = []string{"ISOFORM_FRONT_LISTEN=" + port, "ISOFORM_FRONT_KEY_FILE=" + filepath.Join(dir, "front.key"), "PATH=/usr/bin:/bin"}
	out, _ := front.StdoutPipe()
	if err := front.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = front.Process.Kill(); _ = front.Wait() })
	sc := bufio.NewScanner(out)
	var frontID string
	for sc.Scan() {
		if id, ok := strings.CutPrefix(sc.Text(), "front endpoint id: "); ok {
			frontID = id
		}
		if strings.HasPrefix(sc.Text(), "listening on") {
			break
		}
	}
	go func() {
		for sc.Scan() {
		}
	}()
	if frontID == "" {
		t.Fatal("the front printed no id")
	}

	t.Setenv(frontLinkBinEnv, linkBin)
	t.Setenv(frontIDsEnv, frontID)
	t.Setenv(taskLinkBaseURLEnv, "")
	t.Setenv(taskLinkAddrEnv, "127.0.0.1:0")
	a := &App{}
	a.front.dir = filepath.Join(dir, "computer")

	a.taskLinks.dir = t.TempDir()
	a.front.certFunc = func(hostname, _ string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		cert, p := selfSignedFor(t, hostname)
		e2eRoots = p
		return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil }
	}
	t.Cleanup(a.stopFront)
	t.Cleanup(a.shutdownTaskLinks)
	if err := a.startFront(); err != nil {
		t.Fatal(err)
	}
	host := a.frontHostname()
	link, err := a.createTaskLink(tasklink.Link{ID: "e2e", AgentID: "claude", Text: "End to end through the front."})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: e2eRoots, ServerName: host},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, port)
		},
	}}
	var body []byte
	var status int
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(link)
		if err == nil {
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			status = resp.StatusCode
			break
		}
		t.Logf("not yet: %v", err)
		time.Sleep(3 * time.Second)
	}
	if status != http.StatusOK || !strings.Contains(string(body), "End to end through the front.") {
		t.Fatalf("GET through the front = %d %q", status, body)
	}
}

var e2eRoots *x509.CertPool
