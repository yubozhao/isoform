package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"wails-gui/internal/destinations"
	"wails-gui/storage"
)

type WeeklyLeaves struct {
	Incomplete bool `json:"incomplete,omitempty"`
	ThisWeek   *int `json:"thisWeek,omitempty"`
}
type SearchLeaves struct {
	WeeklyLeaves
	Service string `json:"service"`
}
type EmailLeaves struct {
	WeeklyLeaves
	On            bool   `json:"on"`
	AddressMasked string `json:"addressMasked,omitempty"`
}
type TextLeaves struct {
	WeeklyLeaves
	On      bool `json:"on"`
	NamesOn bool `json:"namesOn"`
}
type ChatLeaves struct {
	WeeklyLeaves
	Apps []string `json:"apps"`
}
type AgentLeaves struct {
	WeeklyLeaves
	Agent string `json:"agent"`
}
type AgentsAskLeaves struct {
	WeeklyLeaves
	ByAgent *[]AgentLeaves `json:"byAgent,omitempty"`
}
type UsageLeaves struct {
	On bool `json:"on"`
}
type YourDataLeaves struct {
	WebSearches *SearchLeaves    `json:"webSearches,omitempty"`
	Email       *EmailLeaves     `json:"email,omitempty"`
	Texts       *TextLeaves      `json:"texts,omitempty"`
	ChatApps    *ChatLeaves      `json:"chatApps,omitempty"`
	AgentsAsk   *AgentsAskLeaves `json:"agentsAsk,omitempty"`
	UsageCounts UsageLeaves      `json:"usageCounts"`
}

type leavesPaths struct {
	search, email, texts, names, agents, usage bool
	lastService, masked                        string
	apps                                       []string
}

func (p leavesPaths) summary(rows []storage.DestinationCount, readErr error) YourDataLeaves {
	counts := make(map[destinations.ID]int)
	lost := make(map[destinations.ID]bool)
	agents := []AgentLeaves{}
	service := p.lastService
	for _, row := range rows {
		if row.Incomplete {
			lost[row.ID] = true
			continue
		}
		if readErr != nil {
			continue
		}
		counts[row.ID] += row.Calls
		switch row.ID {
		case destinations.AssistantSearch:
			service = destinations.SearchService(row.Detail)
		case destinations.AgentsMCPRead:
			agents = append(agents, AgentLeaves{WeeklyLeaves{ThisWeek: &row.Calls}, row.Detail})
		}
	}
	slices.SortFunc(agents, func(a, b AgentLeaves) int { return strings.Compare(a.Agent, b.Agent) })
	week := func(n int, incomplete bool) WeeklyLeaves {
		if readErr != nil {
			return WeeklyLeaves{Incomplete: incomplete}
		}
		return WeeklyLeaves{ThisWeek: &n, Incomplete: incomplete}
	}
	out := YourDataLeaves{UsageCounts: UsageLeaves{p.usage}}
	if n := counts[destinations.AssistantSearch]; p.search || n > 0 || lost[destinations.AssistantSearch] {
		out.WebSearches = &SearchLeaves{week(n, lost[destinations.AssistantSearch]), service}
	}
	if n := counts[destinations.Email]; p.email || n > 0 || lost[destinations.Email] {
		out.Email = &EmailLeaves{week(n, lost[destinations.Email]), p.email, p.masked}
	}
	textsIncomplete := lost[destinations.TextRelay] || lost[destinations.TextMessages]
	if n := counts[destinations.TextRelay] + counts[destinations.TextMessages]; p.texts || n > 0 || textsIncomplete {
		out.Texts = &TextLeaves{week(n, textsIncomplete), p.texts, p.names}
	}
	if len(p.apps) > 0 {
		out.ChatApps = &ChatLeaves{week(counts[destinations.ChatBridges], lost[destinations.ChatBridges]), p.apps}
	}
	if n := counts[destinations.AgentsMCPRead]; p.agents || n > 0 || lost[destinations.AgentsMCPRead] {
		out.AgentsAsk = &AgentsAskLeaves{WeeklyLeaves: week(n, lost[destinations.AgentsMCPRead])}
		if readErr == nil {
			for i := range agents {
				agents[i].Incomplete = out.AgentsAsk.Incomplete
			}
			out.AgentsAsk.ByAgent = &agents
		}
	}
	return out
}

func (a *App) yourDataLeaves() (YourDataLeaves, error) {
	dir, err := topicsStateDir()
	if err != nil {
		return YourDataLeaves{}, err
	}
	var topicSwitch struct{ Enabled *bool }
	raw, err := os.ReadFile(filepath.Join(dir, topicsStateFileName))
	if err == nil {
		err = json.Unmarshal(raw, &topicSwitch)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return YourDataLeaves{}, err
	}
	p := leavesPaths{usage: telemetrySharingOn(), search: topicSwitch.Enabled == nil || *topicSwitch.Enabled}
	channelDir, err := textChannelDir()
	if err != nil {
		return YourDataLeaves{}, err
	}
	ctx := context.Background()
	var rows []storage.DestinationCount
	var readErr error
	if a.store != nil {
		countCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		rows, readErr = a.store.WeeklyDestinationCounts(countCtx, time.Now())
		cancel()
		if !p.search {
			jobs, err := a.store.ListCronJobs(ctx)
			if err != nil {
				return YourDataLeaves{}, err
			}
			for _, job := range jobs {
				if job.Enabled && job.Name == todayNewsJobName && job.SurfaceKind == todayNewsSurfaceKind {
					p.search = true
					break
				}
			}
		}
		if p.search && readErr == nil {
			var err error
			p.lastService, _, err = a.store.LookupUIState(ctx, storage.LastSearchServiceKey)
			if err != nil {
				return YourDataLeaves{}, err
			}
		}
	} else {
		readErr = errors.New("store unavailable")
	}
	if readErr != nil {
		log.Print("[destinations] weekly counts unavailable")
	}
	if featureOn(featurePlaybooks) && a.playbooksReady() {
		file, err := newEmailChannel(channelDir).read()
		if err != nil {
			return YourDataLeaves{}, err
		}
		if file.State == emailStateActive && file.Address != "" {
			plans, err := a.playbookPlans.List()
			if err != nil {
				return YourDataLeaves{}, err
			}
			for _, plan := range plans {
				if !plan.Removed && slices.Contains(plan.Channels, "email") && (plan.Schedule == nil || plan.Schedule.Enabled) {
					p.email = true
					if local, domain, ok := strings.Cut(normalizeEmailAddress(file.Address), "@"); ok && local != "" {
						p.masked = string([]rune(local)[0]) + "***@" + domain
					}
					break
				}
			}
		}
	}
	sms, err := newSMSChannel(channelDir).read()
	if err != nil {
		return YourDataLeaves{}, err
	}
	cfg, err := loadTextChannelConfig(channelDir)
	if err != nil {
		return YourDataLeaves{}, err
	}
	p.texts = (sms.State == smsStateActive && sms.Number != "") || (cfg.Enabled && !cfg.VerifiedAt.IsZero())
	p.names = cfg.Enabled && cfg.IncludeNames
	apps, err := connectedLeavesApps()
	if err != nil {
		return YourDataLeaves{}, err
	}
	p.apps = apps
	for _, client := range []string{"claude-code", "codex"} {
		if _, err := readMCPReadToken(client); err == nil {
			p.agents = true
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return YourDataLeaves{}, err
		}
	}
	return p.summary(rows, readErr), nil
}

// Read saved connection state without starting a bot, probing an assistant,
// or refreshing credentials over the network.
func connectedLeavesApps() ([]string, error) {
	var apps []string
	if creds, err := readTelegramLocalCredentials(); err != nil {
		return nil, err
	} else if creds != nil {
		apps = append(apps, "telegram")
	}
	if creds, err := readSlackLocalCredentials(); err != nil {
		return nil, err
	} else if creds != nil {
		apps = append(apps, "slack")
	}
	if creds, err := readDiscordLocalCredentials(); err != nil {
		return nil, err
	} else if creds != nil {
		apps = append(apps, "discord")
	}
	if creds, err := readFeishuLocalCredentials("feishu"); err != nil {
		return nil, err
	} else if creds != nil {
		apps = append(apps, "feishu")
	}
	if creds, err := readFeishuLocalCredentials("lark"); err != nil {
		return nil, err
	} else if creds != nil {
		apps = append(apps, "lark")
	}
	if creds, err := readWeChatCredentials(); err != nil {
		return nil, err
	} else if creds != nil {
		apps = append(apps, "wechat")
	}
	return apps, nil
}

func readLocalBotCredentials[T any](dir string, dirErr error) (*T, error) {
	if dirErr != nil {
		return nil, dirErr
	}
	raw, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var creds T
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}
