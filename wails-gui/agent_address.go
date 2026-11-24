package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"
)

const frontTermsAcceptedKey = "front_terms_first_accepted_at"

type AgentAddressView struct {
	Open                   bool                         `json:"open"`
	Hostname               string                       `json:"hostname"`
	Service                string                       `json:"service"`
	Apps                   []string                     `json:"apps"`
	UnexpectedCertificates []FrontUnexpectedCertificate `json:"unexpectedCertificates"`
}

// StartAgentJoinForApp is the connect sheet's explicit action. acceptedTerms
// records the person's first acceptance before asking the CA for a certificate.
func (a *App) StartAgentJoinForApp(app string, acceptedTerms bool) (AgentJoinStart, error) {
	switch app {
	case "claude", "chatgpt", "gemini", "grok":
	default:
		return AgentJoinStart{}, errors.New("this app cannot connect through the agent address")
	}
	a.front.lifecycle.Lock()
	defer a.front.lifecycle.Unlock()
	if err := a.openAgentAddress(acceptedTerms); err != nil {
		return AgentJoinStart{}, err
	}
	base, err := a.taskLinkBaseURL()
	if err == nil && !strings.HasPrefix(base, "https://") {
		err = errAgentJoinNoOrigin
	}
	if err != nil {
		a.closeUnusedAddressLocked()
		return AgentJoinStart{}, err
	}
	start, err := a.startJoinAt(base, true)
	if err != nil {
		a.closeUnusedAddressLocked()
	}
	return start, err
}

func configuredAgentBridge() bool {
	return strings.TrimSpace(os.Getenv(taskLinkBaseURLEnv)) != ""
}

// Called under lifecycle. A configured public bridge forwards to the existing
// loopback service and owns its certificate and external endpoint.
func (a *App) openAgentAddress(acceptedTerms bool) error {
	if a.front.shutdown {
		return errors.New("the computer is shutting down")
	}
	if configuredAgentBridge() {
		if _, err := a.taskLinkBaseURL(); err != nil {
			return err
		}
		return a.listenForTaskLinks(true)
	}
	if a.store == nil {
		return errors.New("storage is not ready")
	}
	ctx := context.Background()
	value, err := a.store.GetUIState(ctx, frontTermsAcceptedKey)
	if err != nil {
		return err
	}
	if value == "" {
		if !acceptedTerms {
			return errors.New("accept Let's Encrypt's subscriber agreement to open the agent address")
		}
		if err := a.store.SetUIStateIfAbsent(ctx, frontTermsAcceptedKey, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return a.startFront()
}

func (a *App) closeUnusedAddress() {
	a.front.lifecycle.Lock()
	defer a.front.lifecycle.Unlock()
	a.closeUnusedAddressLocked()
}

func (a *App) closeUnusedAddressLocked() {
	if !a.agentJoinService().NeedsAddress() {
		if a.front.restart != nil {
			a.front.restart.Stop()
		}
		a.stopFront()
		a.closeUnusedBridge()
	}
}

func (a *App) restoreAgentAddress() {
	a.front.lifecycle.Lock()
	defer a.front.lifecycle.Unlock()
	if a.store != nil {
		if err := a.store.DeleteUIState(context.Background(), frontEnabledKey); err != nil {
			log.Printf("[front] old setting not removed: %v", err)
		}
	}
	if !a.agentJoinService().NeedsAddress() {
		a.stopFront()
		return
	}
	if err := a.openAgentAddress(false); err != nil {
		log.Printf("[front] address not restored: %v", err)
	}
}

func (a *App) shutdownAgentAddress() {
	a.front.lifecycle.Lock()
	defer a.front.lifecycle.Unlock()
	a.front.shutdown = true
	if a.front.restart != nil {
		a.front.restart.Stop()
	}
	if a.front.expiry != nil {
		a.front.expiry.Stop()
	}
	a.stopFront()
}

// agentJoinedViaAddress says whether a connected agent reaches Isoform
// through the agent address.
func (a *App) agentJoinedViaAddress(id string) bool {
	for _, ag := range a.agentJoinService().Agents() {
		if ag.ID == id {
			return ag.ViaAddress
		}
	}
	return false
}

// CloseAgentAddress is What leaves' Stop for the agent address: it ends a
// waiting join made for the address and disconnects every agent that reaches
// Isoform through it, and the address closes with the last one. Agents on
// this computer stay connected.
func (a *App) CloseAgentAddress() error {
	svc := a.agentJoinService()
	svc.CancelAddressJoin()
	var errs []error
	for _, ag := range svc.Agents() {
		if !ag.ViaAddress {
			continue
		}
		if err := a.DisconnectAgent(ag.ID); err != nil {
			errs = append(errs, err)
		}
	}
	a.closeUnusedAddress()
	return errors.Join(errs...)
}

func (a *App) GetAgentAddress() AgentAddressView {
	host := a.frontHostname()
	v := AgentAddressView{Open: host != "", Hostname: host, Service: "Isoform's front", Apps: []string{}, UnexpectedCertificates: []FrontUnexpectedCertificate{}}
	svc := a.agentJoinService()
	for _, ag := range svc.Agents() {
		if ag.ViaAddress {
			v.Apps = append(v.Apps, ag.Name)
		}
	}
	if configuredAgentBridge() {
		if base, err := a.taskLinkBaseURL(); err == nil {
			v.Service = strings.TrimPrefix(base, "https://")
			a.taskLinks.listenMu.Lock()
			listening := a.taskLinks.server != nil
			a.taskLinks.listenMu.Unlock()
			v.Open = listening && svc.NeedsAddress()
			if v.Open {
				v.Hostname = v.Service
			}
		}
	}
	if dir, err := a.front.dirPath(); err == nil {
		if alerts, err := readFrontCTAlerts(dir); err == nil && len(alerts) > 0 {
			v.UnexpectedCertificates = alerts
		}
	}
	return v
}

// Compatibility with the Settings page until the connect sheet owns the address.
func (a *App) GetFrontStatus() map[string]any {
	v := a.GetAgentAddress()
	return map[string]any{"enabled": v.Open, "running": v.Open, "hostname": v.Hostname, "unexpectedCertificates": v.UnexpectedCertificates}
}

func (a *App) SetFrontEnabled(bool) error {
	return errors.New("the agent address opens when you connect an agent in its own app")
}
