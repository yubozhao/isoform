package main

import (
	"context"
	"fmt"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"wails-gui/internal/identity"
)

// ContactsAccessStatus is what the Who you know page reads before offering the
// contacts import. Permission is the OS state: granted, denied, restricted,
// not_determined, or not_applicable where the platform has no such permission.
type ContactsAccessStatus struct {
	Supported         bool   `json:"supported"`
	Platform          string `json:"platform"`
	Permission        string `json:"permission"`
	ConsentAccepted   bool   `json:"consentAccepted"`
	ConsentAcceptedAt string `json:"consentAcceptedAt"`
	ImportedCount     int    `json:"importedCount"`
	LastImportAt      string `json:"lastImportAt"`
	SyncState         string `json:"syncState"`
	SyncPaused        bool   `json:"syncPaused"`
	VCardSupported    bool   `json:"vcardSupported"`
}

// SystemContactView is one row in the contacts picker. Imported reports that
// an earlier import linked this contact to a principal in Isoform.
type SystemContactView struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Phones      []string `json:"phones"`
	Emails      []string `json:"emails"`
	Imported    bool     `json:"imported"`
}

// ContactsImportResult counts what one import or removal did. Removed is set
// only by RemoveImportedContacts. Errors names contacts that were skipped in
// part, for example a number that already belongs to someone else.
type ContactsImportResult struct {
	Imported int      `json:"imported"`
	Merged   int      `json:"merged"`
	Skipped  int      `json:"skipped"`
	Removed  int      `json:"removed"`
	Errors   []string `json:"errors"`
}

// GetContactsAccessStatus reports the OS permission, the stored consent and
// how many system contacts Isoform has imported. It never triggers a prompt.
func (a *App) GetContactsAccessStatus() (perfValue ContactsAccessStatus, perfErr error) {
	defer logPageBinding("GetContactsAccessStatus", time.Now(), &perfValue)
	return a.contactsAccessStatus(context.Background())
}

// AcceptContactsConsent records the explicit consent-sheet action before the
// OS prompt or the picker may read any contacts. It does not request access.
func (a *App) AcceptContactsConsent(version int) (ContactsAccessStatus, error) {
	if version != contactsConsentVersion {
		return ContactsAccessStatus{}, errContactsConsentRequired
	}
	path, err := contactsConsentPath()
	if err != nil {
		return ContactsAccessStatus{}, err
	}
	if err := recordContactsConsent(path, "people", time.Now()); err != nil {
		return ContactsAccessStatus{}, err
	}
	if err := a.resumeContactsSync(); err != nil {
		return ContactsAccessStatus{}, err
	}
	return a.contactsAccessStatus(context.Background())
}

// RequestContactsAccess is called only by the People consent sheet. It shares
// the global permission queue so no other OS permission prompt can overlap it.
func (a *App) RequestContactsAccess() (ContactsAccessStatus, error) {
	path, err := contactsConsentPath()
	if err != nil {
		return ContactsAccessStatus{}, err
	}
	if !hasCurrentContactsConsent(path) {
		return ContactsAccessStatus{}, errContactsConsentRequired
	}
	runPermissionRequest(func() { _, err = requestContactsAuthorization() })
	if err != nil {
		return ContactsAccessStatus{}, err
	}
	if err := a.resumeContactsSync(); err != nil {
		return ContactsAccessStatus{}, err
	}
	return a.contactsAccessStatus(context.Background())
}

// ListSystemContacts returns the address book for the picker. It persists
// nothing; the contacts are read only after the user accepts the consent sheet.
func (a *App) ListSystemContacts() ([]SystemContactView, error) {
	im, err := a.newContactsImporter()
	if err != nil {
		return nil, err
	}
	return im.listContacts(context.Background(), systemContactsSource{})
}

func (im contactsImporter) listContacts(ctx context.Context, src contactSource) ([]SystemContactView, error) {
	if !hasCurrentContactsConsent(im.consentPath) {
		return nil, errContactsConsentRequired
	}
	contacts, err := src.contacts(ctx)
	if err != nil {
		return nil, err
	}
	resolver := identity.NewResolver(im.store, false)
	app := contactHandleApp(src.source())
	seen := make(map[string]bool, len(contacts))
	out := make([]SystemContactView, 0, len(contacts))
	for _, c := range contacts {
		id := strings.TrimSpace(c.ID)
		name := contactDisplayName(c)
		if id == "" || name == "" || seen[id] {
			continue
		}
		seen[id] = true
		subject, err := resolver.ResolveSender(ctx, app, id, "")
		if err != nil {
			return nil, fmt.Errorf("look up contact: %w", err)
		}
		out = append(out, SystemContactView{
			ID:          id,
			DisplayName: name,
			Phones:      trimmedNonEmpty(c.Phones),
			Emails:      trimmedNonEmpty(c.Emails),
			Imported:    subject.PrincipalID != "",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName)
	})
	return out, nil
}

// ImportSystemContacts links or creates a principal for each selected contact
// in the macOS address book. consentVersion must match the current consent
// text; the import records that consent.
func (a *App) ImportSystemContacts(ids []string, consentVersion int) (ContactsImportResult, error) {
	// Who you know shows the person's own change at once.
	defer a.touchDerivedViews(derivedPeopleOverview)
	im, err := a.newContactsImporter()
	if err != nil {
		return ContactsImportResult{}, err
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		if t := strings.TrimSpace(id); t != "" {
			selected[t] = true
		}
	}
	return im.importContacts(context.Background(), systemContactsSource{}, selected, consentVersion)
}

// ImportContactsFromVCard imports every card in a .vcf or .vcard file. It is
// the path on Windows and Linux, and works on macOS too.
func (a *App) ImportContactsFromVCard(path string, consentVersion int) (ContactsImportResult, error) {
	// Who you know shows the person's own change at once.
	defer a.touchDerivedViews(derivedPeopleOverview)
	im, err := a.newContactsImporter()
	if err != nil {
		return ContactsImportResult{}, err
	}
	return im.importContacts(context.Background(), vcardFileSource{path: path}, nil, consentVersion)
}

// PickVCardFile opens a file dialog filtered to vCard files. It returns an
// empty string when the user cancels.
func (a *App) PickVCardFile() (string, error) {
	path, err := wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Import contacts from a vCard file",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "vCard (*.vcf, *.vcard)", Pattern: "*.vcf;*.vcard"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("open file dialog: %w", err)
	}
	return path, nil
}

// RemoveImportedContacts deletes only source=system_contacts principals.
// Existing people and vCard imports remain, even when an import linked to them.
func (a *App) RemoveImportedContacts() (ContactsImportResult, error) {
	// Who you know shows the person's own change at once.
	defer a.touchDerivedViews(derivedPeopleOverview)
	store, err := a.requireStore()
	if err != nil {
		return ContactsImportResult{}, err
	}
	// Pause first so a scheduled refresh cannot recreate removed cards.
	if err := store.SetUIState(context.Background(), contactsSyncPausedKey, "true"); err != nil {
		return ContactsImportResult{}, err
	}
	a.CancelContactsSync()
	res, err := removeImportedContacts(context.Background(), store)
	if err == nil && len(res.Errors) == 0 {
		err = store.SetUIState(context.Background(), contactsLastImportKey, "")
	}
	if a.ctx != nil {
		a.emitBrowserEvent("contacts:updated")
	}
	return res, err
}

func (a *App) newContactsImporter() (contactsImporter, error) {
	store, err := a.requireStore()
	if err != nil {
		return contactsImporter{}, err
	}
	path, err := contactsConsentPath()
	if err != nil {
		return contactsImporter{}, fmt.Errorf("locate contacts consent file: %w", err)
	}
	return contactsImporter{store: store, consentPath: path}, nil
}

// contactsAccessStatus assembles the status. A missing store leaves the counts
// at zero so the Who you know page still renders.
func (a *App) contactsAccessStatus(ctx context.Context) (ContactsAccessStatus, error) {
	status := ContactsAccessStatus{
		Supported:      systemContactsSupported(),
		Platform:       goruntime.GOOS,
		Permission:     contactsAuthorization(),
		VCardSupported: true,
	}
	path, err := contactsConsentPath()
	if err != nil {
		return status, fmt.Errorf("locate contacts consent file: %w", err)
	}
	if consent := loadContactsConsent(path); hasCurrentContactsConsent(path) {
		status.ConsentAccepted = true
		status.ConsentAcceptedAt = consent.AcceptedAt
	}
	if a == nil || a.store == nil {
		return status, nil
	}
	count, err := countImportedPrincipals(ctx, a.store)
	if err != nil {
		return status, err
	}
	status.ImportedCount = count
	last, err := a.store.GetUIState(ctx, contactsLastImportKey)
	if err != nil {
		return status, fmt.Errorf("read last import time: %w", err)
	}
	status.LastImportAt = last
	paused, err := a.store.GetUIState(ctx, contactsSyncPausedKey)
	if err != nil {
		return status, err
	}
	status.SyncPaused = paused == "true"
	a.contactsSyncMu.Lock()
	if a.contactsSync != nil {
		status.SyncState = a.contactsSync.status()
	}
	a.contactsSyncMu.Unlock()
	return status, nil
}
