package main

import (
	"context"
	"strings"

	"yansu-agent/agent/authority"

	"wails-gui/storage"
)

// The app's own research schedules (Daily News and "New on your topics")
// search the web with opencli. They run unattended, and the native runner
// refuses every opencli call that no grant covers, so without these grants
// both answered "blocked by authority policy" every day. The grants are the
// narrowest that let them work: a plain "ask" to one of the AI search
// adapters the prompts name, in an ephemeral site session, plus the help and
// list calls that find the right command. Every other subcommand (history,
// delete, send, image...), a compound command, a variable, a substitution, a
// redirect and any other adapter stays refused, and so does WebFetch: a
// fetched page or a poisoned answer could otherwise send prompt text to any
// host in a query string. The coarse external-write deny list stays on.

// appResearchAdapters are the opencli adapters the research prompts may use.
var appResearchAdapters = []string{"grok", "gemini", "doubao"}

type appResearchGrantsKey struct{}

// isAppResearchJob reports the app's own research schedules.
func isAppResearchJob(job *storage.CronJobRow) bool {
	if job == nil {
		return false
	}
	if job.SurfaceKind == openAnswerSurfaceKind && strings.HasPrefix(job.Name, openAnswerJobPrefix) {
		return true
	}
	if job.SurfaceKind != todayNewsSurfaceKind {
		return false
	}
	switch job.Name {
	case todayNewsJobName, topicFindsJobName:
		return true
	default:
		return false
	}
}

// isTopicSearchJob reports whether job is a topic or Daily News search:
// what it sends is built from what the person has been looking at, so it
// never runs under Closed Eyes (topicSearchMayLeave).
func isTopicSearchJob(job *storage.CronJobRow) bool {
	if job == nil || job.SurfaceKind != todayNewsSurfaceKind {
		return false
	}
	switch job.Name {
	case todayNewsJobName, topicFindsJobName:
		return true
	default:
		return false
	}
}

// appResearchAuthorityGrants returns the research grants for a job, or nil.
// Research runs always route to the native loop (they deny external writes),
// so a user whose assistant is Claude Code or Codex needs the grants too; a
// signed-in Claude Code takes the run before it gets here (research_cli.go).
func appResearchAuthorityGrants(job *storage.CronJobRow) []authority.Grant {
	if !isAppResearchJob(job) {
		return nil
	}
	prefix := "app-research:" + job.ID
	grants := []authority.Grant{{ID: prefix + ":opencli-list", Capability: "connector.command.execute", ResourcePrefix: "opencli:list/"}}
	for _, adapter := range appResearchAdapters {
		for _, sub := range []string{"ask", "help"} {
			grants = append(grants, authority.Grant{
				ID:             prefix + ":opencli-" + adapter + "-" + sub,
				Capability:     "connector.command.execute",
				ResourcePrefix: "opencli:" + adapter + ":" + sub + "/",
			})
		}
	}
	return grants
}

func withAppResearchGrants(ctx context.Context, grants []authority.Grant) context.Context {
	if len(grants) == 0 {
		return ctx
	}
	return context.WithValue(ctx, appResearchGrantsKey{}, grants)
}

func appResearchGrantsFrom(ctx context.Context) []authority.Grant {
	grants, _ := ctx.Value(appResearchGrantsKey{}).([]authority.Grant)
	return grants
}
