package main

import (
	"net/url"
	"strings"
)

// The app menu and the Help menu (design conformance answers v1.0.0, S3 and
// A5): the app menu shows Restart to Update once an update is downloaded, in
// place of Check for updates; the Help menu holds Keyboard Shortcuts, Isoform
// Help and Send Feedback…, which the sidebar's profile pop-up held before.
type appMenuCopy struct {
	About, Check, Restart, Settings         string
	Help, Shortcuts, IsoformHelp            string
	Feedback, FeedbackSubject, FeedbackBody string
}

// The menu actions, as tags on the native items.
const (
	appMenuAbout = iota
	appMenuCheck
	appMenuSettings
	appMenuRestart
	appMenuShortcuts
	appMenuIsoformHelp
	appMenuFeedback
)

// appMenuSupportAddress is Send feedback's address, as Settings › About has it
// (help-about-page.tsx SUPPORT_EMAIL).
const appMenuSupportAddress = "bo@isoform.ai"

func appMenuWords(language string) appMenuCopy {
	switch normalizeUILanguage(language) {
	case "zh-CN":
		return appMenuCopy{
			About: "关于 Isoform", Check: "检查更新…", Restart: "重新启动以更新", Settings: "设置…",
			Help: "帮助", Shortcuts: "键盘快捷键", IsoformHelp: "Isoform 帮助", Feedback: "发送反馈…",
			FeedbackSubject: "Isoform 使用反馈", FeedbackBody: "Isoform 版本：{{version}}\n\n发生了什么，或你希望改进什么？\n\n",
		}
	default:
		return appMenuCopy{
			About: "About Isoform", Check: "Check for updates…", Restart: "Restart to Update", Settings: "Settings…",
			Help: "Help", Shortcuts: "Keyboard Shortcuts", IsoformHelp: "Isoform Help", Feedback: "Send Feedback…",
			FeedbackSubject: "Isoform feedback", FeedbackBody: "Isoform version: {{version}}\n\nWhat happened, or what would you like to improve?\n\n",
		}
	}
}

// appMenuTitles is every title the two menus show, for the locale check.
func appMenuTitles(language string) []string {
	w := appMenuWords(language)
	return []string{w.About, w.Check, w.Restart, w.Settings, w.Help, w.Shortcuts, w.IsoformHelp, w.Feedback}
}

// appMenuFeedbackURL is the mail draft Send Feedback… opens: the same address,
// subject and body as Settings › About's Send feedback.
func appMenuFeedbackURL(language, version string) string {
	w := appMenuWords(language)
	escape := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	body := strings.ReplaceAll(w.FeedbackBody, "{{version}}", version)
	return "mailto:" + appMenuSupportAddress + "?subject=" + escape(w.FeedbackSubject) + "&body=" + escape(body)
}

// appMenuActions is what each menu item does; the native menu calls it.
type appMenuActions struct {
	open     func(topic string)
	check    func()
	restart  func()
	feedback func()
}

// dispatchAppMenuAction shares the native menu's navigation contract with tests.
func dispatchAppMenuAction(action int, do appMenuActions) {
	switch action {
	case appMenuAbout:
		do.open("about")
	case appMenuCheck:
		do.open("about")
		do.check()
	case appMenuSettings:
		do.open("")
	case appMenuRestart:
		do.restart()
	case appMenuShortcuts:
		// The keyboard shortcuts topic opens the ⌘/ sheet (computer-keys v1.0.0 rule 5).
		do.open("keybindings")
	case appMenuIsoformHelp:
		// Settings › About holds Help and Send feedback (design A5).
		do.open("about")
	case appMenuFeedback:
		do.feedback()
	}
}

// appMenuUpdateReady is whether a downloaded update waits for a restart, so
// the app menu shows Restart to Update.
func (a *App) appMenuUpdateReady() bool {
	return a.GetUpdateStatus().Status == "update-downloaded"
}
