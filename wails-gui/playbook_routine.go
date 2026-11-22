package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/engagement"
	"wails-gui/internal/sop"
	"wails-gui/storage"
)

// The routine detector (docs/playbooks-audit.md, break 1). The triager sees
// one session at a time and rejects reading as a process, so it never learns
// "the user opens PostHog every morning and reads three numbers". This
// detector does, without a model: it rolls browser visits up into one row per
// site and day, finds a site opened on most of the last seven days at about
// the same time, writes a draft playbook with steps grounded in the pages the
// user opened, and asks once whether Isoform should take it on.

const (
	routineInterval = time.Hour
	routineFirstRun = 3 * time.Minute
	// routineWindowDays is how many local days (today included) the detector
	// looks at; routineMinDays is on how many of them the site must have been
	// opened within routineSpread of the usual time.
	routineWindowDays = 7
	routineMinDays    = 4
	routineSpread     = 60 * time.Minute
	// routineMaxVisits and routineMinEarlyShare drop sites the user lives in
	// all day (a tool, not a check): the usual day has at most this many
	// visits, and at least this share of them in the first sitting.
	routineMaxVisits     = 60
	routineMinEarlyShare = 0.5
	// routineSiteDaysKeep is how long the site-day summary is kept.
	routineSiteDaysKeep = 21
	// routineProposalGap is the least time between two proposals.
	routineProposalGap = 24 * time.Hour
	// routineLead is how long before the usual visit a scheduled run starts,
	// so the numbers are waiting when the user would have looked.
	routineLead = 10 * time.Minute
	// routineProposalExpiry is how long the suggestion stays open.
	routineProposalExpiry  = 7 * 24 * time.Hour
	routineProposalStakes  = 0.45
	routinePagesInPlaybook = 5
)

// routineExcludedHosts are sites a daily habit on which is not a routine
// worth handing over: search, mail, calendars, documents, chat, video,
// social feeds and AI assistants. A host matches itself and its subdomains.
var routineExcludedHosts = []string{
	"google.com", "bing.com", "duckduckgo.com", "baidu.com", "yahoo.com", "yandex.ru",
	"outlook.com", "outlook.live.com", "outlook.office.com", "outlook.office365.com", "office.com", "live.com", "icloud.com", "proton.me", "fastmail.com",
	"gmail.com", "aol.com", "gmx.com", "gmx.net", "zoho.com", "hey.com", "superhuman.com", "tutanota.com", "yandex.com", "mail.ru", "qq.com", "163.com", "126.com",
	"irs.gov", "ssa.gov", "healthcare.gov", "medicare.gov", "va.gov", "login.gov", "id.me",
	"webmd.com", "mayoclinic.org", "zocdoc.com", "teladoc.com", "betterhelp.com", "talkspace.com", "goodrx.com",
	"tinder.com", "bumble.com", "hinge.co", "match.com", "okcupid.com", "grindr.com",
	"slack.com", "discord.com", "teams.microsoft.com", "zoom.us", "whatsapp.com", "messenger.com", "telegram.org", "wechat.com", "feishu.cn", "larksuite.com",
	"youtube.com", "netflix.com", "twitch.tv", "spotify.com", "bilibili.com", "hulu.com", "disneyplus.com",
	"twitter.com", "x.com", "facebook.com", "instagram.com", "tiktok.com", "linkedin.com", "reddit.com", "weibo.com", "threads.net", "bsky.app",
	"chatgpt.com", "openai.com", "claude.ai", "perplexity.ai", "gemini.google.com",
	"paypal.com", "venmo.com", "wise.com", "stripe.com", "coinbase.com", "robinhood.com", "fidelity.com", "schwab.com", "vanguard.com", "chase.com", "wellsfargo.com", "citi.com", "capitalone.com", "americanexpress.com", "alipay.com",
	"1password.com", "lastpass.com", "bitwarden.com", "okta.com", "mychart.org",
	"localhost",
}

// routineSensitiveWords mark hosts never proposed whatever their domain:
// banks, sign-in pages, password managers, health records.
var routineSensitiveWords = []string{"bank", "login", "signin", "auth", "account", "password", "health", "patient", "medical", "clinic", "pharma", "therapy", "dating", "porn", "xxx", "casino"}

// routineSensitiveTitleWords mark pages never proposed whatever their site:
// money, credentials, health and identity.
var routineSensitiveTitleWords = []string{
	"bank", "balance", "statement", "transaction", "credit card", "password", "sign in", "log in", "login", "verification code",
	"diagnos", "prescription", "lab result", "medical", "patient", "therapy", "salary", "payroll", "tax return", "social security", "passport",
}

// routineTitlesSensitive reports whether any page title names something a
// routine must never be proposed for.
func routineTitlesSensitive(pages []BrowserSitePage) bool {
	for _, page := range pages {
		title := strings.ToLower(page.Title)
		for _, word := range routineSensitiveTitleWords {
			if strings.Contains(title, word) {
				return true
			}
		}
	}
	return false
}

// routineAllowedGoogleHosts are Google dashboards a morning check can be about.
var routineAllowedGoogleHosts = map[string]bool{
	"analytics.google.com": true, "search.google.com": true, "console.cloud.google.com": true,
	"ads.google.com": true, "play.google.com": true, "firebase.google.com": true, "console.firebase.google.com": true,
}

// routineHostExcluded reports whether a host is never proposed.
func routineHostExcluded(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.HasPrefix(host, "[") || strings.Trim(host, "0123456789.") == "" {
		return true
	}
	if routineAllowedGoogleHosts[host] {
		return false
	}
	if strings.HasPrefix(host, "mail.") || strings.HasPrefix(host, "webmail.") || strings.HasSuffix(host, ".gov") {
		return true
	}
	for _, word := range routineSensitiveWords {
		if strings.Contains(host, word) {
			return true
		}
	}
	for _, excluded := range routineExcludedHosts {
		if host == excluded || strings.HasSuffix(host, "."+excluded) {
			return true
		}
	}
	return false
}

// routineCandidate is a site opened at about the same time on most days.
type routineCandidate struct {
	Host     string
	SiteName string
	// Minute is the usual first visit, in minutes after local midnight.
	Minute   int
	Days     int
	Weekdays bool
	Pages    []BrowserSitePage
}

// Time is the usual first visit as "9:10".
func (c routineCandidate) Time() string {
	return fmt.Sprintf("%d:%02d", c.Minute/60, c.Minute%60)
}

// RunTime is when a schedule should run, routineLead before the usual visit
// and on a five-minute mark, as "08:55".
func (c routineCandidate) RunTime() string {
	minute := c.Minute - int(routineLead/time.Minute)
	if minute < 0 {
		minute += 24 * 60
	}
	minute -= minute % 5
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

// detectRoutines finds the sites opened within routineSpread of their usual
// time on at least routineMinDays of the routineWindowDays local days ending
// today, most regular first. Browsers are merged: a site opened in two
// browsers on one day counts once, at its earliest visit.
func detectRoutines(days []BrowserSiteDayRow, now time.Time) []routineCandidate {
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	first := today.AddDate(0, 0, -(routineWindowDays - 1)).Format("2006-01-02")
	type siteDay struct {
		day     string
		weekday time.Weekday
		minute  int
		visits  int
		early   int
		pages   []BrowserSitePage
	}
	byHost := map[string]map[string]*siteDay{}
	for _, row := range days {
		if row.Day < first || row.Day > today.Format("2006-01-02") || routineHostExcluded(row.Host) {
			continue
		}
		at := time.UnixMilli(row.FirstAt).In(loc)
		minute := at.Hour()*60 + at.Minute()
		if byHost[row.Host] == nil {
			byHost[row.Host] = map[string]*siteDay{}
		}
		d, ok := byHost[row.Host][row.Day]
		if !ok {
			day, _ := time.ParseInLocation("2006-01-02", row.Day, loc)
			d = &siteDay{day: row.Day, weekday: day.Weekday(), minute: minute}
			byHost[row.Host][row.Day] = d
		}
		if minute < d.minute {
			d.minute = minute
		}
		d.visits += row.Visits
		d.early += row.Early
		d.pages = append(d.pages, row.Pages...)
	}

	var out []routineCandidate
	for host, perDay := range byHost {
		if len(perDay) < routineMinDays {
			continue
		}
		minutes := make([]int, 0, len(perDay))
		for _, d := range perDay {
			minutes = append(minutes, d.minute)
		}
		sort.Ints(minutes)
		usual := minutes[(len(minutes)-1)/2]
		var matched []*siteDay
		for _, d := range perDay {
			diff := d.minute - usual
			if diff < 0 {
				diff = -diff
			}
			if time.Duration(diff)*time.Minute <= routineSpread {
				matched = append(matched, d)
			}
		}
		if len(matched) < routineMinDays {
			continue
		}
		visits := make([]int, 0, len(matched))
		shares := make([]float64, 0, len(matched))
		weekdays := true
		for _, d := range matched {
			visits = append(visits, d.visits)
			if d.visits > 0 {
				shares = append(shares, float64(d.early)/float64(d.visits))
			}
			if d.weekday == time.Saturday || d.weekday == time.Sunday {
				weekdays = false
			}
		}
		sort.Ints(visits)
		sort.Float64s(shares)
		if visits[(len(visits)-1)/2] > routineMaxVisits || len(shares) == 0 || shares[(len(shares)-1)/2] < routineMinEarlyShare {
			continue
		}
		sort.Slice(matched, func(i, j int) bool { return matched[i].day < matched[j].day })
		var pages []BrowserSitePage
		for _, d := range matched {
			pages = append(pages, d.pages...)
		}
		merged := mergeRoutinePages(pages)
		if routineTitlesSensitive(pages) {
			continue
		}
		titles := make([]string, 0, len(merged))
		for _, page := range merged {
			titles = append(titles, page.Title)
		}
		out = append(out, routineCandidate{
			Host:     host,
			SiteName: routineSiteName(host, titles),
			Minute:   usual,
			Days:     len(matched),
			Weekdays: weekdays,
			Pages:    merged,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Days != out[j].Days {
			return out[i].Days > out[j].Days
		}
		if out[i].Minute != out[j].Minute {
			return out[i].Minute < out[j].Minute
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// mergeRoutinePages adds up the pages of several days by link, keeps the
// latest title of each, and returns the most visited ones.
func mergeRoutinePages(pages []BrowserSitePage) []BrowserSitePage {
	type acc struct {
		page  BrowserSitePage
		order int
	}
	byURL := map[string]*acc{}
	for i, page := range pages {
		a, ok := byURL[page.URL]
		if !ok {
			a = &acc{page: BrowserSitePage{URL: page.URL}, order: i}
			byURL[page.URL] = a
		}
		a.page.Count += page.Count
		if strings.TrimSpace(page.Title) != "" {
			a.page.Title = strings.TrimSpace(page.Title)
		}
	}
	list := make([]*acc, 0, len(byURL))
	for _, a := range byURL {
		list = append(list, a)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].page.Count != list[j].page.Count {
			return list[i].page.Count > list[j].page.Count
		}
		return list[i].order < list[j].order
	})
	out := make([]BrowserSitePage, 0, routinePagesInPlaybook)
	for _, a := range list {
		if len(out) >= routinePagesInPlaybook {
			break
		}
		out = append(out, a.page)
	}
	return out
}

// routineTitleSeparators split a page title from the site name it ends with.
var routineTitleSeparators = []string{" • ", " · ", " | ", " — ", " – ", " - "}

// routineSiteName names a site the way its pages do: the part most titles end
// with ("Downloads • PostHog" gives PostHog), or else the host's main label.
func routineSiteName(host string, titles []string) string {
	counts := map[string]int{}
	for _, title := range titles {
		for _, sep := range routineTitleSeparators {
			if i := strings.LastIndex(title, sep); i > 0 {
				if name := strings.TrimSpace(title[i+len(sep):]); name != "" && len([]rune(name)) <= 40 {
					counts[name]++
				}
				break
			}
		}
	}
	best, bestCount := "", 0
	for name, count := range counts {
		if count > bestCount || (count == bestCount && name < best) {
			best, bestCount = name, count
		}
	}
	if best != "" && bestCount*2 >= len(titles) {
		return best
	}
	labels := strings.Split(strings.TrimPrefix(host, "www."), ".")
	main := labels[0]
	if len(labels) >= 2 {
		main = labels[len(labels)-2]
	}
	if main == "" {
		return host
	}
	return strings.ToUpper(main[:1]) + main[1:]
}

// routineTitleMax bounds a page name written into a playbook.
const routineTitleMax = 80

// routinePageName is a page's title without the site name it ends with, as
// plain text: one line, no markup, at most routineTitleMax characters. Titles
// come from pages, so they go into the steps as names, never as prose.
func routinePageName(title, site string) string {
	title = strings.TrimSpace(title)
	for _, sep := range routineTitleSeparators {
		if strings.HasSuffix(title, sep+site) {
			title = strings.TrimSpace(strings.TrimSuffix(title, sep+site))
			break
		}
	}
	title = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return ' '
		case strings.ContainsRune("`<>[]{}*#|\\", r):
			return -1
		default:
			return r
		}
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > routineTitleMax {
		title = strings.TrimSpace(string(runes[:routineTitleMax]))
	}
	return title
}

// routineOpenableURL returns a page link a run can open: a link the scrubber
// cut at a token-like segment ends in "/...", which is dropped.
func routineOpenableURL(link string) string {
	return strings.TrimSuffix(strings.TrimSpace(link), "/...")
}

// routineSites are the origins a run may open: those of the pages the user
// opened, as they were opened (with or without "www.").
func routineSites(pages []BrowserSitePage, host string) []string {
	var links []string
	for _, page := range pages {
		links = append(links, page.URL)
	}
	if origins := playbookOrigins(links); len(origins) > 0 {
		return origins
	}
	return []string{"https://" + host + "/"}
}

// routinePlaybookCopy is the user-facing name, description and trigger of an
// observed playbook in each language.
var routinePlaybookCopy = map[string]struct {
	morning, daily, description, trigger string
}{
	"en": {
		morning:     "%s morning check",
		daily:       "%s daily check",
		description: "Reads the numbers you check on %s (%s) and sends them to you.",
		trigger:     "Most days around %s",
	},
	"zh-CN": {
		morning:     "%s 早间查看",
		daily:       "%s 每日查看",
		description: "读取你在 %s 上查看的数字（%s），并发给你。",
		trigger:     "几乎每天 %s 左右",
	},
}

// buildRoutinePlaybook writes the draft playbook for a routine: steps that
// open the pages the user opened and read what they show, then a short
// report. The steps are for the agent, in English; the title, description
// and trigger are in the user's language.
func buildRoutinePlaybook(c routineCandidate, lang string) sop.Entry {
	words, ok := routinePlaybookCopy[lang]
	if !ok {
		words = routinePlaybookCopy["en"]
	}
	names := make([]string, 0, len(c.Pages))
	for _, page := range c.Pages {
		if name := routinePageName(page.Title, c.SiteName); name != "" {
			names = append(names, name)
		}
	}
	listed := strings.Join(names, ", ")
	if listed == "" {
		listed = c.Host
	}
	titleFormat := words.daily
	if c.Minute < 12*60 {
		titleFormat = words.morning
	}
	sites := routineSites(c.Pages, c.Host)

	var b strings.Builder
	b.WriteString("# Goal\n")
	fmt.Fprintf(&b, "Read the numbers the user checks on %s and report them in a few short lines, so the user does not have to open %s to see them.\n\n", c.SiteName, c.SiteName)
	b.WriteString("# Prerequisites\n")
	fmt.Fprintf(&b, "- The user is signed in to %s in their browser.\n", c.SiteName)
	fmt.Fprintf(&b, "- Open pages only under %s. Do not click, type, change settings or sign in; only open pages and read them.\n\n", strings.Join(sites, " and "))
	b.WriteString("# Steps\n")
	step := 1
	for _, page := range c.Pages {
		link := routineOpenableURL(page.URL)
		name := routinePageName(page.Title, c.SiteName)
		if name == "" {
			name = link
		}
		fmt.Fprintf(&b, "## %d. Read %s\n", step, name)
		fmt.Fprintf(&b, "Open %s with cua_driver open_url, then read it with get_window_state (a screenshot only when the page has no readable text). Note each number the page shows, its name, the period it covers, and the change against the previous period when the page shows one; for a page without numbers, its main items in a few words.\n", link)
		fmt.Fprintf(&b, "**Checkpoint:** the page shows %s and its numbers are noted, or the reason it does not (asked to sign in, did not load) is noted.\n\n", name)
		step++
	}
	fmt.Fprintf(&b, "## %d. Report\n", step)
	b.WriteString("Write the result: one short line per number, with its name, its value, and the change when there is one. Say plainly which page did not load or asked to sign in. No advice, no links.\n")
	b.WriteString("**Checkpoint:** every page above has a line or a reason.\n\n")
	b.WriteString("# Success criteria\n")
	fmt.Fprintf(&b, "Every number the user looks at on %s is in the result, with today's values.\n\n", c.SiteName)
	b.WriteString("# Evidence\n")
	fmt.Fprintf(&b, "Seen in the browser on %d of the last %d days, first visit around %s.", c.Days, routineWindowDays, c.Time())
	if c.Weekdays {
		b.WriteString(" Weekdays only.")
	}
	b.WriteString("\n")
	for _, page := range c.Pages {
		fmt.Fprintf(&b, "- %s (%s), %d visits\n", routineOpenableURL(page.URL), routinePageName(page.Title, c.SiteName), page.Count)
	}

	return sop.Entry{
		Title:       fmt.Sprintf(titleFormat, c.SiteName),
		Description: fmt.Sprintf(words.description, c.SiteName, listed),
		Trigger:     fmt.Sprintf(words.trigger, c.Time()),
		Status:      sop.StatusDraft,
		Source:      sop.SourceObserved,
		SourceIDs:   []string{"routine:" + c.Host},
		Sites:       sites,
		Body:        b.String(),
	}
}

// startPlaybookRoutines runs the detector a few minutes after launch, then
// every hour. The activity store opens in the background at launch, so each
// pass checks for it rather than this start.
func (a *App) startPlaybookRoutines(ctx context.Context) {
	// No background loop while playbooks are off (feature_switches.go).
	if a == nil || a.sopStore == nil || a.playbookPlans == nil || !featureOn(featurePlaybooks) {
		return
	}
	go func() {
		timer := time.NewTimer(routineFirstRun)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				a.checkPlaybookRoutines(ctx, time.Now())
				timer.Reset(routineInterval)
			}
		}
	}()
}

// rollUpBrowserSiteDays stores the site-days of every day the raw visits
// still cover whole: the oldest kept day lost its early visits to pruning,
// so its summary from an earlier pass is left as it was.
func (a *App) rollUpBrowserSiteDays(now time.Time) error {
	store := a.activity.store
	from := now.Add(-browserHistoryRetention)
	visits, err := store.ListBrowserVisits(from.UnixMilli(), now.UnixMilli()+1)
	if err != nil {
		return err
	}
	partial := from.In(now.Location()).Format("2006-01-02")
	var rows []BrowserSiteDayRow
	for _, row := range rollupBrowserSiteDays(visits, now.Location()) {
		if row.Day != partial {
			rows = append(rows, row)
		}
	}
	if err := store.UpsertBrowserSiteDays(rows); err != nil {
		return err
	}
	keepFrom := now.AddDate(0, 0, -routineSiteDaysKeep).Format("2006-01-02")
	_, err = store.PruneBrowserSiteDays(keepFrom)
	return err
}

// checkPlaybookRoutines rolls the visits up and proposes at most one new
// routine: one not proposed before, with no playbook that already opens its
// site, and not within a day of the last proposal.
func (a *App) checkPlaybookRoutines(ctx context.Context, now time.Time) {
	if a == nil || a.activity == nil || a.activity.store == nil || a.sopStore == nil || a.playbookPlans == nil {
		return
	}
	if !a.playbookRoutineMu.TryLock() {
		return
	}
	defer a.playbookRoutineMu.Unlock()
	// Off means no new playbooks: with the Playbooks switch off, screen
	// watching off or observing paused, nothing is rolled up or proposed,
	// and a suggestion not sent yet is withdrawn.
	if !a.playbookLearningAllowed(ctx) {
		a.retireUnsentPlaybookSuggestions(ctx)
		return
	}
	if err := a.rollUpBrowserSiteDays(now); err != nil {
		log.Printf("[playbook] rolling up site visits: %v", err)
		return
	}
	since := now.AddDate(0, 0, -(routineWindowDays - 1)).Format("2006-01-02")
	days, err := a.activity.store.ListBrowserSiteDays(since)
	if err != nil {
		log.Printf("[playbook] reading site days: %v", err)
		return
	}
	candidates := detectRoutines(days, now)
	if len(candidates) == 0 {
		return
	}
	plans, err := a.playbookPlans.List()
	if err != nil {
		log.Printf("[playbook] reading plans: %v", err)
		return
	}
	for _, plan := range plans {
		if plan.Proposal != nil && now.Sub(time.UnixMilli(plan.Proposal.ProposedAt)) < routineProposalGap {
			return
		}
	}
	existing, _ := a.sopStore.List()
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		if proposed, err := a.playbookPlans.ProposedHost(c.Host); err != nil || proposed {
			continue
		}
		if playbookOpensHost(existing, c.Host) {
			continue
		}
		if _, err := a.proposeRoutinePlaybook(c, now); err != nil {
			log.Printf("[playbook] proposing a routine: %v", err)
		}
		return
	}
}

// playbookLearningAllowed reports whether Isoform may learn new playbooks
// from what it observes: the Playbooks switch on, screen watching on, and
// observing not paused.
func (a *App) playbookLearningAllowed(ctx context.Context) bool {
	return a.loadTriageFeatureFlags(ctx).SOP && a.screenWatchingEnabled() && !a.IsClosedEyesModeEnabled()
}

// retireUnsentPlaybookSuggestions withdraws every playbook suggestion that
// has not gone out yet.
func (a *App) retireUnsentPlaybookSuggestions(ctx context.Context) {
	if a.engagement == nil || a.store == nil {
		return
	}
	for _, state := range []string{storage.EngagementStatePending, storage.EngagementStateScheduled} {
		rows, err := a.store.ListEngagementMoments(ctx, state, 0)
		if err != nil {
			return
		}
		for _, row := range rows {
			if row.Kind == string(engagement.KindPlaybookSuggested) {
				_ = a.engagement.Retire(ctx, row.DedupeKey, "playbook learning is off")
			}
		}
	}
}

// playbookOpensHost reports whether a playbook already names a site on host.
func playbookOpensHost(entries []sop.Entry, host string) bool {
	for _, e := range entries {
		for _, site := range e.Sites {
			if u, err := url.Parse(site); err == nil && strings.EqualFold(strings.TrimPrefix(u.Hostname(), "www."), host) {
				return true
			}
		}
	}
	return false
}

// proposeRoutinePlaybook saves the draft playbook and its proposal, tells the
// Playbooks page, and raises the suggestion.
func (a *App) proposeRoutinePlaybook(c routineCandidate, now time.Time) (sop.Entry, error) {
	draft := buildRoutinePlaybook(c, a.currentUILanguage())
	// A random id: an id made from the site's name would carry it into
	// telemetry and logs.
	draft.ID = "routine-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	saved, err := a.sopStore.Save(draft)
	if err != nil {
		return sop.Entry{}, err
	}
	titles := make([]string, 0, len(c.Pages))
	for _, page := range c.Pages {
		if name := routinePageName(page.Title, c.SiteName); name != "" {
			titles = append(titles, name)
		}
	}
	if _, err := a.playbookPlans.Update(saved.ID, func(p *sop.Plan) {
		p.Proposal = &sop.Proposal{
			Host: c.Host, SiteName: c.SiteName, Time: c.Time(), Days: c.Days, Weekdays: c.Weekdays,
			Titles: titles, ProposedAt: now.UnixMilli(),
		}
	}); err != nil {
		return sop.Entry{}, err
	}
	log.Printf("[playbook] proposed %s (%d days)", saved.ID, c.Days)
	a.emitBrowserEvent("sop:updated", map[string]any{"id": saved.ID, "status": string(saved.Status)})
	a.submitEngagementMoment("playbook suggestion", engagement.Moment{
		Kind:      engagement.KindPlaybookSuggested,
		SubjectID: saved.ID,
		DedupeKey: engagement.DedupeKey(engagement.KindPlaybookSuggested, saved.ID),
		Stakes:    routineProposalStakes,
		ExpiresAt: now.Add(routineProposalExpiry),
		Params:    map[string]string{"site": c.SiteName, "time": c.Time()},
	})
	if a.telemetry != nil {
		a.telemetry.Capture("playbook.proposed", map[string]any{"days": c.Days, "pages": len(c.Pages)})
	}
	return saved, nil
}
