import Foundation

/// One proactive notification kind as the Mac reports it (PROTOCOL.md section 16).
struct NotificationKind: Codable, Equatable, Identifiable, Sendable {
    let kind: String
    let enabled: Bool
    let defaultOn: Bool
    /// The kind paused itself after most deliveries were dismissed; turning it on resumes it.
    let paused: Bool
    let mutedUntil: String?
    let sent: Int
    let opened: Int
    let acted: Int
    let dismissed: Int
    let muted: Int

    var id: String { kind }

    /// Three kinds, and nothing else (phone-map rule 16, phone-rings v1.0.0 frame 17c).
    static let phoneKinds = ["brief_ready", "meeting_prep", "work_finished"]

    var phoneTitle: String {
        switch kind {
        case "work_blocked": return String(localized: "Something needs you")
        case "meeting_prep": return String(localized: "set.notif.prep")
        case "work_finished": return String(localized: "set.notif.done")
        case "brief_ready": return String(localized: "set.notif.brief")
        default: return title
        }
    }

    var effectivelyEnabled: Bool {
        let muted = mutedUntil.flatMap { ISO8601DateFormatter().date(from: $0) }.map { $0 > Date() } ?? false
        return enabled && !paused && !muted
    }

    func settingEnabled(_ enabled: Bool) -> NotificationKind {
        NotificationKind(kind: kind, enabled: enabled, defaultOn: defaultOn,
                         paused: enabled ? false : paused, mutedUntil: enabled ? nil : mutedUntil,
                         sent: sent, opened: opened, acted: acted, dismissed: dismissed, muted: muted)
    }

    /// The label for a kind the Mac knows (engagement.AllKinds()); an unknown kind shows its key. `decision_due`
    /// left with the Desk, so an older Mac that still lists it shows its key too.
    var title: String {
        Self.labels[kind]?.title ?? kind
    }

    var detail: String {
        Self.labels[kind]?.detail ?? ""
    }

    private static let labels: [String: (title: String, detail: String)] = [
        "work_blocked": ("Work blocked", "Work is waiting on an answer from you."),
        "work_finished": ("Work finished", "One-time work is done and ready to review."),
        "commitment_slipping": ("Commitments slipping", "Something you committed to is due soon with no progress seen."),
        "capture_paused": ("Capture paused", "Screen or audio capture on your computer has been off for a while."),
        "meeting_ended": ("Meeting notes ready", "Notes are ready after a meeting ends."),
        "meeting_prep": ("Meeting coming up", "Fifteen minutes before a meeting with other people or a call link."),
        "calendar_conflict": ("Calendar clashes", "Two events that overlap, or an invite you have not answered."),
        "waiting_on_reply": ("Waiting on your reply", "Someone you know wrote to you directly more than a day ago and has no answer yet."),
        "meeting_followup": ("Meeting follow-ups", "Things a meeting left for you to do, once the notes are ready."),
        "brief_ready": ("Brief ready", "A new brief is ready to read."),
        "person_reappeared": ("People who came back", "Someone you know came up again after a long while."),
        "goal_check_in": ("Goal check-ins", "A short check on a goal at its cadence."),
        "learned_to_confirm": ("Things Isoform learned", "Something Isoform learned about you that it wants you to confirm."),
        "schedule_missed": ("Missed schedule", "A schedule on Upcoming failed or did not run twice in a row."),
        "brief_missed": ("Missed brief", "A scheduled brief did not arrive. The notice says when the next one runs."),
        "suggestion": ("Suggestions", "One idea a week from work Isoform saw you repeat, with the evidence."),
        "playbook_suggested": ("Routines to take on", "When Isoform notices a page you open at the same time most days and can check it for you."),
        "playbook_result": ("Playbook results", "What a playbook found when it ran, sent where you chose for that playbook."),
        "topic_finds": ("New on your topics", "Once a day, new things on the topics you have been looking at."),
    ]
}

/// The notification controls the Mac shares with the phone.
struct NotificationSettings: Codable, Equatable, Sendable {
    let available: Bool
    let kinds: [NotificationKind]
    let quietStart: String
    let quietEnd: String
    let dailyInterruptBudget: Int
    let digestTimes: [String]
    let statsDays: Int

    /// Quiet hours are wall-clock values on the computer. Use a fixed zone so formatting never shifts them.
    static func quietHoursRange(start: String, end: String, locale: Locale) -> String? {
        guard start != end else { return nil }
        let zone = TimeZone(secondsFromGMT: 0)!
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = zone
        func date(_ value: String) -> Date? {
            let parts = value.split(separator: ":", omittingEmptySubsequences: false)
            guard parts.count == 2, parts[0].count == 2, parts[1].count == 2,
                  value.utf8.allSatisfy({ (48...57).contains($0) || $0 == 58 }),
                  let hour = Int(parts[0]), let minute = Int(parts[1]),
                  (0..<24).contains(hour), (0..<60).contains(minute) else { return nil }
            return calendar.date(from: DateComponents(year: 2001, month: 1, day: 15, hour: hour, minute: minute))
        }
        guard let first = date(start), let last = date(end) else { return nil }
        let style = Date.FormatStyle(date: .omitted, time: .shortened, locale: locale, timeZone: zone)
        return first.formatted(style) + " – " + last.formatted(style)
    }

    var phoneKinds: [NotificationKind] {
        NotificationKind.phoneKinds.compactMap { name in kinds.first { $0.kind == name } }
    }

    var enabledPhoneKinds: [NotificationKind] {
        phoneKinds.filter(\.effectivelyEnabled)
    }

    func applying(_ change: PendingNotificationChange) -> NotificationSettings {
        NotificationSettings(available: available, kinds: kinds.map { item in
            item.kind == change.kind ? item.settingEnabled(change.enabled ?? item.enabled) : item
        }, quietStart: change.quietStart ?? quietStart, quietEnd: change.quietEnd ?? quietEnd,
           dailyInterruptBudget: dailyInterruptBudget, digestTimes: digestTimes, statsDays: statsDays)
    }

    /// One sentence on the rules every kind shares.
    var rules: String {
        "Quiet hours run from \(quietStart) to \(quietEnd). At most \(dailyInterruptBudget) interruptions a day; "
            + "quieter kinds wait for a digest at \(digestTimes.joined(separator: ", "))."
    }
}

struct SetNotificationKindRequest: Encodable, Equatable {
    let idempotencyKey: String
    let enabled: Bool
}

struct SetNotificationPreferencesRequest: Encodable, Equatable, Sendable {
    let idempotencyKey: String
    let quietStart: String
    let quietEnd: String
}

struct PendingNotificationChange: Codable, Equatable, Sendable {
    let idempotencyKey: String
    var kind: String?
    var enabled: Bool?
    var quietStart: String?
    var quietEnd: String?
    var fromToday: Bool? = nil
    var dispatched: Bool? = nil
}
