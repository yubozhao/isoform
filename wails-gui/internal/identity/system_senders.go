package identity

import "strings"

// systemSenderNames are accounts chat platforms and workplace tools post
// as. They appear in a chat's sender field like a person but are the
// platform itself, an integration or an assistant, so they must not become
// people on the Who you know page. Names a person could plausibly carry
// (Claude, Devin, Polly) are deliberately absent: a missed bot costs one
// row the person can remove, a dropped colleague is silent.
var systemSenderNames = map[string]struct{}{
	// Platform accounts.
	"slackbot":              {},
	"workflow builder":      {},
	"wechat team":           {},
	"微信团队":                  {},
	"file transfer":         {},
	"文件传输助手":                {},
	"wechat pay":            {},
	"微信支付":                  {},
	"service notifications": {},
	"服务通知":                  {},
	"telegram":              {},
	"discord":               {},
	"feishu assistant":      {},
	"飞书助手":                  {},
	"lark assistant":        {},
	"microsoft teams":       {},
	"google calendar":       {},
	"google drive":          {},
	"outlook calendar":      {},
	// Integrations that post into channels.
	"github":         {},
	"gitlab":         {},
	"jira":           {},
	"jira cloud":     {},
	"confluence":     {},
	"linear":         {},
	"notion":         {},
	"asana":          {},
	"trello":         {},
	"zapier":         {},
	"sentry":         {},
	"datadog":        {},
	"pagerduty":      {},
	"vercel":         {},
	"circleci":       {},
	"figma":          {},
	"loom":           {},
	"zoom":           {},
	"giphy":          {},
	"chatgpt":        {},
	"copilot":        {},
	"github copilot": {},
	"isoform":        {},
	"yansu":          {},
}

// IsSystemSender reports whether a normalized sender name is a platform,
// integration or assistant account rather than a person.
func IsSystemSender(name string) bool {
	key := strings.ToLower(CollapseNameSpace(name))
	if key == "" {
		return false
	}
	_, ok := systemSenderNames[key]
	return ok
}
