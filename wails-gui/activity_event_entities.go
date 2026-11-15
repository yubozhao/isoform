package main

import (
	"log"
	"regexp"
	"strings"
	"time"
)

// Entity is a normalized identifier mentioned in an observation. Entities are
// the backbone of cross-app stitching: two observations in different apps may
// only be merged when they share a STRONG entity (§4, §6). See
// docs/activity-event-model-p1-design.md (local).
type Entity struct {
	Type string `json:"type"` // repo | project | channel | person | pr
	Name string `json:"name"` // normalized display name
}

const (
	entityRepo    = "repo"
	entityProject = "project"
	entityChannel = "channel"
	entityPerson  = "person"
	entityPR      = "pr"
	entityTopic   = "topic" // coherent subject/interest (e.g. "cold brew coffee")
)

// isStrongEntityType reports whether an entity type is strong enough to justify
// cross-app merging. channel is deliberately weak: "#general" must not glue
// unrelated events together (§6).
func isStrongEntityType(t string) bool {
	switch t {
	case entityRepo, entityProject, entityPR, entityPerson:
		return true
	default:
		return false
	}
}

var (
	githubPRRe   = regexp.MustCompile(`github\.com/([\w.-]+)/([\w.-]+)/pull/(\d+)`)
	githubRepoRe = regexp.MustCompile(`github\.com/([\w.-]+)/([\w.-]+)`)
	// goModDepSuffixRe matches a go-module version right after a repo path
	// ("github.com/x/y v1.18.1") — the signature of a `go mod download` / go.sum
	// build dependency, NOT a repo the user is working on.
	goModDepSuffixRe = regexp.MustCompile(`^\s+v\d+\.`)
	// "interesting-researches-and-repos (Channel) - Isoform - Slack"
	slackChannelRe = regexp.MustCompile(`^(.+?)\s*\(Channel\)`)
	// "X (DM) - Isoform - Slack"
	dmPersonRe = regexp.MustCompile(`^(.+?)\s*\(DM\)`)
	// "#常规 | Yansu - Discord"  /  inline "#channel"
	hashChannelRe = regexp.MustCompile(`#([\w\p{Han}-]{2,40})`)
	// body sender lines: "Maya Chen [Jul 2nd at 10:15:00 AM]:"
	senderLineRe = regexp.MustCompile(`(?m)^([^\[\]\n]{1,32}?)\s*\[[^\]]*\]\s*:`)
)

// extractEntities pulls normalized entities from one observation using
// deterministic rules over URL / title / body (§4). It intentionally avoids an
// LLM: URL and chat-title signals are exact and zero-cost. Body text is the
// source of truth (already real-app-resolved by §3a).
func extractEntities(o Observation) []Entity {
	var ents []Entity
	seen := map[string]bool{}
	add := func(t, n string) {
		n = normalizeEntityName(n)
		if n == "" {
			return
		}
		key := t + "|" + strings.ToLower(n)
		if seen[key] {
			return
		}
		seen[key] = true
		ents = append(ents, Entity{Type: t, Name: n})
	}

	// 1. GitHub repo/PR — scan URL, title AND body. Chat apps carry the link in
	//    the message TEXT (browser_url is empty for Slack/WeChat/Discord), so a
	//    PR pasted in chat must still resolve. A PR URL also yields repo+project;
	//    add() dedups across every source.
	ghText := o.URL + "\n" + o.Title + "\n" + o.Body
	for _, m := range githubPRRe.FindAllStringSubmatch(ghText, 5) {
		add(entityPR, m[1]+"/"+m[2]+"#"+m[3])
		add(entityRepo, m[1]+"/"+m[2])
		add(entityProject, m[2])
	}
	// Skip go-module dependency lines ("github.com/x/y v1.18.1"): `go mod download`
	// and go.sum list build deps, not repos the user is working on. Extracting them
	// would flood entities (one build → 15 tags) and cause false cross-event merges
	// when two unrelated builds share a common dep (gods/groupcache).
	for _, idx := range githubRepoRe.FindAllStringSubmatchIndex(ghText, -1) {
		if goModDepSuffixRe.MatchString(ghText[idx[1]:]) {
			continue
		}
		owner, repo := ghText[idx[2]:idx[3]], ghText[idx[4]:idx[5]]
		add(entityRepo, owner+"/"+repo)
		add(entityProject, repo)
	}

	// 2. Chat title: Slack channel / DM person / Discord #channel.
	if m := slackChannelRe.FindStringSubmatch(o.Title); m != nil {
		add(entityChannel, m[1])
	}
	if m := dmPersonRe.FindStringSubmatch(o.Title); m != nil {
		add(entityPerson, m[1])
	}
	for _, m := range hashChannelRe.FindAllStringSubmatch(o.Title, 3) {
		// Skip all-digit hashes: "#1984" is a PR/issue number, not a channel.
		// A real channel name always has at least one non-digit character.
		if isAllDigits(m[1]) {
			continue
		}
		add(entityChannel, m[1])
	}

	// 3. Body: conversation senders (people actually in the active thread).
	for _, m := range senderLineRe.FindAllStringSubmatch(o.Body, 8) {
		add(entityPerson, m[1])
	}

	return ents
}

// normalizeEntityName trims punctuation/whitespace noise from an entity name and
// rejects junk. Display form is preserved; comparison is case-insensitive (§6).
func normalizeEntityName(n string) string {
	n = strings.TrimSpace(n)
	n = strings.Trim(n, " \t\r\n-—|:#")
	n = strings.TrimSpace(n)
	if len(n) < 2 {
		return ""
	}
	// Reject obvious non-entities that slip through sender/title regexes.
	switch strings.ToLower(n) {
	case "weixin", "photos and videos", "downloads", "new tab", "login":
		return ""
	}
	return n
}

// annotateEntities fills each observation's Entities slice in place (§4).
func annotateEntities(seq *ObservationSequence) {
	for i := range seq.Observations {
		t0 := time.Now()
		ents := extractEntities(seq.Observations[i])
		// Bridge local repo paths to their GitHub slug so terminal work aligns
		// with browser/chat repo entities (see activity_event_repolink.go).
		ents = append(ents, repoLinkEntities(seq.Observations[i])...)
		seq.Observations[i].Entities = dedupEntities(ents)
		if d := time.Since(t0); d > 2*time.Second {
			logEntityAnnotationDuration(i, d, seq.Observations[i])
		}
	}
}

func logEntityAnnotationDuration(index int, duration time.Duration, observation Observation) {
	log.Printf("[semantic-events] annotateEntities: obs=%d duration=%v app_bytes=%d title_bytes=%d", index, duration, len(observation.RealApp), len(observation.Title))
}

// strongEntityKey returns a stable comparison key for a strong entity, or "" for
// weak/empty ones. Used for candidate clustering and cross-app stitch matching.
func strongEntityKey(e Entity) string {
	if !isStrongEntityType(e.Type) {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(e.Name))
	if name == "" {
		return ""
	}
	return e.Type + "|" + name
}
