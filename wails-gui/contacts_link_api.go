package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"wails-gui/internal/identity"
	"wails-gui/storage"
)

type ContactMatchDTO struct {
	ContactID   string   `json:"contactId"`
	DisplayName string   `json:"displayName"`
	Photo       string   `json:"photo"`
	Phones      []string `json:"phones"`
	Emails      []string `json:"emails"`
	SuggestedBy string   `json:"suggestedBy"`
	Available   bool     `json:"available"`
}

// SearchImportedContacts searches the local cache, never the OS address book.
// Identifiers are routing keys only; the picker renders name, phone and email.
func (a *App) SearchImportedContacts(personID, query string) ([]ContactMatchDTO, error) {
	im, err := a.newContactsImporter()
	if err != nil {
		return nil, err
	}
	return im.searchImportedContacts(context.Background(), personID, query)
}

func (im contactsImporter) searchImportedContacts(ctx context.Context, personID, query string) ([]ContactMatchDTO, error) {
	if !hasCurrentContactsConsent(im.consentPath) {
		return nil, errContactsConsentRequired
	}
	person, err := im.store.GetPrincipal(ctx, personID)
	if err != nil {
		return nil, err
	}
	handles, err := im.store.ListAppHandlesByPrincipal(ctx, personID)
	if err != nil {
		return nil, err
	}
	cards, err := im.store.ListImportedContactCards(ctx)
	if err != nil {
		return nil, err
	}
	resolver := identity.NewResolver(im.store, false)
	phones, emails := map[string]bool{}, map[string]bool{}
	names := append([]string{person.DisplayName}, person.Aliases...)
	for _, h := range handles {
		if h.App == "contacts" || h.App == "vcard" {
			continue
		}
		for _, v := range []string{h.HandleID, h.DisplayHandle} {
			if p := normalizeContactPhone(v); p != "" {
				phones[p] = true
			}
			if e := normalizeContactEmail(v); e != "" {
				emails[e] = true
			}
		}
		names = append(names, h.DisplayHandle)
	}
	out := []ContactMatchDTO{}
	q := strings.ToLower(strings.TrimSpace(query))
	phoneQuery := normalizeContactSearchPhone(q)
	for _, card := range cards {
		var c systemContact
		if json.Unmarshal([]byte(card.Payload), &c) != nil || c.ID == "" {
			continue
		}
		name := contactDisplayName(c)
		haystack := strings.ToLower(strings.Join(append(append([]string{name}, c.Phones...), c.Emails...), " "))
		phoneMatch := false
		if phoneQuery != "" {
			for _, p := range c.Phones {
				if strings.Contains(normalizeContactSearchPhone(p), phoneQuery) {
					phoneMatch = true
					break
				}
			}
		}
		if q != "" && !strings.Contains(haystack, q) && !phoneMatch {
			continue
		}
		owner, err := im.store.ResolveByAppHandle(ctx, "contacts", c.ID)
		if err != nil {
			return nil, err
		}
		if owner == nil || owner.Kind == storage.PrincipalKindSelf {
			continue
		}
		reason := ""
		for _, p := range c.Phones {
			if phones[normalizeContactPhone(p)] {
				reason = "phone"
				break
			}
		}
		if reason == "" {
			for _, e := range c.Emails {
				if emails[normalizeContactEmail(e)] {
					reason = "email"
					break
				}
			}
		}
		if reason == "" && resolver.ContactNamesMatch(names, []string{name, c.GivenName + " " + c.FamilyName}) {
			reason = "name"
		}
		linked, err := im.store.LinkedContactRef(ctx, owner.ID)
		if err != nil {
			return nil, err
		}
		available := owner.ID != personID && linked == ""
		out = append(out, ContactMatchDTO{ContactID: c.ID, DisplayName: name, Photo: contactPhoto(c.Photo), Phones: trimmedNonEmpty(c.Phones), Emails: trimmedNonEmpty(c.Emails), SuggestedBy: reason, Available: available})
	}
	rank := func(s string) int {
		switch s {
		case "phone":
			return 0
		case "email":
			return 1
		case "name":
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i].SuggestedBy) != rank(out[j].SuggestedBy) {
			return rank(out[i].SuggestedBy) < rank(out[j].SuggestedBy)
		}
		return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName)
	})
	return out, nil
}

func normalizeContactSearchPhone(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, value)
}

// Only embedded thumbnails from the computer are rendered. External images
// would cause an address-book read to make an unsolicited network request.
func contactPhoto(value string) string {
	if len(value) > 1<<20 {
		return ""
	}
	for _, prefix := range []string{"data:image/jpeg;base64,", "data:image/png;base64,", "data:image/webp;base64,", "data:image/gif;base64,"} {
		if strings.HasPrefix(value, prefix) {
			return value
		}
	}
	return ""
}

func (a *App) LinkPersonToContact(personID, contactID string) error {
	// Who you know shows the person's own change at once.
	defer a.touchDerivedViews(derivedPeopleOverview)
	im, err := a.newContactsImporter()
	if err != nil {
		return err
	}
	if err := im.linkContact(context.Background(), personID, contactID); err != nil {
		return err
	}
	if a.ctx != nil {
		a.emitBrowserEvent("contacts:updated")
	}
	return nil
}

func (im contactsImporter) linkContact(ctx context.Context, personID, contactID string) error {
	contactsImportMu.Lock()
	defer contactsImportMu.Unlock()
	if !hasCurrentContactsConsent(im.consentPath) {
		return errContactsConsentRequired
	}
	cards, err := im.store.ListImportedContactCards(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, c := range cards {
		if c.ExternalRef == contactID {
			found = true
			break
		}
	}
	if !found {
		return errors.New("contact is no longer available")
	}
	owner, err := im.store.ResolveByAppHandle(ctx, "contacts", contactID)
	if err != nil {
		return err
	}
	if owner == nil {
		return errors.New("contact is no longer available")
	}
	return im.store.MergeContactIntoPrincipal(ctx, owner.ID, personID, contactID)
}

func (a *App) UnlinkPersonContact(personID string) error {
	// Who you know shows the person's own change at once.
	defer a.touchDerivedViews(derivedPeopleOverview)
	store, err := a.requireStore()
	if err != nil {
		return err
	}
	contactsImportMu.Lock()
	defer contactsImportMu.Unlock()
	if err := store.UnlinkContactPrincipal(context.Background(), personID); err != nil {
		return err
	}
	if a.ctx != nil {
		a.emitBrowserEvent("contacts:updated")
	}
	return nil
}

// decorateContactDTOs reads the cached thumbnails once per People load. Names
// are presentation overlays: unlink immediately reveals the original identity.
func decorateContactDTOs(ctx context.Context, store *storage.Store, dtos []*PrincipalDTO) error {
	cards, err := store.ListImportedContactCards(ctx)
	if err != nil {
		return err
	}
	byRef := map[string]systemContact{}
	for _, card := range cards {
		var c systemContact
		if json.Unmarshal([]byte(card.Payload), &c) == nil {
			byRef[card.ExternalRef] = c
		}
	}
	linkedByPrincipal, linksErr := store.LinkedContactRefs(ctx)
	for _, dto := range dtos {
		var linked string
		if linksErr != nil {
			linked, err = store.LinkedContactRef(ctx, dto.ID)
			if err != nil {
				return err
			}
		} else {
			linked = linkedByPrincipal[dto.ID]
		}
		dto.ContactLinked = linked != ""
		ref := linked
		if ref == "" {
			for _, h := range dto.AppHandles {
				if h.App == "contacts" {
					ref = h.HandleID
					break
				}
			}
		}
		if c, ok := byRef[ref]; ok {
			dto.ContactName = contactDisplayName(c)
			dto.ContactPhoto = contactPhoto(c.Photo)
		}
	}
	return nil
}
