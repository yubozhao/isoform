package main

// Certificate Transparency watch for the front's hostname
// (docs/design/mcp-public-front.md, "Honest limit"). Isoform controls the
// DNS for agents.isoform.ai, so a compromised front operator could get its
// own certificate for a person's label. Every public certificate is logged
// in CT, so the computer checks the logs once a day and alerts the person on
// any certificate for its hostname whose key it never held.
//
// The lookup sends only the hostname, which is public anyway (it is in the
// CT logs). It uses SSLMate's Cert Spotter API, which needs no account for
// a few lookups a day.

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	frontCTFirstCheck = 10 * time.Minute
	frontCTEvery      = 24 * time.Hour
	frontCTKnownFile  = "known-keys.json"
	frontCTAlertsFile = "unexpected-certificates.json"
	frontCTDefaultAPI = "https://api.certspotter.com/v1/issuances"
	frontCTMaxBody    = 4 << 20
	frontCTClickData  = "settings:front"
)

// frontCTCopy is the notification, per UI language.
var frontCTCopy = map[string]map[string]string{
	"en":    {"title": "Isoform", "body": "A certificate for your agent address %s was issued with a key this computer never held. Agents may not be reaching you safely."},
	"zh-CN": {"title": "Isoform", "body": "你的代理地址 %s 被签发了一张证书，其密钥不在这台电脑上。代理可能无法安全地连接到你。"},
}

// frontCTIssuance is the part of a Cert Spotter issuance the watch reads.
type frontCTIssuance struct {
	ID           string   `json:"id"`
	PubkeySHA256 string   `json:"pubkey_sha256"`
	DNSNames     []string `json:"dns_names"`
	NotBefore    string   `json:"not_before"`
	Issuer       struct {
		FriendlyName string `json:"friendly_name"`
	} `json:"issuer"`
}

// FrontUnexpectedCertificate is one alert, shown in Settings.
type FrontUnexpectedCertificate struct {
	ID        string `json:"id"`
	Issuer    string `json:"issuer"`
	NotBefore string `json:"notBefore"`
	FoundAt   int64  `json:"foundAt"`
}

type frontCTWatch struct {
	hostname string
	dir      string // the front folder
	api      string
	client   *http.Client
	notify   func(title, body, clickData string)
	language string
	now      func() time.Time
}

func (w *frontCTWatch) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// ownKeys reads the keys of the certificates autocert cached for the
// hostname, adds them to the known list, and returns the whole list. A key
// the computer once held stays known after renewal replaces it.
func (w *frontCTWatch) ownKeys() (map[string]bool, error) {
	known := map[string]bool{}
	path := filepath.Join(w.dir, frontCTKnownFile)
	if data, err := os.ReadFile(path); err == nil {
		var list []string
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, err
		}
		for _, k := range list {
			known[k] = true
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	before := len(known)
	for _, name := range []string{w.hostname, w.hostname + "+rsa"} {
		data, err := os.ReadFile(filepath.Join(w.dir, "acme", name))
		if err != nil {
			continue
		}
		for rest := data; ; {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				continue
			}
			// Only the leaf names the hostname; the chain's keys aren't ours.
			if cert.VerifyHostname(w.hostname) != nil {
				continue
			}
			sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
			known[hex.EncodeToString(sum[:])] = true
		}
	}
	if len(known) != before {
		list := make([]string, 0, len(known))
		for k := range known {
			list = append(list, k)
		}
		if err := writeFrontJSON(path, list); err != nil {
			return nil, err
		}
	}
	return known, nil
}

func (w *frontCTWatch) lookup(ctx context.Context) ([]frontCTIssuance, error) {
	api := w.api
	if api == "" {
		api = frontCTDefaultAPI
	}
	q := url.Values{"domain": {w.hostname}}
	q.Add("expand", "dns_names")
	q.Add("expand", "issuer")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Isoform")
	req.Header.Set("Accept", "application/json")
	client := w.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CT lookup: %s", resp.Status)
	}
	var out []frontCTIssuance
	if err := json.NewDecoder(io.LimitReader(resp.Body, frontCTMaxBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("CT lookup: %w", err)
	}
	return out, nil
}

// check runs one lookup and returns the new unexpected certificates.
func (w *frontCTWatch) check(ctx context.Context) ([]FrontUnexpectedCertificate, error) {
	known, err := w.ownKeys()
	if err != nil {
		return nil, err
	}
	issuances, err := w.lookup(ctx)
	if err != nil {
		return nil, err
	}
	alerts, err := readFrontCTAlerts(w.dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, a := range alerts {
		seen[a.ID] = true
	}
	var fresh []FrontUnexpectedCertificate
	for _, is := range issuances {
		names := false
		for _, n := range is.DNSNames {
			if strings.EqualFold(strings.TrimSuffix(n, "."), w.hostname) {
				names = true
			}
		}
		if !names || is.ID == "" || known[strings.ToLower(is.PubkeySHA256)] || seen[is.ID] {
			continue
		}
		alert := FrontUnexpectedCertificate{ID: is.ID, Issuer: is.Issuer.FriendlyName, NotBefore: is.NotBefore, FoundAt: w.clock().UnixMilli()}
		fresh = append(fresh, alert)
		seen[is.ID] = true
	}
	if len(fresh) == 0 {
		return nil, nil
	}
	if err := writeFrontJSON(filepath.Join(w.dir, frontCTAlertsFile), append(alerts, fresh...)); err != nil {
		return nil, err
	}
	if w.notify != nil {
		copy := frontCTCopy[w.language]
		if copy == nil {
			copy = frontCTCopy["en"]
		}
		w.notify(copy["title"], fmt.Sprintf(copy["body"], w.hostname), frontCTClickData)
	}
	return fresh, nil
}

// runFrontCTWatch checks soon after start, then daily, until ctx ends.
func (a *App) runFrontCTWatch(ctx context.Context, w *frontCTWatch) {
	wait := frontCTFirstCheck
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = frontCTEvery
		fresh, err := w.check(ctx)
		if err != nil {
			log.Printf("[front] CT check: %v", err)
			continue
		}
		if len(fresh) > 0 {
			log.Printf("[front] %d certificate(s) for this computer's hostname with a key it never held", len(fresh))
		}
	}
}

func readFrontCTAlerts(dir string) ([]FrontUnexpectedCertificate, error) {
	var alerts []FrontUnexpectedCertificate
	data, err := os.ReadFile(filepath.Join(dir, frontCTAlertsFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return alerts, json.Unmarshal(data, &alerts)
}

func writeFrontJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
