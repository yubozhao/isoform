package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/quotedprintable"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/identity"
	"wails-gui/storage"
)

const (
	// contactsConsentVersion is the version of the consent text the Who you know page
	// shows before any contact is read. Bump it when that text changes.
	contactsConsentVersion = 3

	// contactsSourceSystem and contactsSourceVCard are the provenance values
	// recorded on principals an import creates.
	contactsSourceSystem = "system_contacts"
	contactsSourceVCard  = "vcard"

	contactsConsentFileName = "contacts-consent.json"
	contactsLastImportKey   = "contacts_last_import_at"

	// maxVCardBytes bounds one vCard import. Thousands of cards fit well below it.
	maxVCardBytes = 10 << 20
)

var (
	errContactsConsentRequired = errors.New("contacts import needs consent to the current terms")
	errContactsNotAuthorized   = errors.New("contacts access is not granted")
	errContactsReadFailed      = errors.New("could not read the system contacts")
	errContactsUnsupported     = errors.New("system contacts are not available on this platform; import a vCard file instead")
	errVCardExtension          = errors.New("choose a .vcf or .vcard file")
	// Imports resolve, create and bind in several steps. Serializing import
	// and removal prevents overlapping picker submissions creating duplicates.
	contactsImportMu  sync.Mutex
	contactsConsentMu sync.Mutex
)

// systemContact is one contact read from the OS address book or a vCard file.
// It carries only what matching and naming need.
type systemContact struct {
	ID           string   `json:"id"`
	GivenName    string   `json:"givenName"`
	FamilyName   string   `json:"familyName"`
	FullName     string   `json:"fullName"`
	Organization string   `json:"organization"`
	Phones       []string `json:"phones"`
	Emails       []string `json:"emails"`
	Photo        string   `json:"photo"`
}

// contactSource yields the contacts one import reads. The production sources
// are the macOS address book and a vCard file; tests supply a fake.
type contactSource interface {
	// source is the created_from value recorded on principals this import creates.
	source() string
	contacts(ctx context.Context) ([]systemContact, error)
}

// systemContactsSource reads the macOS address book. It persists nothing.
type systemContactsSource struct{}

func (systemContactsSource) source() string { return contactsSourceSystem }

func (systemContactsSource) contacts(ctx context.Context) ([]systemContact, error) {
	return enumerateSystemContacts(ctx)
}

// vcardFileSource reads every card in one .vcf file.
type vcardFileSource struct {
	path string
}

func (s vcardFileSource) source() string { return contactsSourceVCard }

func (s vcardFileSource) contacts(ctx context.Context) ([]systemContact, error) {
	if err := checkVCardPath(s.path); err != nil {
		return nil, err
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return nil, fmt.Errorf("read vCard file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the vCard path is not a regular file")
	}
	if info.Size() > maxVCardBytes {
		return nil, fmt.Errorf("the vCard file is larger than %d MB", maxVCardBytes>>20)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read vCard file: %w", err)
	}
	return parseVCards(data), nil
}

func checkVCardPath(path string) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".vcf", ".vcard":
		return nil
	default:
		return errVCardExtension
	}
}

// contactsConsent is the stored acceptance of the contacts consent text, kept
// in the active flavor's data directory with 0600 permissions.
type contactsConsent struct {
	Version    int    `json:"version"`
	AcceptedAt string `json:"acceptedAt"`
	Source     string `json:"source"`
}

func contactsConsentPath() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, contactsConsentFileName), nil
}

// loadContactsConsent returns the stored consent, or the zero value when none
// is on file. An unreadable file counts as no consent, so the sheet shows again.
func loadContactsConsent(path string) contactsConsent {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[contacts] read consent file: %v", err)
		}
		return contactsConsent{}
	}
	var consent contactsConsent
	if err := json.Unmarshal(raw, &consent); err != nil {
		log.Printf("[contacts] ignoring unreadable consent file: %v", err)
		return contactsConsent{}
	}
	return consent
}

func hasCurrentContactsConsent(path string) bool {
	consent := loadContactsConsent(path)
	if consent.Version != contactsConsentVersion {
		return false
	}
	_, err := time.Parse(time.RFC3339, consent.AcceptedAt)
	return err == nil
}

// recordContactsConsent stores acceptance of the current consent version. The
// first acceptance keeps its time, so a later import does not rewrite it.
func recordContactsConsent(path, source string, now time.Time) error {
	contactsConsentMu.Lock()
	defer contactsConsentMu.Unlock()
	if hasCurrentContactsConsent(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create consent directory: %w", err)
	}
	return writeMobilePrivateJSON(path, contactsConsent{
		Version:    contactsConsentVersion,
		AcceptedAt: now.UTC().Format(time.RFC3339),
		Source:     source,
	})
}

// contactsImporter applies one import to the principals store.
type contactsImporter struct {
	store       *storage.Store
	consentPath string
	incremental bool
}

// importContacts reads the source and links or creates a principal for each
// selected contact. selected == nil means every contact. The consent version
// must match the current text; the source is read only after that check.
func (im contactsImporter) importContacts(ctx context.Context, src contactSource, selected map[string]bool, consentVersion int) (ContactsImportResult, error) {
	contactsImportMu.Lock()
	defer contactsImportMu.Unlock()
	res := ContactsImportResult{Errors: []string{}}
	if im.incremental {
		// Recheck inside the import/removal lock. A removal can pause the
		// worker after its first gate and before it acquires this lock.
		paused, err := im.store.GetUIState(ctx, contactsSyncPausedKey)
		if err != nil {
			return res, err
		}
		if paused == "true" {
			return res, context.Canceled
		}
		if !hasCurrentContactsConsent(im.consentPath) {
			return res, errContactsConsentRequired
		}
	}
	if consentVersion != contactsConsentVersion {
		return res, errContactsConsentRequired
	}
	if selected != nil && len(selected) == 0 {
		return res, nil
	}
	if err := recordContactsConsent(im.consentPath, src.source(), time.Now()); err != nil {
		return res, fmt.Errorf("record contacts consent: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	contacts, err := src.contacts(ctx)
	if err != nil {
		return res, err
	}
	previous := map[string]string{}
	legacy := map[string]bool{}
	if src.source() == contactsSourceSystem {
		cards, err := im.store.ListImportedContactCards(ctx)
		if err != nil {
			return res, err
		}
		for _, c := range cards {
			legacy[c.ExternalRef] = c.Legacy
			if im.incremental {
				previous[c.ExternalRef] = c.Payload
			}
		}
		for _, c := range contacts {
			id := strings.TrimSpace(c.ID)
			if !legacy[id] || (selected != nil && !selected[id]) {
				continue
			}
			if err := im.store.AdoptLegacyContactHandles(ctx, id,
				uniqueNonEmpty(c.Phones, normalizeContactPhone), uniqueNonEmpty(c.Emails, normalizeContactEmail)); err != nil {
				return res, err
			}
		}
	}
	seen := make(map[string]bool, len(contacts))
	for _, c := range contacts {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		id := strings.TrimSpace(c.ID)
		if selected != nil && !selected[id] {
			continue
		}
		if id == "" || seen[id] {
			res.Skipped++
			continue
		}
		// A notes-only/unsupported-fields card withdraws the previous display
		// payload and bindings just like deletion. Leave it out of the live set.
		if contactDisplayName(c) == "" {
			res.Skipped++
			continue
		}
		seen[id] = true
		payload, _ := json.Marshal(c)
		if previous[id] == string(payload) {
			owner, err := im.store.ResolveByAppHandle(ctx, "contacts", id)
			if err != nil {
				return res, err
			}
			if owner != nil {
				res.Skipped++
				continue
			}
		}
		im.importContact(ctx, src.source(), c, &res)
	}

	if err := ctx.Err(); err != nil {
		return res, err
	}
	if im.incremental && src.source() == contactsSourceSystem && selected == nil {
		cards, err := im.store.ListImportedContactCards(ctx)
		if err != nil {
			return res, err
		}
		for _, card := range cards {
			if seen[card.ExternalRef] {
				continue
			}
			owner, err := im.store.ResolveByAppHandle(ctx, "contacts", card.ExternalRef)
			if err != nil {
				return res, err
			}
			if owner != nil {
				linked, err := im.store.LinkedContactRef(ctx, owner.ID)
				if err != nil {
					return res, err
				}
				if linked != "" {
					if err := im.store.UnlinkContactPrincipal(ctx, owner.ID); err != nil {
						return res, err
					}
				}
			}
			if err := im.store.RemoveImportedContactCard(ctx, card.ExternalRef); err != nil {
				return res, err
			}
		}
	}
	if len(res.Errors) == 0 {
		if err := im.store.SetUIState(ctx, contactsLastImportKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
			log.Printf("[contacts] record last import time: %v", err)
		}
	}
	return res, nil
}

// contactHandle is one app_handles lookup a contact makes.
type contactHandle struct {
	app    string
	handle string
}

// importContact links one contact to the principal it already matches, or
// creates a Contact for it, then binds its identifier, phones and emails.
//
// Matching runs in this order: the source identifier (a re-import), then each
// phone as an imessage handle, then each email as an email handle. The first
// non-anonymous principal found becomes the target. Anonymous buckets that
// match are folded into the target with MergePrincipals. A contact that
// matches Yansu's own self principal is skipped.
func (im contactsImporter) importContact(ctx context.Context, source string, c systemContact, res *ContactsImportResult) {
	name := contactDisplayName(c)
	phones := uniqueNonEmpty(c.Phones, normalizeContactPhone)
	emails := uniqueNonEmpty(c.Emails, normalizeContactEmail)
	id := strings.TrimSpace(c.ID)
	if name == "" {
		res.Skipped++
		return
	}

	handleApp := contactHandleApp(source)
	refs := []contactHandle{{app: handleApp, handle: id}}
	for _, p := range phones {
		refs = append(refs, contactHandle{app: "imessage", handle: p})
	}
	for _, e := range emails {
		refs = append(refs, contactHandle{app: "email", handle: e})
	}

	resolver := identity.NewResolver(im.store, false)
	var matches []identity.SubjectRef
	matched := map[string]bool{}
	for _, ref := range refs {
		subject, err := resolveContactHandle(ctx, resolver, ref)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", name, err))
			return
		}
		if subject.PrincipalID == "" || matched[subject.PrincipalID] {
			continue
		}
		matched[subject.PrincipalID] = true
		matches = append(matches, subject)
	}
	for _, m := range matches {
		if m.Kind == identity.SubjectSelf {
			linked, err := im.store.LinkedContactRef(ctx, m.PrincipalID)
			if err != nil {
				res.Errors = append(res.Errors, "read explicit contact link")
				return
			}
			if linked != id {
				res.Skipped++
				return
			}
		}
	}

	target := ""
	for _, m := range matches {
		if m.Kind != identity.SubjectAnonymous {
			target = m.PrincipalID
			break
		}
	}
	created := false
	if target == "" {
		// Recover a principal whose creation committed before a cancellation
		// prevented its first handle from being attached.
		rows, err := im.store.ListPrincipals(ctx)
		if err != nil {
			res.Errors = append(res.Errors, "read imported people")
			return
		}
		for _, row := range rows {
			if row.Source == source && row.ExternalRef == id {
				target = row.ID
				break
			}
		}
	}
	if target == "" {
		dto, err := createContactRecordWithProvenance(ctx, im.store, source, source, id, name, "", nil, nil)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: create contact: %v", name, err))
			return
		}
		target = dto.ID
		created = true
	}

	for _, m := range matches {
		if m.PrincipalID == target || m.Kind != identity.SubjectAnonymous {
			continue
		}
		if _, err := im.store.MergePrincipals(ctx, m.PrincipalID, target); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: fold chat contact: %v", name, err))
		}
	}

	if !created {
		row, err := im.store.GetPrincipal(ctx, target)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", name, err))
			return
		}
		// Only a principal this import created is renamed on re-import. A
		// person the user curated keeps the name they chose.
		if row.Source == source && row.DisplayName != name {
			row.DisplayName = name
			if _, err := im.store.UpdatePrincipal(ctx, row); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: rename: %v", name, err))
			}
		}
	}

	before, err := im.store.ListAppHandlesByPrincipal(ctx, target)
	if err != nil {
		res.Errors = append(res.Errors, "read contact handles")
		return
	}
	known := map[int64]bool{}
	for _, h := range before {
		known[h.ID] = true
	}
	errorCount := len(res.Errors)
	for _, ref := range refs {
		im.bindContactHandle(ctx, resolver, target, name, source, id, ref, res)
	}
	if source == contactsSourceSystem {
		after, err := im.store.ListAppHandlesByPrincipal(ctx, target)
		if err != nil {
			res.Errors = append(res.Errors, "read imported handles")
			return
		}
		var added, retained []int64
		for _, h := range after {
			if !known[h.ID] {
				added = append(added, h.ID)
			}
			for _, ref := range refs {
				if h.App == ref.app && (h.HandleID == ref.handle || h.DisplayHandle == ref.handle) {
					retained = append(retained, h.ID)
					break
				}
			}
		}
		payload, _ := json.Marshal(c)
		// Leave a retryable cache entry after partial handle failures.
		if len(res.Errors) != errorCount {
			var retry map[string]json.RawMessage
			_ = json.Unmarshal(payload, &retry)
			retry["_retry"] = json.RawMessage(`true`)
			payload, _ = json.Marshal(retry)
		}
		if err := im.store.SaveImportedContactCard(ctx, storage.ImportedContactCard{ExternalRef: id, Payload: string(payload)}, added, retained); err != nil {
			res.Errors = append(res.Errors, "save imported contact")
		}
	}

	if created {
		res.Imported++
	} else {
		res.Merged++
	}
}

// bindContactHandle attaches one handle to the target principal. A handle that
// already belongs to another principal is left where it is and reported, so an
// import never moves a number or address away from someone it was linked to.
func (im contactsImporter) bindContactHandle(ctx context.Context, resolver *identity.Resolver, target, name, source, contactID string, ref contactHandle, res *ContactsImportResult) {
	if ref.handle == "" {
		return
	}
	owner, err := resolveContactHandle(ctx, resolver, ref)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", name, err))
		return
	}
	if owner.PrincipalID == target {
		return
	}
	if owner.PrincipalID != "" {
		res.Errors = append(res.Errors, fmt.Sprintf("%s: %s already belongs to another person, so it was not linked", name, contactHandleLabel(ref.app)))
		return
	}
	if source == contactsSourceSystem {
		if err := im.store.BindImportedContactHandle(ctx, target, contactID, ref.app, ref.handle); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: link %s: %v", name, contactHandleLabel(ref.app), err))
		}
		return
	}
	if _, err := im.store.BindAppHandle(ctx, target, ref.app, ref.handle, ref.handle); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("%s: link %s: %v", name, contactHandleLabel(ref.app), err))
	}
}

// Phone/email handles entered by hand may have only a display handle. Use the
// resolver's fallback for these identifiers, never for the contact's name or
// opaque address-book ID, which are not evidence that two people are the same.
func resolveContactHandle(ctx context.Context, resolver *identity.Resolver, ref contactHandle) (identity.SubjectRef, error) {
	displayHandle := ""
	switch ref.app {
	case "imessage", "email":
		displayHandle = ref.handle
	}
	return resolver.ResolveSender(ctx, ref.app, ref.handle, displayHandle)
}

// removeImportedContacts deletes only principals the system address book
// import created. Existing people and vCard imports are kept.
func removeImportedContacts(ctx context.Context, store *storage.Store) (ContactsImportResult, error) {
	contactsImportMu.Lock()
	defer contactsImportMu.Unlock()
	res := ContactsImportResult{Errors: []string{}}
	rows, err := store.ListPrincipals(ctx)
	if err != nil {
		return res, fmt.Errorf("list principals: %w", err)
	}
	for _, p := range rows {
		ref, err := store.LinkedContactRef(ctx, p.ID)
		if err != nil {
			return res, err
		}
		if ref != "" {
			if err := store.UnlinkContactPrincipal(ctx, p.ID); err != nil {
				return res, err
			}
		}
	}
	if err := store.RemoveImportedContactCards(ctx); err != nil {
		return res, err
	}
	rows, err = store.ListPrincipals(ctx)
	if err != nil {
		return res, err
	}
	for _, p := range rows {
		if p.Source != contactsSourceSystem {
			continue
		}
		if err := store.DeletePrincipal(ctx, p.ID); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", p.DisplayName, err))
			continue
		}
		// A person removed from the system contacts withdraws what briefs cite about them.
		publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "principal", ID: p.ID})
		res.Removed++
	}
	return res, nil
}

// countImportedPrincipals counts the principals an import created and still exists.
func countImportedPrincipals(ctx context.Context, store *storage.Store) (int, error) {
	rows, err := store.ListPrincipals(ctx)
	if err != nil {
		return 0, fmt.Errorf("list principals: %w", err)
	}
	count := 0
	for _, p := range rows {
		if p.Source == contactsSourceSystem {
			count++
		}
	}
	return count, nil
}

// contactHandleApp is the app_handles app that keys a contact to its source
// record, so a re-import finds the principal it created or merged into.
func contactHandleApp(source string) string {
	switch source {
	case contactsSourceVCard:
		return "vcard"
	default:
		return "contacts"
	}
}

func contactHandleLabel(app string) string {
	switch app {
	case "imessage":
		return "this phone number"
	case "email":
		return "this email address"
	default:
		return "this contact record"
	}
}

// contactDisplayName picks the name shown for a contact: the vCard full name,
// then given and family name, then the organization, then the first number or
// address. An empty result means the contact has nothing to show.
func contactDisplayName(c systemContact) string {
	if name := strings.TrimSpace(c.FullName); name != "" {
		return name
	}
	given := strings.TrimSpace(c.GivenName)
	family := strings.TrimSpace(c.FamilyName)
	if name := strings.TrimSpace(given + " " + family); name != "" {
		return name
	}
	if org := strings.TrimSpace(c.Organization); org != "" {
		return org
	}
	for _, p := range c.Phones {
		if v := strings.TrimSpace(p); v != "" {
			return v
		}
	}
	for _, e := range c.Emails {
		if v := strings.TrimSpace(e); v != "" {
			return v
		}
	}
	return ""
}

// normalizeContactPhone reduces a number to digits, keeping a leading "+" for
// international numbers so they match iMessage handles, which are stored in
// E.164. A number with fewer than seven digits is dropped. A number written
// without a country code is kept as digits only; it cannot match an E.164
// handle, so it links only to a handle stored in the same form.
func normalizeContactPhone(raw string) string {
	s := strings.TrimSpace(raw)
	international := strings.HasPrefix(s, "+")
	if strings.HasPrefix(s, "00") {
		s = s[2:]
		international = true
	}
	var digits strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	if digits.Len() < 7 {
		return ""
	}
	if international {
		return "+" + digits.String()
	}
	return digits.String()
}

// normalizeContactEmail lowercases an address and drops values that are not
// shaped like one.
func normalizeContactEmail(raw string) string {
	e := strings.ToLower(strings.TrimSpace(raw))
	at := strings.IndexByte(e, '@')
	if at <= 0 || at == len(e)-1 || strings.ContainsAny(e, " \t") {
		return ""
	}
	return e
}

// uniqueNonEmpty normalizes each value, drops empty results and duplicates, and
// keeps the first occurrence order.
func uniqueNonEmpty(values []string, normalize func(string) string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, v := range values {
		n := normalize(v)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// trimmedNonEmpty returns the trimmed, non-empty values in order. It never
// returns nil, so the JSON shape stays an array.
func trimmedNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parseVCards reads every card in a vCard 2.1, 3.0 or 4.0 file. Only FN, N,
// ORG, UID, TEL and EMAIL are kept. Folded lines are unfolded, and
// quoted-printable values are decoded. Notes, photos and other properties are
// ignored, and a card without a UID gets a stable id from its content.
func parseVCards(data []byte) []systemContact {
	text := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(data))
	var out []systemContact
	var card *vcardCard
	for _, line := range unfoldVCardLines(text) {
		name, params, value := splitVCardLine(line)
		switch name {
		case "BEGIN":
			if strings.EqualFold(strings.TrimSpace(value), "VCARD") {
				card = &vcardCard{}
			}
		case "END":
			if card != nil && strings.EqualFold(strings.TrimSpace(value), "VCARD") {
				out = append(out, card.contact())
				card = nil
			}
		default:
			if card != nil {
				card.add(name, params, value)
			}
		}
	}
	return out
}

// unfoldVCardLines joins continuation lines (starting with a space or tab) onto
// the line before them and drops blank lines.
func unfoldVCardLines(text string) []string {
	var lines []string
	for _, raw := range strings.Split(text, "\n") {
		if len(lines) > 0 && (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) {
			lines[len(lines)-1] += raw[1:]
			continue
		}
		if strings.TrimSpace(raw) != "" {
			lines = append(lines, raw)
		}
	}
	return lines
}

// splitVCardLine splits "item1.TEL;TYPE=CELL:+1..." into the upper-case
// property name, its parameters and the raw value. The group prefix is dropped.
func splitVCardLine(line string) (string, []string, string) {
	head, value, found := strings.Cut(line, ":")
	if !found {
		return "", nil, ""
	}
	parts := strings.Split(head, ";")
	name := strings.ToUpper(strings.TrimSpace(parts[0]))
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		name = name[dot+1:]
	}
	return name, parts[1:], value
}

// splitVCardFields splits a structured value (N, ORG) on unescaped semicolons
// and unescapes each field.
func splitVCardFields(value string) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\\' && i+1 < len(value) {
			cur.WriteByte(c)
			cur.WriteByte(value[i+1])
			i++
			continue
		}
		if c == ';' {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	parts = append(parts, cur.String())
	for i := range parts {
		parts[i] = unescapeVCardValue(parts[i])
	}
	return parts
}

// unescapeVCardValue resolves the vCard escapes for newline, comma, semicolon
// and backslash. Other sequences are kept as written.
func unescapeVCardValue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		case ',', ';', '\\':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// vcardCard collects the properties of one card while the file is read.
type vcardCard struct {
	uid, fullName, family, given, org string
	phones, emails                    []string
}

func (c *vcardCard) add(name string, params []string, value string) {
	if hasQuotedPrintable(params) {
		if decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(value))); err == nil {
			value = string(decoded)
		}
	}
	switch name {
	case "UID":
		if c.uid == "" {
			c.uid = strings.TrimSpace(unescapeVCardValue(value))
		}
	case "FN":
		if c.fullName == "" {
			c.fullName = strings.TrimSpace(unescapeVCardValue(value))
		}
	case "N":
		if c.family == "" && c.given == "" {
			fields := splitVCardFields(value)
			c.family = strings.TrimSpace(fields[0])
			if len(fields) > 1 {
				c.given = strings.TrimSpace(fields[1])
			}
		}
	case "ORG":
		if c.org == "" {
			c.org = strings.TrimSpace(splitVCardFields(value)[0])
		}
	case "TEL":
		number := value
		if len(number) >= 4 && strings.EqualFold(number[:4], "tel:") {
			number = number[4:]
		}
		if v := strings.TrimSpace(unescapeVCardValue(number)); v != "" {
			c.phones = append(c.phones, v)
		}
	case "EMAIL":
		if v := strings.TrimSpace(unescapeVCardValue(value)); v != "" {
			c.emails = append(c.emails, v)
		}
	}
}

func (c *vcardCard) contact() systemContact {
	id := c.uid
	if id == "" {
		key := strings.Join([]string{
			c.fullName, c.family, c.given, c.org,
			strings.Join(c.phones, ","), strings.Join(c.emails, ","),
		}, "\x1f")
		sum := sha256.Sum256([]byte(key))
		id = "vcard-" + hex.EncodeToString(sum[:8])
	}
	return systemContact{
		ID:           id,
		GivenName:    c.given,
		FamilyName:   c.family,
		FullName:     c.fullName,
		Organization: c.org,
		Phones:       c.phones,
		Emails:       c.emails,
	}
}

func hasQuotedPrintable(params []string) bool {
	for _, p := range params {
		if strings.Contains(strings.ToUpper(p), "QUOTED-PRINTABLE") {
			return true
		}
	}
	return false
}
