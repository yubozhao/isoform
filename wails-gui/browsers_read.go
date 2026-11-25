package main

import (
	"errors"
	"os"

	"wails-gui/internal/platform/browserhistory"
)

// What Isoform sees › Screen names the browsers whose history is read on
// this computer (computer-conformance answers v1.5.0 T16): "What's on screen
// and your browsing history in {browsers}, …". The list is the reader's own:
// a browser is read when one of its profiles keeps a History database
// (browserhistory.Profiles) and the user doesn't protect it
// (browserVisitSensitive), in browserhistory.Browsers' order. Safari is
// never read.

// browsersReadNames lists the browsers read under home, each once.
func browsersReadNames(home string, sensitive func(bundleID, name string) bool) []string {
	names := []string{}
	seen := map[string]bool{}
	for _, profile := range browserhistory.Profiles(home) {
		browser := profile.Browser
		if seen[browser.Name] || (sensitive != nil && sensitive(browser.BundleID, browser.Name)) {
			continue
		}
		seen[browser.Name] = true
		names = append(names, browser.Name)
	}
	return names
}

// GetBrowsersRead returns the names of the browsers whose history Isoform
// reads on this computer, empty when none. A failed read is an error, never
// an empty list.
func (a *App) GetBrowsersRead() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, errors.New("couldn't find the home folder")
	}
	return browsersReadNames(home, browserVisitSensitive), nil
}
