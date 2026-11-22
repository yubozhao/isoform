package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/identity"
	"wails-gui/storage"
)

const (
	peopleInjectMaxMatches = 3
	peopleInjectMaxChars   = 2800
	peopleInjectMinNameLen = 2 // runes; avoid matching 1-char names against random text
)

// injectPeopleContext detects which of the user's known People (principals)
// are named in the message and prepends their profile — current readable local signals — as a <people-context> block. This is the
// reason the agent can answer "what's my relationship with X" directly from
// context instead of spelunking the database with Bash/sqlite.
//
// query is the RAW user message. Returns "" when no known person is mentioned,
// so the block only appears when relevant (keeping the prompt prefix stable
// for caching on unrelated turns).
func (a *App) injectPeopleContext(query string) string {
	content, err := a.readPeopleContext(context.Background(), query)
	if err != nil {
		return "The people context could not be read."
	}
	return content
}

func (a *App) readPeopleContext(ctx context.Context, query string) (string, error) {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return "", nil
	}
	store, err := a.requireStore()
	if err != nil {
		return "", err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	principals, err := store.ListPrincipals(ctx)
	if err != nil {
		return "", err
	}
	if len(principals) == 0 {
		return "", nil
	}

	type match struct {
		p      storage.PrincipalRow
		hitLen int // length of the matched name token (longer = more specific)
	}
	// Match against both the raw query and a space-stripped form so a name
	// like "Momo子" still matches when the user typed "momo 子" with a space
	// (common when an input method segments CJK + latin).
	qStripped := stripSpaces(q)
	var matches []match
	for _, p := range principals {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		candidates := append([]string{p.DisplayName}, p.Aliases...)
		best := 0
		for _, c := range candidates {
			cl := strings.ToLower(strings.TrimSpace(c))
			if len([]rune(cl)) < peopleInjectMinNameLen {
				continue
			}
			if strings.Contains(q, cl) || strings.Contains(qStripped, stripSpaces(cl)) {
				if len(cl) > best {
					best = len(cl)
				}
			}
		}
		if best == 0 {
			continue
		}
		allowed, err := a.peopleProfileRead(ctx, p, nil)
		if err != nil {
			return "", err
		}
		if allowed {
			matches = append(matches, match{p: p, hitLen: best})
		}
	}
	if len(matches) == 0 {
		return "", nil
	}
	// Most specific (longest matched name) first, then cap.
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].hitLen > matches[j].hitLen })
	if len(matches) > peopleInjectMaxMatches {
		matches = matches[:peopleInjectMaxMatches]
	}

	var sb strings.Builder
	sb.WriteString("<people-context>\n")
	sb.WriteString("People from the user's Isoform People list who are named in this message. This IS your knowledge of them — answer identity/relationship questions directly from here. Do NOT run Bash/sqlite to re-derive this from the database.\n\n")
	for _, m := range matches {
		notes, err := a.personCorrectionNotes(ctx, m.p.ID)
		if err != nil {
			return "", err
		}
		a.writePersonBlock(ctx, &sb, store, m.p)
		if notes != "" {
			sb.WriteString("- User corrections: " + redactActivityModelText(notes) + "\n")
		}
		if sb.Len() > peopleInjectMaxChars {
			break
		}
	}
	sb.WriteString("</people-context>\n\n")
	return sb.String(), ctx.Err()
}

// Observed identity text is derived content too. A curated/imported profile
// with no observation lineage is direct People data; inferred profiles need
// every recorded session to remain readable.
// peopleProfileAllowsModelRead evaluates the profile lineage gate once per
// person per read generation when a cache is shared.
func (a *App) peopleProfileAllowsModelRead(ctx context.Context, p storage.PrincipalRow, cache *peopleReadGateCache) bool {
	allowed, _ := a.peopleProfileRead(ctx, p, cache)
	return allowed
}

func (a *App) peopleProfileRead(ctx context.Context, p storage.PrincipalRow, cache *peopleReadGateCache) (bool, error) {
	if a == nil || a.store == nil || ctx.Err() != nil || a.IsClosedEyesModeEnabled() || personResearchAppProtected(p.CreatedFrom) {
		return false, ctx.Err()
	}
	if cache != nil {
		if ok, seen := cache.profile[p.ID]; seen {
			return ok, nil
		}
	}
	sightings, err := a.principalSessionSightingsCached(ctx, p.ID, cache)
	ok := err == nil && a.peopleProfileLineageAllowsModelRead(ctx, p, sightings, cache)
	if cache != nil && err == nil {
		cache.profile[p.ID] = ok
	}
	if err != nil {
		return false, err
	}
	return ok, ctx.Err()
}

// Retained merge sources use this same gate with their original lineage.
// cache memoizes the per-session dependency read within one read generation.
func (a *App) peopleProfileLineageAllowsModelRead(ctx context.Context, p storage.PrincipalRow, sightings []storage.PrincipalSessionSightingRow, cache *peopleReadGateCache) bool {
	if a == nil || a.store == nil || ctx.Err() != nil || a.IsClosedEyesModeEnabled() || personResearchAppProtected(p.CreatedFrom) || len(sightings) == 0 && strings.HasPrefix(p.CreatedFrom, "auto_") {
		return false
	}
	for _, sighting := range sightings {
		if ctx.Err() != nil {
			return false
		}
		allowed, seen := false, false
		if cache != nil {
			allowed, seen = cache.depends[sighting.SessionID]
		}
		if !seen {
			allowed = a.activityDependencyAllowsModelReadWithCache(sighting.SessionID, nil, cache)
			if cache != nil {
				cache.depends[sighting.SessionID] = allowed
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

type principalSessionSightingReader interface {
	ReadRevision(context.Context) (storage.ReadRevision, error)
	ListPrincipalSessionSightingsPage(context.Context, string, int, int) ([]storage.PrincipalSessionSightingRow, error)
}

// A context budget is not a lifetime provenance limit. Read the entire lineage
// with bounded pages, and fail closed on an error, cancellation or write that
// could move a session across page boundaries.
func (a *App) principalSessionSightingsForRead(ctx context.Context, id string) ([]storage.PrincipalSessionSightingRow, error) {
	return readPrincipalSessionSightings(ctx, a.store, id)
}

func readPrincipalSessionSightings(ctx context.Context, reader principalSessionSightingReader, id string) ([]storage.PrincipalSessionSightingRow, error) {
	before, err := reader.ReadRevision(ctx)
	if err != nil {
		return nil, err
	}
	var all []storage.PrincipalSessionSightingRow
	for offset := 0; ; offset += todayChatCandidateLimit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := reader.ListPrincipalSessionSightingsPage(ctx, id, todayChatCandidateLimit, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < todayChatCandidateLimit {
			break
		}
	}
	after, err := reader.ReadRevision(ctx)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, storage.ErrReadRevisionChanged
	}
	return all, ctx.Err()
}

func (a *App) writePersonBlock(ctx context.Context, sb *strings.Builder, store *storage.Store, p storage.PrincipalRow) {
	p.DisplayName = redactActivityModelText(p.DisplayName)
	p.Relationship = redactActivityModelText(p.Relationship)
	p.Notes = redactActivityModelText(p.Notes)
	p.Aliases = append([]string(nil), p.Aliases...)
	for i := range p.Aliases {
		p.Aliases[i] = redactActivityModelText(p.Aliases[i])
	}
	sb.WriteString("## " + p.DisplayName)
	if len(p.Aliases) > 0 {
		sb.WriteString(" (aka " + strings.Join(p.Aliases, ", ") + ")")
	}
	sb.WriteString("\n")
	// The ref is what CiteSources accepts (chat_sources.go).
	sb.WriteString("- Ref: person:" + p.ID + "\n")

	kindLabel := map[string]string{
		storage.PrincipalKindSelf:           "the user themselves (self)",
		storage.PrincipalKindContact:        "a named contact",
		storage.PrincipalKindAnonymous:      "an unnamed / auto-detected contact",
		storage.PrincipalKindContentSubject: "a content subject",
	}[p.Kind]
	if kindLabel != "" {
		sb.WriteString("- Kind: " + kindLabel + "\n")
	}
	if r := strings.TrimSpace(p.Relationship); r != "" {
		sb.WriteString("- Relationship label: " + r + "\n")
	}
	if n := strings.TrimSpace(p.Notes); n != "" {
		sb.WriteString("- User notes: " + n + "\n")
	}

	if handles, err := a.peopleHandlesForModel(ctx, store, p); err == nil && len(handles) > 0 {
		var hs []string
		for _, h := range handles {
			if personResearchAppProtected(h.App) {
				continue
			}
			label := strings.TrimSpace(h.DisplayHandle)
			if label == "" {
				label = strings.TrimSpace(h.HandleID)
			}
			if label == "" {
				continue
			}
			label = redactActivityModelText(label)
			hs = append(hs, identity.AppDisplayName(h.App)+": "+label)
		}
		if len(hs) > 0 {
			sb.WriteString("- Accounts: " + strings.Join(hs, "; ") + "\n")
		}
	}

	seed := a.buildResearchSeed(ctx, p, "")
	if seed.Stats != "" {
		sb.WriteString(seed.Stats + "\n")
	}
	if seed.Recent != "" {
		sb.WriteString("- Recent message snippets:\n" + indentLines(seed.Recent, "    ") + "\n")
	}
	// Legacy dossiers have no complete source envelope. Their synthesis may
	// quote an app protected after research, so only the local People UI may
	// read them until a writer can retain verifiable per-source provenance.
	sb.WriteString("\n")
}

// stripSpaces removes ASCII spaces and tabs so whitespace differences between
// a stored name and the user's typed form don't defeat substring matching.
func stripSpaces(s string) string {
	return strings.NewReplacer(" ", "", "\t", "").Replace(s)
}

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// Imported handles are local People data until the current sharing disclosure
// is accepted. Source markers also cover imports merged into a curated person.
func (a *App) peopleHandlesForModel(ctx context.Context, store *storage.Store, p storage.PrincipalRow) ([]storage.AppHandleRow, error) {
	handles, err := store.ListAppHandlesByPrincipal(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	filtered := handles[:0]
	for _, handle := range handles {
		if !personResearchAppProtected(handle.App) {
			filtered = append(filtered, handle)
		}
	}
	handles = filtered
	path, pathErr := contactsConsentPath()
	if pathErr == nil && hasCurrentContactsConsent(path) {
		return handles, nil
	}
	imported := p.Source == contactsSourceSystem || p.Source == contactsSourceVCard || p.CreatedFrom == contactsSourceSystem || p.CreatedFrom == contactsSourceVCard
	for _, h := range handles {
		if h.App == "contacts" || h.App == "vcard" {
			imported = true
		}
	}
	if !imported {
		return handles, nil
	}
	var out []storage.AppHandleRow
	for _, h := range handles {
		switch h.App {
		case "contacts", "vcard", "imessage", "email":
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

// personCorrectionNotes reads the current, active memory files so editing or
// forgetting a note also changes what the profile shows.
func (a *App) personCorrectionNotes(ctx context.Context, id string) (string, error) {
	snapshot, err := a.memoryExternalSnapshot()
	if err != nil {
		return "", err
	}
	allow, err := a.memoryReadFilterContext(ctx)
	if err != nil {
		return "", err
	}
	var notes []string
	for i, dep := range snapshot.readDependencies() {
		if dep.correctionPerson != id {
			continue
		}
		entry := snapshot.entries[i]
		if entry.inUse && allow(entry.raw.ID, entry.raw.Content) {
			notes = append(notes, strings.TrimSpace(stripMemoryFrontmatter(entry.raw.Content)))
		}
	}
	return strings.Join(notes, "\n"), ctx.Err()
}
