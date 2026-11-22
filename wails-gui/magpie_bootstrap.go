package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Bootstrap creates Yansu's three routing groups in the magpie instance:
//
//	yansu-chat       routing=order  stays=session  (chat turns)
//	yansu-background routing=pace   stays=off      (Dream, summaries, extraction)
//	yansu-light      routing=usage  stays=off      (titles, classification)
//
// Every run is idempotent: group add replaces, and a group left without
// members is removed so a revoked subscription cannot linger in it.

const (
	magpieConsentVersion    = 1
	magpieConsentFileName   = "consent.json"
	magpieBootstrapFileName = "bootstrap.json"
	// magpieByokName is the custom provider that carries the user's own key.
	magpieByokName = "yansu-byok"
	// magpieOllamaBaseURL is where Ollama listens by default.
	magpieOllamaBaseURL = "http://127.0.0.1:11434"
	magpieOllamaTimeout = time.Second
)

var errMagpieNotHealthy = errors.New("magpie gateway is not answering")

// MagpieConsent records acceptance of the subscription-use notice. Subscription
// members stay out of every group until it is accepted at the current version.
type MagpieConsent struct {
	Version    int       `json:"version"`
	AcceptedAt time.Time `json:"acceptedAt"`
}

// magpieGroupPlan is one group as Bootstrap applies it.
type magpieGroupPlan struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Routing string   `json:"routing"`
	Stays   string   `json:"stays"`
	Members []string `json:"members"`
}

// magpieBootstrapInput holds everything planMagpieGroups decides from.
type magpieBootstrapInput struct {
	// Catalog is every model id GET /v1/models lists, in catalog order.
	Catalog []string
	// OllamaModels is the local model list in Ollama's order.
	OllamaModels []string
	// ByokChatModel and ByokLightModel are the user's own key's models.
	// ByokLightModel defaults to ByokChatModel when empty.
	ByokChatModel  string
	ByokLightModel string
	// ConsentAccepted records whether the user accepted the note about each
	// provider's terms. It is informational: planMagpieGroups admits
	// subscription members (claude, codex) whenever the build allows them.
	ConsentAccepted bool
	// AccountOrder lists the subscription providers to admit, primary first.
	// Empty means the default order (AIProviderConfig.managedOrder).
	AccountOrder []string
	// AgentModels is the model each agent currently uses, by provider id
	// (`magpie ls`): it answers first for that provider when the catalog
	// serves it, so Isoform thinks with what the user already chose.
	AgentModels map[string]string
}

// magpieAccountInfo is one account's entry from `magpie accounts --json`.
type magpieAccountInfo struct {
	On   bool   `json:"on"`
	Plan string `json:"plan,omitempty"`
	User string `json:"user,omitempty"`
}

// magpieBootstrapState is what the last successful Bootstrap applied.
type magpieBootstrapState struct {
	Groups         []magpieGroupPlan            `json:"groups"`
	Ollama         bool                         `json:"ollama"`
	ByokRegistered bool                         `json:"byokRegistered"`
	Subscriptions  bool                         `json:"subscriptions"`
	Accounts       map[string]magpieAccountInfo `json:"accounts,omitempty"`
	// External is true when the state describes the user's own magpie:
	// Groups are theirs and ActiveGroup is the one Isoform thinks with.
	External      bool              `json:"external,omitempty"`
	ActiveGroup   string            `json:"activeGroup,omitempty"`
	FallbackModel string            `json:"fallbackModel,omitempty"`
	AgentModels   map[string]string `json:"agentModels,omitempty"`
	AppliedAt     time.Time         `json:"appliedAt"`
}

// Isoform's own routing group. In either magpie (the user's or the bundled
// copy) Isoform thinks through a group it owns, so the user's groups and
// agent settings are never changed and the group shows up in their magpie
// under its own name. It is made from magpie's "Never stuck" shape: a copy
// of the user's never-stuck group when they have one, else the steady
// template (the strongest model of each signed-in provider, each behind the
// other), else the members Isoform would have planned itself. In the bundled
// copy its members then follow the user's assistant order (applyIsoformOrder);
// in either magpie Isoform may reconcile that group from its own config.
const (
	magpieIsoformGroupName = "Isoform"
	magpieIsoformGroupID   = "isoform"
	magpieNeverStuckGroup  = "never-stuck"
)

// ensureIsoformGroup creates the reserved ID without replacing another group
// that happens to have the same display name.
func (s *magpieSidecar) ensureIsoformGroup(ctx context.Context, groups []magpieGroupInfo, fallbackMembers []string) []magpieGroupInfo {
	for _, group := range groups {
		if group.ID == magpieIsoformGroupID {
			return groups
		}
	}
	members := fallbackMembers
	for _, group := range groups {
		if group.ID == magpieNeverStuckGroup && len(group.Members) > 0 {
			members = group.Members
			break
		}
	}
	if len(members) == 0 {
		return groups
	}
	args := []string{"group", "add", magpieIsoformGroupName, "id=" + magpieIsoformGroupID,
		"models=" + strings.Join(members, ","), "routing=order", "stays=session"}
	if _, err := s.runCLI(ctx, args...); err != nil {
		log.Printf("[magpie] Isoform group not created: %v", err)
		return groups
	}
	if refreshed := s.readGroups(ctx); hasGroup(refreshed, magpieIsoformGroupID) {
		return refreshed
	}
	return groups
}

// orderIsoformMembers puts Isoform's group in the user's assistant order
// (AIProviderConfig.managedOrder): each chosen subscription's members first,
// primary first, then every other member (the user's key, local models) as it
// was. A subscription the user left out is dropped. A chosen one the group has
// no member for takes its member from planned (the yansu-chat plan), so a
// backup that is turned back on rejoins. An empty result falls back to planned.
func orderIsoformMembers(current, planned, order []string) []string {
	providerOf := func(member string) string {
		provider, _, _ := strings.Cut(member, "/")
		return strings.ToLower(strings.TrimSpace(provider))
	}
	membersOf := func(members []string, provider string) []string {
		var out []string
		for _, member := range members {
			if providerOf(member) == provider {
				out = append(out, member)
			}
		}
		return out
	}
	var out []string
	for _, provider := range order {
		mine := membersOf(current, provider)
		if len(mine) == 0 {
			mine = membersOf(planned, provider)
		}
		out = append(out, mine...)
	}
	for _, member := range current {
		if !managedAccountProviderKnown(providerOf(member)) {
			out = append(out, member)
		}
	}
	if len(out) == 0 {
		return append([]string{}, planned...)
	}
	return out
}

// applyIsoformOrder rebuilds Isoform's group in the bundled copy when its
// members are not in the user's assistant order, and returns the refreshed
// listing. Chat routes through that group, so this is what makes the
// subscription order and Isoform's model and effort take effect. Only the
// bundled copy calls it: a magpie the user installed keeps its groups as the
// user set them. choice is Isoform's own pick from Isoform's config.
func (s *magpieSidecar) applyIsoformOrder(ctx context.Context, groups []magpieGroupInfo, planned, order []string, choice AgentModelChoice) []magpieGroupInfo {
	for _, group := range groups {
		if group.ID != magpieIsoformGroupID {
			continue
		}
		// The user's Settings choice for Isoform: its model first, every
		// member at its effort.
		want := applyIsoformChoice(orderIsoformMembers(group.Members, planned, order), choice)
		if len(want) == 0 || slices.Equal(want, group.Members) {
			return groups
		}
		profile := make([]AgentModelChoice, 0, len(want))
		for _, member := range want {
			base := memberBase(member)
			profile = append(profile, AgentModelChoice{Model: base, Effort: strings.TrimPrefix(member[len(base):], ":")})
		}
		refreshed, err := s.applyOwnedProfile(ctx, groups, profile)
		if err != nil {
			log.Printf("[magpie] Isoform group not reordered: %v", err)
			return groups
		}
		return refreshed
	}
	return groups
}

// hasGroup reports whether the listing has a group with id.
func hasGroup(groups []magpieGroupInfo, id string) bool {
	for _, group := range groups {
		if group.ID == id {
			return true
		}
	}
	return false
}

// magpieBootstrapOptions carries the App-owned inputs Bootstrap reads.
type magpieBootstrapOptions struct {
	// Provider is ai-provider.json. A BYOA key there is registered as yansu-byok.
	Provider        AIProviderConfig
	ConsentAccepted bool
	// OllamaBaseURL overrides the Ollama address (tests). Empty means the default.
	OllamaBaseURL string
	// Reload the profile after acquiring bootstrapMu so a concurrent Settings
	// save cannot be replaced by an older bootstrap snapshot.
	ReloadProfile bool
}

// planMagpieGroups returns the three groups in priority order. Only ids the
// catalog lists are kept, so a missing provider or signed-out subscription
// drops out cleanly. Subscription members join in every build;
// consent is informational (see below).
func planMagpieGroups(in magpieBootstrapInput) []magpieGroupPlan {
	have := make(map[string]bool, len(in.Catalog))
	for _, id := range in.Catalog {
		have[id] = true
	}
	// Subscriptions join whenever the build allows them; the consent note in
	// onboarding and Settings is informational. The model each agent
	// currently uses answers first for its provider when the catalog serves
	// it, so Isoform thinks with what the user already chose.
	var claudeChat, codexChat, claudeLight, codexLight string
	claudeChat = firstPresent(have, agentCatalogModel(in.AgentModels, "claude"), "claude/claude-sonnet-5-5", "claude/claude-sonnet-5", "claude/claude-sonnet-4-6")
	codexChat = firstPresent(have, agentCatalogModel(in.AgentModels, "codex"), "codex/gpt-5.6-sol")
	if codexChat == "" {
		codexChat = firstWithPrefix(in.Catalog, "codex/", "")
	}
	claudeLight = firstPresent(have, "claude/claude-haiku-5-5", "claude/claude-haiku-4-5")
	codexLight = firstWithPrefix(in.Catalog, "codex/", "-luna")
	if codexLight == "" {
		codexLight = firstWithPrefix(in.Catalog, "codex/", "")
	}

	lightByok := in.ByokLightModel
	if lightByok == "" {
		lightByok = in.ByokChatModel
	}
	var byokChat, byokLight string
	if in.ByokChatModel != "" {
		byokChat = firstPresent(have, magpieByokName+"/"+in.ByokChatModel)
	}
	if lightByok != "" {
		byokLight = firstPresent(have, magpieByokName+"/"+lightByok)
	}

	// Ollama contributes its first model only, and only when the catalog lists it.
	var ollama string
	if len(in.OllamaModels) > 0 && have["ollama/"+in.OllamaModels[0]] {
		ollama = "ollama/" + in.OllamaModels[0]
	}

	credits := hostedCreditsModel(have, true)
	// The user's assistant choice decides which subscriptions join and in
	// what order: the primary answers first, the others are backups.
	var subChat, subLight []string
	for _, provider := range normalizeManagedOrder(in.AccountOrder) {
		switch provider {
		case "claude":
			subChat = append(subChat, claudeChat)
			subLight = append(subLight, claudeLight)
		case "codex":
			subChat = append(subChat, codexChat)
			subLight = append(subLight, codexLight)
		}
	}
	chat := compactIDs(append(append([]string{}, subChat...), byokChat, ollama, credits)...)
	// Light work prefers the user's key, a local model and credits over the
	// subscriptions, so titles and classification never eat the plan.
	light := compactIDs(append([]string{byokLight, ollama, credits}, subLight...)...)
	return []magpieGroupPlan{
		{ID: "yansu-chat", Name: "yansu chat", Routing: "order", Stays: "session", Members: chat},
		{ID: "yansu-background", Name: "yansu background", Routing: "pace", Stays: "off", Members: append([]string{}, chat...)},
		{ID: "yansu-light", Name: "yansu light", Routing: "usage", Stays: "off", Members: light},
	}
}

// hostedCreditsModel is the dormant seam for a hosted "Isoform credits"
// provider, dropped from the 2026-10 rework (docs/hosted-credits-gateway.md
// keeps the contract for a later paid tier). It returns no member and must not
// invent one; a user with no subscription, key or local model sees the
// model-capacity card instead.
func hostedCreditsModel(have map[string]bool, subscriptions bool) string {
	_ = have
	_ = subscriptions
	return ""
}

// firstPresent returns the first candidate the catalog lists, or "".
func firstPresent(have map[string]bool, candidates ...string) string {
	for _, id := range candidates {
		if have[id] {
			return id
		}
	}
	return ""
}

// firstWithPrefix returns the first catalog id that starts with prefix and,
// when suffix is set, ends with it. Catalog order decides ties.
func firstWithPrefix(catalog []string, prefix, suffix string) string {
	for _, id := range catalog {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		if suffix != "" && !strings.HasSuffix(id, suffix) {
			continue
		}
		return id
	}
	return ""
}

func compactIDs(ids ...string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func catalogHasPrefix(catalog []string, prefix string) bool {
	return firstWithPrefix(catalog, prefix, "") != ""
}

// magpieByokArgs builds the `provider add` parameters for the user's own key.
// It reports ok=false when the config holds no anthropic or openai key with a
// chat model.
func magpieByokArgs(cfg AIProviderConfig) (args []string, chatModel, lightModel string, ok bool) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	key := strings.TrimSpace(cfg.APIKey)
	chatModel = strings.TrimSpace(cfg.Model)
	if (provider != "anthropic" && provider != "openai") || key == "" || chatModel == "" {
		return nil, "", "", false
	}
	lightModel = strings.TrimSpace(cfg.LightModel)
	if lightModel == "" {
		lightModel = chatModel
	}
	models := chatModel
	if lightModel != chatModel {
		models += "," + lightModel
	}

	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	var endpoint string
	if provider == "anthropic" {
		if base == "" {
			base = "https://api.anthropic.com"
		}
		endpoint = "anthropic=" + base
	} else {
		if base == "" {
			base = "https://api.openai.com"
		}
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		endpoint = "url=" + base
	}
	return []string{endpoint, "key=" + key, "models=" + models}, chatModel, lightModel, true
}

// detectOllamaModels lists local Ollama models. Anything but a quick 200
// means Ollama is absent, so it returns nil.
//
//isoform:destination local
func detectOllamaModels(ctx context.Context, baseURL string) []string {
	if baseURL == "" {
		baseURL = magpieOllamaBaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, magpieOllamaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/tags", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil
	}
	names := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		if name := strings.TrimSpace(m.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// Bootstrap reconciles the magpie instance with Yansu's configuration. It
// reconciles credentials even without a healthy sidecar. It is serialized,
// so a restart racing a settings change runs one after another.
func (s *magpieSidecar) Bootstrap(ctx context.Context, opts magpieBootstrapOptions) (magpieBootstrapState, error) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	if opts.ReloadProfile {
		opts.Provider.MagpieProfile = loadAIProviderConfig().MagpieProfile
	}

	if ctx == nil {
		ctx = context.Background()
	}
	var state magpieBootstrapState
	magpieByokMu.Lock()
	provider, err := reconcileMagpieByokFromConfig(s.stateDir)
	magpieByokMu.Unlock()
	if err != nil {
		return state, err
	}
	if !s.checkHealth(ctx) {
		return state, errMagpieNotHealthy
	}
	if s.External() {
		return s.bootstrapExternal(ctx, opts.Provider)
	}

	catalog, err := s.listModels(ctx)
	if err != nil {
		return state, err
	}

	ollamaModels := detectOllamaModels(ctx, opts.OllamaBaseURL)
	if len(ollamaModels) > 0 && !catalogHasPrefix(catalog, "ollama/") {
		if _, err := s.runCLI(ctx, "provider", "add", "ollama"); err != nil {
			log.Printf("[magpie] ollama provider not added: %v", err)
		} else {
			if catalog, err = s.listModels(ctx); err != nil {
				return state, err
			}
		}
	}

	_, byokChat, byokLight, registered := magpieByokArgs(provider)

	accounts := s.readAccountInfo(ctx)
	agentModels := s.readAgentModels(ctx)

	plans := planMagpieGroups(magpieBootstrapInput{
		Catalog:         catalog,
		OllamaModels:    ollamaModels,
		ByokChatModel:   byokChat,
		ByokLightModel:  byokLight,
		ConsentAccepted: opts.ConsentAccepted,
		AccountOrder:    opts.Provider.managedOrder(),
		AgentModels:     agentModels,
	})
	if err := s.applyGroupPlans(ctx, plans); err != nil {
		return state, err
	}
	// Chat uses Isoform's never-stuck group; the yansu groups retain the
	// separately paced background and cheap light routes.
	var chatMembers []string
	for _, plan := range plans {
		if plan.ID == "yansu-chat" {
			chatMembers = plan.Members
		}
	}
	// A model the user chose for Isoform makes the group even when nothing
	// else would.
	fallback := chatMembers
	if choice := opts.Provider.agentChoice(answeringIsoform); choice.Model != "" {
		fallback = applyIsoformChoice(chatMembers, choice)
	}
	groups := s.ensureIsoformGroup(ctx, s.readGroups(ctx), fallback)
	// The assistant order the user chose applies to Isoform's group too,
	// since chat routes there (magpieRouteTargets).
	if len(opts.Provider.MagpieProfile) > 0 {
		groups, err = s.applyOwnedProfile(ctx, groups, opts.Provider.MagpieProfile)
		if err != nil {
			return state, err
		}
	} else {
		groups = s.applyIsoformOrder(ctx, groups, chatMembers, opts.Provider.managedOrder(), opts.Provider.agentChoice(answeringIsoform))
	}
	for _, group := range groups {
		if group.ID == magpieIsoformGroupID {
			plans = append(plans, magpieGroupPlan{ID: group.ID, Name: group.Name, Routing: group.Routing, Members: group.Members})
		}
	}

	state = magpieBootstrapState{
		ActiveGroup:    activeGroupID(groups),
		Groups:         plans,
		Ollama:         catalogHasPrefix(catalog, "ollama/"),
		ByokRegistered: registered,
		Subscriptions:  catalogHasPrefix(catalog, "claude/") || catalogHasPrefix(catalog, "codex/"),
		Accounts:       accounts,
		AgentModels:    agentModels,
		AppliedAt:      time.Now(),
	}
	if err := s.saveBootstrapState(state); err != nil {
		log.Printf("[magpie] bootstrap state not saved: %v", err)
	}
	return state, nil
}

// bootstrapExternal observes the user's magpie and owns only the Isoform group.
func (s *magpieSidecar) bootstrapExternal(ctx context.Context, cfg AIProviderConfig) (magpieBootstrapState, error) {
	catalog, err := s.listModels(ctx)
	if err != nil {
		return magpieBootstrapState{}, err
	}
	agents := s.readAgentModels(ctx)
	groups := s.readGroups(ctx)
	if !hasGroup(groups, magpieIsoformGroupID) {
		fallback := []string{}
		for _, provider := range cfg.managedOrder() {
			if model := agentCatalogModel(agents, provider); slices.Contains(catalog, model) {
				fallback = append(fallback, model)
			}
		}
		if len(fallback) == 0 {
			if model := externalMagpieFallbackModel(agents, catalog); model != "" {
				fallback = append(fallback, model)
			}
		}
		groups = s.ensureIsoformGroup(ctx, groups, fallback)
	}
	if len(cfg.MagpieProfile) > 0 {
		groups, err = s.applyOwnedProfile(ctx, groups, cfg.MagpieProfile)
		if err != nil {
			return magpieBootstrapState{}, err
		}
	}
	active := activeGroupID(groups)
	if active == "" {
		active = pickActiveGroup(groups, agents)
	}
	// A previously created task group is usable only if it still matches the
	// current chat selection. Never repair it in the user's installation.
	for _, plan := range planExternalBackgroundGroups(groups, active, agents, catalog) {
		groups = slices.DeleteFunc(groups, func(group magpieGroupInfo) bool {
			return group.ID == plan.ID && (group.Name != plan.Name || group.Routing != plan.Routing || group.Stays != plan.Stays || !slices.Equal(group.Members, plan.Members))
		})
	}
	state := magpieBootstrapState{
		External:      true,
		Subscriptions: catalogHasPrefix(catalog, "claude/") || catalogHasPrefix(catalog, "codex/"),
		Accounts:      s.readAccountInfo(ctx),
		AgentModels:   agents,
		ActiveGroup:   active,
		FallbackModel: externalMagpieFallbackModel(agents, catalog),
		AppliedAt:     time.Now(),
	}
	for _, group := range groups {
		state.Groups = append(state.Groups, magpieGroupPlan{
			ID: group.ID, Name: group.Name, Routing: group.Routing, Stays: group.Stays, Members: group.Members,
		})
	}
	if err := s.saveBootstrapState(state); err != nil {
		log.Printf("[magpie] bootstrap state not saved: %v", err)
	}
	return state, nil
}

// With no usable group or agent setting, use a concrete model the user's
// gateway advertises rather than inventing a bundled group or model id.
func externalMagpieFallbackModel(agents map[string]string, catalog []string) string {
	for _, provider := range []string{"claude", "codex"} {
		model := agentCatalogModel(agents, provider)
		if model != "" && slices.Contains(catalog, model) {
			return model
		}
	}
	for _, model := range catalog {
		if model != "" && !strings.HasPrefix(model, "group/") {
			return model
		}
	}
	return ""
}

// Keep the user's chat group intact. Isoform owns separate groups for its
// unattended work, using only providers already selected by the chat route.
func planExternalBackgroundGroups(groups []magpieGroupInfo, active string, agents map[string]string, catalog []string) []magpieGroupPlan {
	var members []string
	byID := make(map[string]magpieGroupInfo, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}
	// A user group may contain another group. Resolve those aliases to
	// concrete models, so our task groups cannot become their own fallback.
	visited := map[string]bool{}
	seenMembers := map[string]bool{}
	var addMember func(string)
	addMember = func(member string) {
		member = strings.TrimSpace(member)
		if id, isGroup := strings.CutPrefix(member, "group/"); isGroup {
			if visited[id] || isIsoformTaskGroup(id) {
				return
			}
			visited[id] = true
			for _, nested := range byID[id].Members {
				addMember(nested)
			}
			return
		}
		if member != "" && !seenMembers[member] {
			members = append(members, member)
			seenMembers[member] = true
		}
	}
	if _, exists := byID[active]; exists && !isIsoformTaskGroup(active) {
		addMember("group/" + active)
	} else {
		for _, provider := range []string{"claude", "codex"} {
			if model := agentCatalogModel(agents, provider); model != "" {
				addMember(model)
			}
		}
	}
	have := make(map[string]bool, len(catalog))
	for _, model := range catalog {
		have[model] = true
	}
	var light []string
	seen := map[string]bool{}
	for _, member := range members {
		model := member
		provider, _, _ := strings.Cut(member, "/")
		switch provider {
		case "claude":
			model = firstPresent(have, "claude/claude-haiku-5-5", "claude/claude-haiku-4-5", member)
		case "codex":
			model = firstWithPrefix(catalog, "codex/", "-luna")
		}
		if model == "" {
			model = member
		}
		if !seen[model] {
			light = append(light, model)
			seen[model] = true
		}
	}
	return []magpieGroupPlan{
		{ID: "isoform-background", Name: "Isoform background", Routing: "pace", Stays: "off", Members: members},
		{ID: "isoform-light", Name: "Isoform light", Routing: "usage", Stays: "off", Members: light},
	}
}

// activeGroupID is Isoform's own group when the listing has it, else "".
func activeGroupID(groups []magpieGroupInfo) string {
	if hasGroup(groups, magpieIsoformGroupID) {
		return magpieIsoformGroupID
	}
	return ""
}

// pickActiveGroup is the fallback route when Isoform's group could not be
// made: the group Claude Code is set to when it exists, otherwise the first
// non-task group `magpie groups` lists.
func pickActiveGroup(groups []magpieGroupInfo, agents map[string]string) string {
	if model := agents["claude"]; strings.HasPrefix(model, "group/") {
		id := strings.TrimPrefix(model, "group/")
		for _, group := range groups {
			if group.ID == id && !isIsoformTaskGroup(id) {
				return id
			}
		}
	}
	for _, group := range groups {
		if !isIsoformTaskGroup(group.ID) {
			return group.ID
		}
	}
	return ""
}

func isIsoformTaskGroup(id string) bool {
	return id == "isoform-background" || id == "isoform-light"
}

// readGroups lists the instance's routing groups (`magpie groups`).
func (s *magpieSidecar) readGroups(ctx context.Context) []magpieGroupInfo {
	out, err := s.runCLI(ctx, "groups")
	if err != nil {
		log.Printf("[magpie] groups unavailable: %v", err)
		return nil
	}
	return parseMagpieGroups(string(out))
}

// readAgentModels reads the model each agent currently uses (`magpie ls`).
func (s *magpieSidecar) readAgentModels(ctx context.Context) map[string]string {
	out, err := s.runCLI(ctx, "ls")
	if err != nil {
		log.Printf("[magpie] agents unavailable: %v", err)
		return nil
	}
	return parseMagpieAgentModels(string(out))
}

// applyGroupPlans upserts groups with members and removes empty ones.
func (s *magpieSidecar) applyGroupPlans(ctx context.Context, plans []magpieGroupPlan) error {
	for _, plan := range plans {
		if len(plan.Members) == 0 {
			if _, err := s.runCLI(ctx, "group", "rm", plan.ID); err != nil {
				log.Printf("[magpie] group %s not removed: %v", plan.ID, err)
			}
			continue
		}
		args := []string{
			"group", "add", plan.Name,
			"models=" + strings.Join(plan.Members, ","),
			"routing=" + plan.Routing,
			"stays=" + plan.Stays,
		}
		if _, err := s.runCLI(ctx, args...); err != nil {
			return fmt.Errorf("apply group %s: %w", plan.ID, err)
		}
	}
	return nil
}

// listModels returns the ids GET /v1/models lists.
func (s *magpieSidecar) listModels(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	port := s.port
	s.mu.Unlock()
	if port == 0 {
		return nil, errMagpieNotHealthy
	}
	status, body, err := s.probe(ctx, port, "/v1/models")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("magpie /v1/models returned %d", status)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decode magpie models: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// readAccountInfo runs `magpie accounts --json`. Failure is not fatal: the
// status then shows quota without on/plan detail.
func (s *magpieSidecar) readAccountInfo(ctx context.Context) map[string]magpieAccountInfo {
	out, err := s.runCLI(ctx, "accounts", "--json")
	if err != nil {
		log.Printf("[magpie] accounts unavailable: %v", err)
		return nil
	}
	var rows []struct {
		Agent string `json:"agent"`
		User  string `json:"user"`
		Plan  string `json:"plan"`
		On    bool   `json:"on"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		log.Printf("[magpie] accounts output not parsed: %v", err)
		return nil
	}
	info := make(map[string]magpieAccountInfo, len(rows))
	for _, row := range rows {
		agent := strings.ToLower(strings.TrimSpace(row.Agent))
		if agent == "" {
			continue
		}
		info[agent] = magpieAccountInfo{On: row.On, Plan: row.Plan, User: row.User}
	}
	return info
}

func (s *magpieSidecar) bootstrapStatePath() string {
	return filepath.Join(s.stateDir, magpieBootstrapFileName)
}

func (s *magpieSidecar) saveBootstrapState(state magpieBootstrapState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.bootstrapStatePath(), append(data, '\n'), 0o600)
}

// loadMagpieConsent reads consent.json. ok is false when none exists or it
// cannot be parsed.
func loadMagpieConsent(stateDir string) (MagpieConsent, bool) {
	if stateDir == "" {
		return MagpieConsent{}, false
	}
	data, err := os.ReadFile(filepath.Join(stateDir, magpieConsentFileName))
	if err != nil {
		return MagpieConsent{}, false
	}
	var consent MagpieConsent
	if err := json.Unmarshal(data, &consent); err != nil {
		return MagpieConsent{}, false
	}
	return consent, true
}

// magpieConsentAccepted reports whether consent covers the current version.
func magpieConsentAccepted(stateDir string) bool {
	consent, ok := loadMagpieConsent(stateDir)
	return ok && consent.Version == magpieConsentVersion
}

// saveMagpieConsent writes consent.json with mode 0600.
func saveMagpieConsent(stateDir string, consent MagpieConsent) error {
	if stateDir == "" {
		return errMagpieStateDir
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(consent, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, magpieConsentFileName), append(data, '\n'), 0o600)
}

// clearMagpieConsent removes consent.json. A missing file is not an error.
func clearMagpieConsent(stateDir string) error {
	if stateDir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(stateDir, magpieConsentFileName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
