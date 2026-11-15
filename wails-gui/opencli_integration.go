package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ── Types ────────────────────────────────────────────────────────────

// openCLIStrategy represents an OpenCLI authentication strategy.
type openCLIStrategy string

const (
	openCLIStrategyPublic    openCLIStrategy = "public"
	openCLIStrategyCookie    openCLIStrategy = "cookie"
	openCLIStrategyHeader    openCLIStrategy = "header"
	openCLIStrategyIntercept openCLIStrategy = "intercept"
	openCLIStrategyUI        openCLIStrategy = "ui"
)

// openCLICommandArg describes a single argument for an OpenCLI command.
type openCLICommandArg struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Required   bool     `json:"required"`
	Positional bool     `json:"positional"`
	Choices    []string `json:"choices"`
	Default    any      `json:"default"`
	Help       string   `json:"help"`
}

// openCLICommand describes a single command available for a site.
type openCLICommand struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Strategy    openCLIStrategy     `json:"strategy"`
	Args        []openCLICommandArg `json:"args"`
	Columns     []string            `json:"columns"`
	Domain      string              `json:"domain"`
	Browser     bool                `json:"browser"`
}

// openCLIRawCommand is the flat JSON entry from `opencli list -f json`.
type openCLIRawCommand struct {
	Command     string              `json:"command"`
	Site        string              `json:"site"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Strategy    openCLIStrategy     `json:"strategy"`
	Browser     bool                `json:"browser"`
	Args        []openCLICommandArg `json:"args"`
	Columns     []string            `json:"columns"`
	Domain      string              `json:"domain"`
}

// openCLISite groups all commands for one website/app.
type openCLISite struct {
	Site     string           `json:"site"`
	Commands []openCLICommand `json:"commands"`
	Category string           `json:"category"`
	Domain   string           `json:"domain"`
}

// openCLICapabilities holds the full result of detecting OpenCLI on the system.
type openCLICapabilities struct {
	Version            string        `json:"version"`
	Sites              []openCLISite `json:"sites"`
	Available          bool          `json:"available"`
	ChromeExtInstalled bool          `json:"chromeExtInstalled"` // true if COOKIE/HEADER strategies can work
}

// resolvedDataSource tracks which data source to use for a feature.
type resolvedDataSource struct {
	FeatureName string          // the feature this source serves
	SourceType  string          // "opencli_builtin" | "opencli_generated" | "computer_use" | "unavailable"
	Site        string          // OpenCLI site name or app name for computer use
	Commands    []string        // relevant command names
	Strategy    openCLIStrategy // auth strategy
	Note        string          // stability or provenance note
}

// ── Detection & Caching ─────────────────────────────────────────────

const opencliCacheTTL = 10 * time.Minute

var (
	opencliCache     *openCLICapabilities
	opencliCacheMu   sync.RWMutex
	opencliCacheTime time.Time
)

// ── Auto-Generation ─────────────────────────────────────────────────

const opencliGenerateTimeout = 2 * time.Minute

// ── Prompt Building ─────────────────────────────────────────────────

// ── Data Source Resolution (Phase 2.5) ──────────────────────────────

// siteCommandNames returns the command names for a site as a string slice.
func siteCommandNames(site *openCLISite) []string {
	cmds := make([]string, 0, len(site.Commands))
	for _, c := range site.Commands {
		cmds = append(cmds, c.Name)
	}
	return cmds
}

// siteDefaultStrategy returns the first command's strategy as the site default.
func siteDefaultStrategy(site *openCLISite) openCLIStrategy {
	if len(site.Commands) > 0 {
		return site.Commands[0].Strategy
	}
	return openCLIStrategyPublic
}

// maxAdapterDocLength caps each adapter doc to avoid bloating the prompt.
const maxAdapterDocLength = 3000

// buildAdapterDocsSection returns a prompt section with cached adapter documentation
// for the resolved OpenCLI sources. Includes usage examples, output format notes,
// and authentication tips from the official adapter docs.
func buildAdapterDocsSection(sources []resolvedDataSource) string {
	var b strings.Builder
	included := 0

	for _, s := range sources {
		doc := LoadAdapterDoc(s.Site)
		if doc == "" {
			continue
		}
		if included == 0 {
			b.WriteString("\nADAPTER DOCUMENTATION (from OpenCLI docs):\n\n")
		}
		// Truncate very long docs to avoid blowing up the prompt.
		if len(doc) > maxAdapterDocLength {
			doc = doc[:maxAdapterDocLength] + "\n... (truncated)"
		}
		fmt.Fprintf(&b, "--- %s ---\n%s\n\n", s.Site, doc)
		included++
	}

	return b.String()
}

// deduplicateSources returns sources with unique sites only (first occurrence wins).
func deduplicateSources(sources []resolvedDataSource) []resolvedDataSource {
	seen := make(map[string]bool)
	var result []resolvedDataSource
	for _, s := range sources {
		if seen[s.Site] {
			continue
		}
		seen[s.Site] = true
		result = append(result, s)
	}
	return result
}

// parseOpenCLIListOutput tries multiple JSON shapes to be tolerant of
// schema changes across OpenCLI versions. Returns sites (possibly empty)
// but never fails — a partial parse is better than no OpenCLI support.
func parseOpenCLIListOutput(output []byte) []openCLISite {
	// 1. Try flat command array (current format).
	var rawCommands []openCLIRawCommand
	if err := json.Unmarshal(output, &rawCommands); err == nil && len(rawCommands) > 0 {
		return groupCommandsBySite(rawCommands)
	}

	// 2. Try already-grouped site array (possible alternate format).
	var sites []openCLISite
	if err := json.Unmarshal(output, &sites); err == nil && len(sites) > 0 {
		return sites
	}

	// 3. Could not parse — log and return empty (OpenCLI still marked available
	// so other features like `opencli generate` keep working).
	log.Printf("[opencli] could not parse list output (len=%d), continuing with empty site list", len(output))
	return nil
}

// groupCommandsBySite groups flat opencli list output into site-grouped structure.
func groupCommandsBySite(rawCommands []openCLIRawCommand) []openCLISite {
	siteMap := make(map[string]*openCLISite)
	var siteOrder []string

	for _, raw := range rawCommands {
		if raw.Site == "" || raw.Site == "_shared" {
			continue
		}
		site, exists := siteMap[raw.Site]
		if !exists {
			site = &openCLISite{
				Site:   raw.Site,
				Domain: raw.Domain,
			}
			siteMap[raw.Site] = site
			siteOrder = append(siteOrder, raw.Site)
		}
		site.Commands = append(site.Commands, openCLICommand{
			Name:        raw.Name,
			Description: raw.Description,
			Strategy:    raw.Strategy,
			Args:        raw.Args,
			Columns:     raw.Columns,
			Domain:      raw.Domain,
			Browser:     raw.Browser,
		})
	}

	sites := make([]openCLISite, 0, len(siteOrder))
	for _, name := range siteOrder {
		sites = append(sites, *siteMap[name])
	}
	return sites
}

// formatCommandSignature formats a single command's args for the generation prompt.
func formatCommandSignature(site string, cmd openCLICommand) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  opencli %s %s", site, cmd.Name)

	for _, arg := range cmd.Args {
		if arg.Positional {
			if arg.Required {
				fmt.Fprintf(&b, " <%s>", arg.Name)
			} else {
				fmt.Fprintf(&b, " [%s]", arg.Name)
			}
		}
	}
	b.WriteString("\n")

	if cmd.Description != "" {
		fmt.Fprintf(&b, "    %s\n", cmd.Description)
	}

	for _, arg := range cmd.Args {
		if arg.Positional {
			continue
		}
		required := ""
		if arg.Required {
			required = " (required)"
		}
		defVal := ""
		if arg.Default != nil {
			defVal = fmt.Sprintf(", default: %v", arg.Default)
		}
		choices := ""
		if len(arg.Choices) > 0 {
			choices = fmt.Sprintf(", choices: %s", strings.Join(arg.Choices, "|"))
		}
		fmt.Fprintf(&b, "    --%s <%s>%s%s%s", arg.Name, arg.Type, required, defVal, choices)
		if arg.Help != "" {
			fmt.Fprintf(&b, " — %s", arg.Help)
		}
		b.WriteString("\n")
	}

	if len(cmd.Columns) > 0 {
		fmt.Fprintf(&b, "    Output columns: %s\n", strings.Join(cmd.Columns, ", "))
	}

	return b.String()
}
