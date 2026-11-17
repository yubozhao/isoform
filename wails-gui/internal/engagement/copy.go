package engagement

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// CopySet is the text for one kind in one language. PushBody is the generic
// line a push carries in the clear; Sealed is the template the phone shows
// after decrypting the notice, with {param} placeholders filled from
// Moment.Params; Text is the text message template (notifications design
// section 7a), naming the reply codes TextReplies lists; Actions maps action
// ids to button labels.
type CopySet struct {
	PushBody string
	Sealed   string
	// Phone is the sealed title a phone that reads Your work shows in place
	// of PushBody, when the kind has one (phone-agent v1.2.0 rule 10).
	Phone string
	Text  string
	// EmailSubject and EmailBody are the email templates, only for kinds the
	// user can have emailed (a playbook's result).
	EmailSubject string
	EmailBody    string
	Actions      map[string]string
}

// Languages lists the catalog languages; "en" is the source of truth and
// every kind must exist in all of them.
var Languages = []string{"en", "zh-CN"}

var copyCatalog = map[string]map[Kind]CopySet{
	"en": {
		KindFirstWeek:   {PushBody: "Your first week with Isoform", Sealed: "Open the chat for your first-week check-in.", Text: "Isoform: your first-week check-in is in the chat on your computer or phone.", Actions: map[string]string{"open": "Read it"}},
		KindWeeklyRecap: {PushBody: "Your week is ready.", Sealed: "A short look back at your week, and one idea for Monday. Open the brief.", Text: "Isoform: your week is ready in the brief on your computer or phone.", Actions: map[string]string{"open": "Read your week"}},
		KindDecisionDue: {
			PushBody: "Work needs your permission.",
			Sealed:   "{title} needs your permission before continuing.",
			Text:     "Isoform: work needs your permission. Open the chat to review the request.",
			Actions:  map[string]string{"approve": "Allow", "decline": "Not now"},
		},
		KindCommitmentSlipping: {
			PushBody: "A commitment needs attention.",
			Sealed:   "{title} is due {due}, and I have no record of progress on it.",
			Text:     "Isoform: \"{title}\" is due {due}, and I have no record of progress on it. Open Isoform to review it.",
			Actions:  map[string]string{"open": "Find time"},
		},
		KindMeetingEnded: {
			PushBody: "Your meeting notes are ready.",
			Sealed:   "{title}: {decisions} decisions, {followups} follow-ups.",
			Text:     "Isoform: notes for \"{title}\" are ready: {decisions} decisions, {followups} follow-ups.",
			Actions:  map[string]string{"open_notes": "Open notes", "later": "Later"},
		},
		KindPersonReappeared: {
			PushBody: "Someone you know came up.",
			Sealed:   "{name} came up in {where}. Want a note?",
			Text:     "Isoform: {name} came up in {where}. Reply X to stop for this person.",
			Actions:  map[string]string{"open_person": "Show person", "snooze": "Snooze", "stop_person": "Stop for this person"},
		},
		KindGoalCheckIn: {
			PushBody: "A quick check on a goal.",
			Sealed:   "You set \"{title}\". How is it going?",
			Text:     "Isoform: how is \"{title}\" going? Reply with a sentence, or Z to snooze.",
			Actions:  map[string]string{"update": "Update", "snooze": "Snooze", "dismiss": "Not now"},
		},
		KindWorkFinished: {
			PushBody: "Work finished.",
			Sealed:   "{title} is ready to review. Nothing was sent.",
			Phone:    "Back from {agent}",
			Text:     "Isoform: \"{title}\" is ready to review. Nothing was sent.",
			Actions:  map[string]string{"review": "Review"},
		},
		KindWorkBlocked: {
			PushBody: "Work is waiting on you.",
			Sealed:   "{title} needs {question}",
			Text:     "Isoform: \"{title}\" needs {question} Reply A to answer in chat, L for later.",
			Actions:  map[string]string{"answer": "Answer", "stop": "Stop", "later": "Later"},
		},
		KindBriefReady: {
			PushBody: "Your brief is ready.",
			Sealed:   "Your {slot} brief is ready.",
			Text:     "Isoform: your {slot} brief is ready on your computer.",
			Actions:  map[string]string{"open": "Play"},
		},
		KindBriefMissed: {
			PushBody: "Your brief didn't come.",
			Sealed:   "Your {slot} brief was missed. Open Today to see when the next one runs.",
			Text:     "Isoform: your {slot} brief was missed. Open Today on your computer for the next one.",
			Actions:  map[string]string{"make_it_now": "Make it now", "open": "Open Today"},
		},
		KindScheduleMissed: {
			PushBody: "A schedule did not run.",
			Sealed:   "\"{title}\" didn't run twice in a row. Open Upcoming to see why.",
			Text:     "Isoform: \"{title}\" didn't run twice in a row. Open Upcoming on your computer to see why.",
			Actions:  map[string]string{"run_now": "Run it now", "open": "Show why"},
		},
		KindSuggestion: {
			PushBody: "Isoform has a suggestion.",
			Sealed:   "You did \"{title}\" {count} times. Want Isoform to take it on?",
			Text:     "Isoform: you did \"{title}\" {count} times. Reply N to dismiss, L for later.",
			Actions:  map[string]string{"see": "See it", "add_to_upcoming": "Add to Upcoming", "do_once": "Do it", "not_now": "Not now", "dismiss": "Not now"},
		},
		KindTopicFinds: {
			PushBody: "Something new on a topic you follow.",
			Sealed:   "New on {topic}: {title}",
			Text:     "Isoform: new on {topic}: {title}. More on Today on your computer.",
			Actions:  map[string]string{"open": "Show what's new", "not_now": "Not now"},
		},
		KindLearnedToConfirm: {
			PushBody: "Isoform learned something.",
			Sealed:   "{statement} Keep this?",
			Text:     "Isoform: I learned \"{statement}\". Reply K to keep, C to correct.",
			Actions:  map[string]string{"keep": "That's me", "correct": "Not quite", "not_now": "Not now"},
		},
		KindCapturePaused: {
			PushBody: "Isoform stopped watching for a while.",
			Sealed:   "{what} has been off for {duration}. Turn it back on?",
			Text:     "Isoform: {what} has been off for {duration}. Turn it back on in Isoform.",
			Actions:  map[string]string{"resume_capture": "Resume"},
		},
		KindMeetingPrep: {
			PushBody: "A meeting is about to start.",
			Sealed:   "{title} starts in {minutes} min {place} {people}.",
			Text:     "Isoform: \"{title}\" starts in {minutes} min {place}.",
			Actions:  map[string]string{"open": "Open in Calendar"},
		},
		KindCalendarConflict: {
			PushBody: "Something on your calendar needs a look.",
			Sealed:   "{summary}",
			Text:     "Isoform: {summary}",
			Actions:  map[string]string{"open": "Open in Calendar", "keep_both": "Keep both"},
		},
		KindMeetingFollowup: {
			PushBody: "A meeting left you follow-ups.",
			Sealed:   "{title}: {followups} follow-ups for you, {decisions} decisions.",
			Text:     "Isoform: \"{title}\" left you {followups} follow-ups.",
			Actions:  map[string]string{"open_notes": "Open notes", "later": "Later"},
		},
		KindWaitingOnReply: {
			PushBody: "Someone is waiting on your reply.",
			Sealed:   "{name} is waiting on your reply.",
			Text:     "Isoform: {name} is waiting on your reply.",
			Actions:  map[string]string{"copy_draft": "Copy draft", "send": "Send", "open": "Review", "not_now": "Not now"},
		},
		KindPlaybookSuggested: {
			PushBody: "Isoform can take a routine off your hands.",
			Sealed:   "You open {site} most days around {time}. Want Isoform to check it for you and send you the numbers?",
			Text:     "Isoform: you open {site} most days around {time}. Open Playbooks on your computer to have Isoform check it for you.",
			Actions:  map[string]string{"see": "Review", "not_now": "Not now"},
		},
		KindPlaybookResult: {
			PushBody:     "A playbook finished.",
			Sealed:       "{title}: {result}",
			Text:         "Isoform: {title}: {result}",
			EmailSubject: "Isoform: {title}",
			EmailBody:    "{detail}\n\nIsoform ran \"{title}\" on your computer at {time}. To change where its results go, open Playbooks in Isoform.",
			Actions:      map[string]string{"review": "Review"},
		},
		KindReminder: {
			PushBody: "A reminder from Isoform.",
			Sealed:   "Reminder: {title}",
			Text:     "Isoform reminder: {title}",
			Actions:  map[string]string{"done": "Done", "not_now": "Not now"},
		},
		KindAnswerReady: {
			PushBody: "An answer you asked for is in.",
			Sealed:   "{subject}: {first_line}",
			Text:     "Isoform: an answer you asked for is in. Reply R to read it.",
			Actions:  map[string]string{"open": "Read it"},
		},
	},
	"zh-CN": {
		KindFirstWeek:   {PushBody: "与 Isoform 的第一周", Sealed: "打开聊天，查看第一周的提醒。", Text: "Isoform：第一周的提醒已在电脑和手机的聊天中。", Actions: map[string]string{"open": "展开阅读"}},
		KindWeeklyRecap: {PushBody: "你的一周已准备好。", Sealed: "简短回顾你的这一周，并给周一一个建议。打开简报查看。", Text: "Isoform：你的一周已在电脑和手机的简报中。", Actions: map[string]string{"open": "阅读你的一周"}},
		KindDecisionDue: {
			PushBody: "工作需要你的授权。",
			Sealed:   "“{title}”需要你的授权才能继续。",
			Text:     "Isoform：工作需要你的授权。请打开聊天查看请求。",
			Actions:  map[string]string{"approve": "允许", "decline": "暂不"},
		},
		KindCommitmentSlipping: {
			PushBody: "有一项承诺需要关注。",
			Sealed:   "{title}将于{due}到期，我没有相关进展的记录。",
			Text:     "Isoform：“{title}”将于{due}到期，我没有相关进展的记录。打开 Isoform 查看。",
			Actions:  map[string]string{"open": "找个时间"},
		},
		KindMeetingEnded: {
			PushBody: "会议记录已经准备好了。",
			Sealed:   "{title}：{decisions} 项决定，{followups} 项待跟进。",
			Text:     "Isoform：“{title}”的会议记录好了：{decisions} 项决定，{followups} 项待跟进。",
			Actions:  map[string]string{"open_notes": "查看记录", "later": "稍后"},
		},
		KindPersonReappeared: {
			PushBody: "你认识的人出现了。",
			Sealed:   "{name}在{where}中出现了。要记一笔吗？",
			Text:     "Isoform：{name}在{where}中出现了。回复 X 不再提醒此人。",
			Actions:  map[string]string{"open_person": "查看人物", "snooze": "稍后提醒", "stop_person": "不再提醒此人"},
		},
		KindGoalCheckIn: {
			PushBody: "看看目标进展。",
			Sealed:   "你设定了“{title}”。进展如何？",
			Text:     "Isoform：“{title}”进展如何？直接回复一句话，或回复 Z 稍后提醒。",
			Actions:  map[string]string{"update": "更新", "snooze": "稍后提醒", "dismiss": "暂不"},
		},
		KindWorkFinished: {
			PushBody: "工作已完成。",
			Sealed:   "{title}可以查看了。没有发送任何内容。",
			Text:     "Isoform：“{title}”可以查看了。没有发送任何内容。",
			Actions:  map[string]string{"review": "查看"},
		},
		KindWorkBlocked: {
			PushBody: "有工作在等你。",
			Sealed:   "{title}需要{question}",
			Text:     "Isoform：“{title}”需要{question}回复 A 在聊天中回答，回复 L 稍后。",
			Actions:  map[string]string{"answer": "回答", "stop": "停止", "later": "稍后"},
		},
		KindBriefReady: {
			PushBody: "你的简报已经准备好了。",
			Sealed:   "你的{slot}简报已经准备好了。",
			Text:     "Isoform：你的{slot}简报已在电脑上准备好了。",
			Actions:  map[string]string{"open": "播放"},
		},
		KindBriefMissed: {
			PushBody: "你的简报没有来。",
			Sealed:   "你的{slot}简报错过了。打开“今天”查看下一次的时间。",
			Text:     "Isoform：你的{slot}简报错过了。在电脑上打开“今天”查看下一次。",
			Actions:  map[string]string{"make_it_now": "现在生成", "open": "打开今天"},
		},
		KindScheduleMissed: {
			PushBody: "有一个定时任务没有运行。",
			Sealed:   "“{title}”连续两次没有运行。打开“即将进行”查看原因。",
			Text:     "Isoform：“{title}”连续两次没有运行。在电脑上打开“即将进行”查看原因。",
			Actions:  map[string]string{"run_now": "现在运行", "open": "查看原因"},
		},
		KindSuggestion: {
			PushBody: "Isoform 有一个建议。",
			Sealed:   "你做了 {count} 次“{title}”。要让 Isoform 接手吗？",
			Text:     "Isoform：你做了 {count} 次“{title}”。回复 N 忽略，回复 L 稍后。",
			Actions:  map[string]string{"see": "看看", "add_to_upcoming": "添加到即将进行", "do_once": "就做这个", "not_now": "暂不", "dismiss": "暂不"},
		},
		KindTopicFinds: {
			PushBody: "你关注的话题有新内容。",
			Sealed:   "{topic}有新内容：{title}",
			Text:     "Isoform：{topic}有新内容：{title}。在电脑上打开“今天”查看更多。",
			Actions:  map[string]string{"open": "查看新内容", "not_now": "暂不"},
		},
		KindLearnedToConfirm: {
			PushBody: "Isoform 学到了一些东西。",
			Sealed:   "{statement}要保留吗？",
			Text:     "Isoform：我学到了“{statement}”。回复 K 保留，回复 C 纠正。",
			Actions:  map[string]string{"keep": "是我", "correct": "不太对", "not_now": "暂不"},
		},
		KindCapturePaused: {
			PushBody: "Isoform 暂停观察了一会儿。",
			Sealed:   "{what}已经关闭了{duration}。要重新打开吗？",
			Text:     "Isoform：{what}已经关闭了{duration}。在 Isoform 中重新打开。",
			Actions:  map[string]string{"resume_capture": "恢复观察"},
		},
		KindMeetingPrep: {
			PushBody: "有一个会议即将开始。",
			Sealed:   "{title}将在 {minutes} 分钟后开始（{place} {people}）。",
			Text:     "Isoform：“{title}”将在 {minutes} 分钟后开始 {place}。",
			Actions:  map[string]string{"open": "在日历中打开"},
		},
		KindCalendarConflict: {
			PushBody: "日历上有需要留意的安排。",
			Sealed:   "{summary}",
			Text:     "Isoform：{summary}",
			Actions:  map[string]string{"open": "在日历中打开", "keep_both": "两个都保留"},
		},
		KindMeetingFollowup: {
			PushBody: "会议给你留下了待跟进事项。",
			Sealed:   "{title}：{followups} 项需要你跟进，{decisions} 项决定。",
			Text:     "Isoform：“{title}”给你留下了 {followups} 项待跟进。",
			Actions:  map[string]string{"open_notes": "查看记录", "later": "稍后"},
		},
		KindWaitingOnReply: {
			PushBody: "有人在等你回复。",
			Sealed:   "{name}在等你回复。",
			Text:     "Isoform：{name}在等你回复。",
			Actions:  map[string]string{"copy_draft": "复制草稿", "send": "发送", "open": "查看", "not_now": "暂不"},
		},
		KindPlaybookSuggested: {
			PushBody: "Isoform 可以替你接手一项日常事务。",
			Sealed:   "你几乎每天 {time} 左右打开 {site}。要让 Isoform 替你查看，并把数字发给你吗？",
			Text:     "Isoform：你几乎每天 {time} 左右打开 {site}。在电脑上打开“操作手册”，让 Isoform 替你查看。",
			Actions:  map[string]string{"see": "查看", "not_now": "暂不"},
		},
		KindPlaybookResult: {
			PushBody:     "一个操作手册运行完了。",
			Sealed:       "{title}：{result}",
			Text:         "Isoform：{title}：{result}",
			EmailSubject: "Isoform：{title}",
			EmailBody:    "{detail}\n\nIsoform 于 {time} 在你的电脑上运行了“{title}”。要更改结果的发送方式，请在 Isoform 中打开“操作手册”。",
			Actions:      map[string]string{"review": "查看"},
		},
		KindReminder: {
			PushBody: "来自 Isoform 的提醒。",
			Sealed:   "提醒：{title}",
			Text:     "Isoform 提醒：{title}",
			Actions:  map[string]string{"done": "完成", "not_now": "暂不"},
		},
		KindAnswerReady: {
			PushBody: "你问的事有结果了。",
			Sealed:   "{subject}：{first_line}",
			Text:     "Isoform：你问的事有结果了。回复 R 查看。",
			Actions:  map[string]string{"open": "阅读"},
		},
	},
}

// Copy returns the text for a kind in a language, falling back to English
// for unknown languages. The second result is false for unknown kinds.
// Optional parameters select the waiting-mail template. A raw subject is never
// used; only the consent-gated headline_subject permits the subject variant.
func Copy(kind Kind, lang string, params ...map[string]string) (CopySet, bool) {
	table, ok := copyCatalog[normalizeLang(lang)]
	if !ok {
		table = copyCatalog["en"]
	}
	set, ok := table[kind]
	if kind == KindBriefReady && len(params) > 0 {
		// Slot is schedule metadata, safe even in an unsealed push. Select
		// complete localized catalog copy rather than interpolating a title.
		titles := map[string]map[string]string{
			"en":    {"morning": "Your morning brief is ready.", "evening": "Your evening brief is ready."},
			"zh-CN": {"morning": "你的早间简报已经准备好了。", "evening": "你的晚间简报已经准备好了。"},
		}
		langKey := normalizeLang(lang)
		if titles[langKey] == nil {
			langKey = "en"
		}
		if title := titles[langKey][params[0]["slot"]]; title != "" {
			set.PushBody = title
			set.Sealed = title
		} else {
			set.Sealed = set.PushBody
			set.Text = Render(set.Text, nil)
		}
	}
	if ok && kind == KindBriefMissed {
		var p map[string]string
		if len(params) > 0 {
			p = params[0]
		}
		set.Sealed, set.Text = briefMissedCopy(normalizeLang(lang), p)
	}
	if ok && kind == KindCapturePaused && len(params) > 0 {
		if sealed, text := capturePausedCopy(normalizeLang(lang), params[0]["cause"]); sealed != "" {
			set.Sealed, set.Text = sealed, text
		}
	}
	if ok {
		labels := make(map[string]string, len(set.Actions)+1)
		for id, label := range set.Actions {
			labels[id] = label
		}
		labels[ActionMuteKind] = MuteKindLabel(lang)
		if kind == KindFirstWeek {
			labels[ActionMuteKind] = NotForMeLabel(lang)
		}
		set.Actions = labels
	}
	if ok && kind == KindDecisionDue && len(params) > 0 && params[0]["_activity_thread"] != "" {
		set.Sealed = "{title} {next_step}"
		if normalizeLang(lang) == "zh-CN" {
			set.PushBody = "一件未完成的事可以继续。"
			set.Text = "Isoform：打开聊天，查看下一步。"
			set.Actions = map[string]string{"open": "带我过去", ActionMuteKind: MuteKindLabel(lang)}
		} else {
			set.PushBody = "An open thread has a next step."
			set.Text = "Isoform: open the chat to review the next step."
			set.Actions = map[string]string{"open": "Take me there", ActionMuteKind: MuteKindLabel(lang)}
		}
	}
	if ok && kind == KindWaitingOnReply && len(params) > 0 {
		values := params[0]
		hasSubject := strings.TrimSpace(values["headline_subject"]) != ""
		hasSince := strings.TrimSpace(values["waiting_since"]) != ""
		variant := 0
		if hasSubject {
			variant += 1
		}
		if hasSince {
			variant += 2
		}
		if normalizeLang(lang) == "zh-CN" {
			switch variant {
			case 3:
				set.Sealed = "{name}从{waiting_since}起就在等你回复关于“{headline_subject}”的邮件。"
			case 1:
				set.Sealed = "{name}在等你回复关于“{headline_subject}”的邮件。"
			case 2:
				set.Sealed = "{name}从{waiting_since}起就在等你回复。"
			}
			set.Text = "Isoform：" + set.Sealed
		} else {
			switch variant {
			case 3:
				set.Sealed = "{name} has been waiting on your answer about {headline_subject} since {waiting_since}."
			case 1:
				set.Sealed = "{name} is waiting on your reply about {headline_subject}."
			case 2:
				set.Sealed = "{name} has been waiting on a reply since {waiting_since}."
			}
			set.Text = "Isoform: " + set.Sealed
		}
		if normalizeLang(lang) == "zh-CN" {
			set.Actions["open"] = "查看"
		} else {
			set.Actions["open"] = "Review"
		}
	}
	return set, ok
}

// ChatActions lists only actions that the main chat can carry out today.
// Notification action ids stay readable for receipts from older phones.
// The optional flags add clipboard and send actions only after draft validation.
func ChatActions(kind Kind, draftAllowed ...bool) []Action {
	var ids []string
	switch kind {
	case KindSuggestion:
		ids = []string{"add_to_upcoming", "do_once", "not_now", "dismiss"}
	case KindBriefMissed:
		ids = []string{"make_it_now", "open"}
	case KindLearnedToConfirm:
		ids = []string{"keep", "correct", "not_now"}
	case KindReminder:
		ids = []string{"done", "not_now"}
	case KindScheduleMissed:
		ids = []string{"run_now", "open"}
	case KindCommitmentSlipping:
		ids = []string{"open"}
	case KindPlaybookResult:
		ids = []string{"review"}
	case KindBriefReady, KindTopicFinds, KindFirstWeek, KindWeeklyRecap, KindAnswerReady:
		ids = []string{"open"}
	case KindCalendarConflict:
		ids = []string{"open", "keep_both"}
	case KindWaitingOnReply:
		ids = []string{"open", "not_now"}
		if kind == KindWaitingOnReply {
			if len(draftAllowed) > 0 && draftAllowed[0] {
				ids = append([]string{"copy_draft"}, ids...)
				if len(draftAllowed) > 1 && draftAllowed[1] {
					ids = append([]string{"send"}, ids...)
				}
			}
		}
	case KindMeetingPrep:
		ids = []string{"open"}
	case KindWorkBlocked:
		ids = []string{"answer"}
	case KindWorkFinished:
		ids = []string{"review"}
	case KindCapturePaused:
		ids = []string{"resume_capture"}
	case KindPlaybookSuggested:
		ids = []string{"see"}
	case KindPersonReappeared:
		ids = []string{"open_person"}
	default:
		return nil
	}
	actions := make([]Action, 0, len(ids))
	for _, id := range ids {
		actions = append(actions, Action{ID: id, Label: id})
	}
	return actions
}

var forbiddenCopyWords = regexp.MustCompile(`(?i)\b(?:yansu|urgent|critical)\b`)

// ValidBody bounds the short message shown in chat and on a doorbell. Source
// parameters are untrusted copy; a rejected body falls back to generic copy.
func ValidBody(text string) bool {
	if strings.ContainsAny(text, "!！") || forbiddenCopyWords.MatchString(text) || len(strings.Fields(text)) > 60 {
		return false
	}
	han := 0
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			han++
		}
		if r >= 0x1F000 && r <= 0x1FAFF || r >= 0x2600 && r <= 0x27BF || r == 0xFE0F || r == 0x20E3 {
			return false
		}
	}
	// Chinese does not delimit words with spaces. Bound its equivalent short
	// message separately so one unbroken parameter cannot evade the limit.
	return han <= 240 && utf8.RuneCountInString(text) <= 600
}

func DigestHeadline(lang string, count ...int) string {
	if len(count) > 0 && count[0] > 0 {
		if count[0] == 1 {
			if normalizeLang(lang) == "zh-CN" {
				return "有一件事可以看看。"
			}
			return "One thing to catch up on."
		}
		if normalizeLang(lang) == "zh-CN" {
			return fmt.Sprintf("有 %d 件事可以看看。", count[0])
		}
		return fmt.Sprintf("%d things to catch up on.", count[0])
	}
	if normalizeLang(lang) == "zh-CN" {
		return "有几件事可以看看。"
	}
	return "A few things to catch up on."
}

func DigestStatus(lang string) string {
	if normalizeLang(lang) == "zh-CN" {
		return "你的最新动态已准备好。"
	}
	return "Your updates are ready."
}

func StatusReport(lang, kind, title string) string {
	text := ""
	fallback := ""
	switch kind {
	case "handoff_completed":
		text = "Finished {title}, ready to review."
		fallback = "Untitled work"
		if normalizeLang(lang) == "zh-CN" {
			text = "{title}已完成，可以查看了。"
			fallback = "未命名工作"
		}
	case "schedule_ok":
		text = "{title} produced a result."
		fallback = "A schedule"
		if normalizeLang(lang) == "zh-CN" {
			text = "{title}已生成结果。"
			fallback = "一个定时任务"
		}
	case "schedule_error":
		text = "{title} did not finish its last run."
		fallback = "A schedule"
		if normalizeLang(lang) == "zh-CN" {
			text = "{title}上一次运行没有完成。"
			fallback = "一个定时任务"
		}
	case "schedule_missed_once":
		text = "{title} missed its scheduled time."
		fallback = "A schedule"
		if normalizeLang(lang) == "zh-CN" {
			text = "{title}错过了计划运行时间。"
			fallback = "一个定时任务"
		}
	}
	if strings.TrimSpace(title) == "" {
		title = fallback
	}
	line := Render(text, map[string]string{"title": title})
	if !ValidBody(line) {
		line = Render(text, map[string]string{"title": fallback})
	}
	return line
}

// Render fills {param} placeholders from params. Missing params are replaced
// with an empty string and surrounding double spaces are collapsed, so a
// template never leaks its placeholder names.
func Render(template string, params map[string]string) string {
	var out strings.Builder
	rest := template
	for {
		start := strings.Index(rest, "{")
		if start < 0 {
			out.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}")
		if end < 0 {
			out.WriteString(rest)
			break
		}
		// A value is written through, never rescanned, so braces inside a
		// parameter can neither expand nor loop.
		out.WriteString(rest[:start])
		out.WriteString(strings.TrimSpace(params[rest[start+1:start+end]]))
		rest = rest[start+end+1:]
	}
	text := out.String()
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	return strings.TrimSpace(text)
}

func normalizeLang(lang string) string {
	lang = strings.TrimSpace(lang)
	switch {
	case strings.HasPrefix(strings.ToLower(lang), "zh"):
		return "zh-CN"
	case lang == "":
		return "en"
	default:
		if _, ok := copyCatalog[lang]; ok {
			return lang
		}
		return "en"
	}
}

// GenericText is a kind's text that names nothing: "Isoform: " and the line
// a push carries in the clear.
func GenericText(kind Kind, lang string) (string, bool) {
	set, ok := Copy(kind, lang)
	if !ok || strings.TrimSpace(set.PushBody) == "" {
		return "", false
	}
	if normalizeLang(lang) == "zh-CN" {
		return "Isoform：" + set.PushBody, true
	}
	return "Isoform: " + set.PushBody, true
}

// playbookFailed is the result line of a playbook run that did not finish,
// before its reason; playbookSeeWhy stands in for a reason the run did not
// give.
var (
	playbookFailed = map[string]string{
		"en":    "did not finish.",
		"zh-CN": "没有完成。",
	}
	playbookSeeWhy = map[string]string{
		"en":    "Open Playbooks on your computer to see why.",
		"zh-CN": "在电脑上打开“操作手册”查看原因。",
	}
)

var playbookChanged = map[string]string{
	"en":    "It changed since you approved it; open Playbooks on your computer to look at it again.",
	"zh-CN": "它在你批准后有了改动，请在电脑上打开“操作手册”重新查看。",
}

// PlaybookChangedReason is why a run of a playbook edited since its approval
// was stopped, in a language.
func PlaybookChangedReason(lang string) string {
	return playbookChanged[normalizeLang(lang)]
}

// PlaybookFailedResult is the {result} of a failed playbook run in a
// language: that it did not finish, then the reason, or where to see it.
func PlaybookFailedResult(lang, reason string) string {
	lang = normalizeLang(lang)
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = playbookSeeWhy[lang]
	}
	if lang == "zh-CN" {
		return playbookFailed[lang] + reason
	}
	return playbookFailed[lang] + " " + reason
}

// briefMissedCopy says why a brief didn't run only when its run record names
// a reason, and otherwise when it was due. Isoform doesn't guess (§5).
func briefMissedCopy(lang string, params map[string]string) (string, string) {
	reasons := map[string]map[string]string{
		"en":    {"failed": "it couldn't be made", "asleep": "your computer was asleep", "closed": "Isoform was closed"},
		"zh-CN": {"failed": "没能生成", "asleep": "当时电脑在睡眠", "closed": "当时 Isoform 没有打开"},
	}
	zh := lang == "zh-CN"
	reason := ""
	if params != nil {
		reason = reasons[map[bool]string{true: "zh-CN", false: "en"}[zh]][params["reason"]]
	}
	// Today's sentence with its cause (settings-review ST66, today-review
	// v1.1.13 D3). The reason is filled in here; {slot} and {time} stay for
	// the caller to localize.
	at := params != nil && params["time"] != ""
	clock := ""
	if at {
		clock = briefMissedClock(lang, params["time"])
	}
	var sealed string
	switch {
	case zh && at && reason != "":
		sealed = "你的{slot}简报在" + clock + "没有来：" + reason + "。"
	case at && reason != "":
		sealed = "Your {slot} brief didn't come at " + clock + ": " + reason + "."
	case zh && reason != "":
		sealed = "你的{slot}简报没有来：" + reason + "。"
	case reason != "":
		sealed = "Your {slot} brief didn't come: " + reason + "."
	case zh && at:
		sealed = "你的{slot}简报在" + clock + "没有来。"
	case at:
		sealed = "Your {slot} brief didn't come at " + clock + "."
	case zh:
		sealed = "你的{slot}简报没有来。"
	default:
		sealed = "Your {slot} brief didn't come."
	}
	if zh {
		return sealed, "Isoform：" + sealed
	}
	return sealed, "Isoform: " + sealed
}

// capturePausedCopy says which source stopped and why; only a stop nobody
// chose is announced (settings-review ST66).
func capturePausedCopy(lang, cause string) (string, string) {
	causes := map[string]map[string]string{
		"en":    {"permission": "its permission was turned off in System Settings", "stopped": "it stopped on its own"},
		"zh-CN": {"permission": "系统设置里关闭了它的权限", "stopped": "它自己停了"},
	}
	key := "en"
	if lang == "zh-CN" {
		key = "zh-CN"
	}
	reason := causes[key][cause]
	if reason == "" {
		return "", ""
	}
	if key == "zh-CN" {
		sealed := "{what}已经停了{duration}：" + reason + "。要重新打开吗？"
		return sealed, "Isoform：" + sealed
	}
	sealed := "{what} has been off for {duration}: " + reason + ". Turn it back on?"
	return sealed, "Isoform: " + sealed
}

// briefMissedClock renders the slot's due time ("15:04") on a 12-hour clock,
// as Today does: "7:30 AM", zh "7:30".
func briefMissedClock(lang, hhmm string) string {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return hhmm
	}
	if lang == "zh-CN" {
		return fmt.Sprintf("%d:%02d", t.Hour(), t.Minute())
	}
	return t.Format("3:04 PM")
}
