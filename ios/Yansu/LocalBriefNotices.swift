import Foundation
import UserNotifications

/// The "Your brief is ready" notice for the phone's current brief (handoff 2026-10-28, section 6). It posts once the
/// brief's text and audio are both on this phone, or two minutes after the text if the audio is late. Its body is the
/// brief's lead sentence (handoff 2026-10-27 phone notifications), and only a brief the phone has in full is eligible.
enum LocalBriefNotices {
    @MainActor private static var inFlight: Set<String> = []

    /// How long the notice waits for audio after the text arrives.
    static let audioWait: TimeInterval = 2 * 60

    /// The notice's body: the brief's lead sentence, with a greeting joined to it, or its title when it has no words.
    static func body(_ brief: MobileBriefV1) -> String {
        PastBriefIndex.lead(brief) ?? BriefTitle.title(BriefTitleInput(brief: brief))
    }

    private static func pendingKey(_ pairing: TodayPairingIdentity) -> String {
        "localBrief.pending." + RelayCrypto.sha256Hex(Data("\(pairing.hubID)/\(pairing.deviceID)".utf8))
    }

    /// The brief the Mac names as current, or its newest listed brief for a Mac before 1.0.
    static func currentID(_ snapshot: MobileTodaySnapshot) -> String? {
        snapshot.currentBrief?.id ?? snapshot.latestBrief?.id
    }

    /// A new current brief makes the notice pending. A snapshot that names the same brief leaves it as it was.
    static func observe(_ current: MobileTodaySnapshot, previous: MobileTodaySnapshot?, pairing: TodayPairingIdentity,
                        defaults: UserDefaults) {
        let key = pendingKey(pairing)
        let id = currentID(current)
        if let previous, let id, id != currentID(previous) {
            defaults.set(id, forKey: key)
        } else if id != defaults.string(forKey: key) {
            defaults.removeObject(forKey: key)
        }
    }

    static func isPending(_ briefID: String, pairing: TodayPairingIdentity, defaults: UserDefaults) -> Bool {
        defaults.string(forKey: pendingKey(pairing)) == briefID
    }

    static func discardPending(pairing: TodayPairingIdentity, defaults: UserDefaults) {
        defaults.removeObject(forKey: pendingKey(pairing))
    }

    private static func digest(_ briefID: String, pairing: TodayPairingIdentity) -> String {
        RelayCrypto.sha256Hex(Data("\(pairing.hubID)/\(pairing.deviceID)/\(briefID)".utf8))
    }

    private static func sentKey(_ briefID: String, pairing: TodayPairingIdentity) -> String {
        "localBrief.sent." + digest(briefID, pairing: pairing)
    }

    private static func textKey(_ briefID: String, pairing: TodayPairingIdentity) -> String {
        "localBrief.text." + digest(briefID, pairing: pairing)
    }

    /// When the brief's text first reached this phone. It is kept until the notice is posted, so the two-minute wait
    /// does not restart when the text is seen again.
    static func textArrival(_ briefID: String, pairing: TodayPairingIdentity, defaults: UserDefaults,
                            now: Date) -> Date {
        let key = textKey(briefID, pairing: pairing)
        if let stored = defaults.object(forKey: key) as? Date { return stored }
        defaults.set(now, forKey: key)
        return now
    }

    /// When the notice is due: now once the audio is on the phone; otherwise two minutes after the text arrived, or
    /// now when that time has already passed.
    static func readyDate(textArrivedAt: Date, audioOnPhone: Bool, now: Date) -> Date {
        if audioOnPhone { return now }
        return max(now, textArrivedAt.addingTimeInterval(audioWait))
    }

    static func deliveryDate(scheduled: Date, now: Date, quietStart: String, quietEnd: String,
                             calendar: Calendar = .current) -> Date? {
        guard scheduled >= now.addingTimeInterval(-24 * 60 * 60) else { return nil }
        let candidate = max(scheduled, now)
        guard let start = minutes(quietStart), let end = minutes(quietEnd), start != end else { return candidate }
        let components = calendar.dateComponents([.hour, .minute], from: candidate)
        let minute = (components.hour ?? 0) * 60 + (components.minute ?? 0)
        let quiet = start < end ? (minute >= start && minute < end) : (minute >= start || minute < end)
        guard quiet else { return candidate }
        let nextDay = start > end && minute >= start
        let day = nextDay ? calendar.date(byAdding: .day, value: 1, to: candidate) ?? candidate : candidate
        return calendar.date(bySettingHour: end / 60, minute: end % 60, second: 0, of: day)
    }

    private static func minutes(_ value: String) -> Int? {
        let parts = value.split(separator: ":")
        guard parts.count == 2, let hour = Int(parts[0]), let minute = Int(parts[1]),
              (0..<24).contains(hour), (0..<60).contains(minute) else { return nil }
        return hour * 60 + minute
    }

    /// Posts the notice for `brief` when it is due, or moves a waiting notice up once its audio arrives. A notice that
    /// already waits for the two minutes keeps its time while the audio is still missing.
    @MainActor
    static func schedule(_ brief: MobileBriefV1, hasAudio: Bool, pairing: TodayPairingIdentity, settings: NotificationSettings,
                         defaults: UserDefaults = .standard, center: UNUserNotificationCenter = .current(),
                         now: Date = Date(), isCurrent: @escaping @MainActor () -> Bool = { true }) async {
        let authorization = await center.notificationSettings().authorizationStatus
        guard isCurrent(), [.authorized, .provisional, .ephemeral].contains(authorization),
              let kind = settings.kinds.first(where: { $0.kind == "brief_ready" }), kind.effectivelyEnabled,
              !brief.identifier.isEmpty else { return }
        let key = sentKey(brief.identifier, pairing: pairing)
        guard !defaults.bool(forKey: key), inFlight.insert(key).inserted else { return }
        defer { inFlight.remove(key) }
        let arrived = textArrival(brief.identifier, pairing: pairing, defaults: defaults, now: now)
        let ready = readyDate(textArrivedAt: arrived, audioOnPhone: hasAudio, now: now)
        guard let delivery = deliveryDate(scheduled: ready, now: now, quietStart: settings.quietStart,
                                          quietEnd: settings.quietEnd) else { return }
        let identifier = "local.brief.\(digest(brief.identifier, pairing: pairing))"
        if remoteNoticeCame(for: brief.identifier, pairing: pairing) {
            // The computer's own notice for this brief reached the phone: it is the one notification (9.1).
            center.removePendingNotificationRequests(withIdentifiers: [identifier])
            markSent(brief.identifier, pairing: pairing, defaults: defaults)
            return
        }
        let delivered = await center.deliveredNotifications()
        let pending = await center.pendingNotificationRequests()
        guard isCurrent() else { return }
        if delivered.contains(where: { $0.request.identifier == identifier }) {
            markSent(brief.identifier, pairing: pairing, defaults: defaults)
            return
        }
        if pending.contains(where: { $0.identifier == identifier }) {
            // The two-minute notice is already queued. Only the arrival of the audio moves it up.
            guard hasAudio else { return }
            center.removePendingNotificationRequests(withIdentifiers: [identifier])
        }
        let content = UNMutableNotificationContent()
        content.title = String(localized: "Your brief is ready.")
        content.body = Self.body(brief)
        content.sound = .default
        content.categoryIdentifier = NotificationCategories.localBriefCategory
        content.interruptionLevel = .passive
        content.userInfo = ["yansu": ["kind": "brief"], "localBriefID": brief.identifier]
        let trigger = UNTimeIntervalNotificationTrigger(timeInterval: max(1, delivery.timeIntervalSince(now)), repeats: false)
        do {
            try await center.add(UNNotificationRequest(identifier: identifier, content: content, trigger: trigger))
            guard isCurrent() else {
                center.removePendingNotificationRequests(withIdentifiers: [identifier])
                return
            }
            if hasAudio { markSent(brief.identifier, pairing: pairing, defaults: defaults) }
        } catch {
            // The next refresh may retry the same id.
        }
    }

    /// Whether the extension recorded the computer's brief_ready notice for this brief on this pairing.
    static func remoteNoticeCame(for briefID: String, pairing: TodayPairingIdentity,
                                 directory: URL? = SharedNoticeDedup.defaultDirectory) -> Bool {
        SharedNoticeDedup.containsRemoteBrief(hub: pairing.hubID, device: pairing.deviceID, briefID: briefID,
                                              directory: directory)
    }

    private static func markSent(_ briefID: String, pairing: TodayPairingIdentity, defaults: UserDefaults) {
        defaults.set(true, forKey: sentKey(briefID, pairing: pairing))
        defaults.removeObject(forKey: textKey(briefID, pairing: pairing))
        if defaults.string(forKey: pendingKey(pairing)) == briefID {
            defaults.removeObject(forKey: pendingKey(pairing))
        }
    }

    /// Removes a waiting notice for a brief that is no longer current.
    @MainActor
    static func reconcile(currentID: String?, center: UNUserNotificationCenter = .current(),
                          isCurrent: @escaping @MainActor () -> Bool) async {
        let requests = await center.pendingNotificationRequests()
        guard isCurrent() else { return }
        let stale = requests.filter { request in
            guard request.identifier.hasPrefix("local.brief."),
                  let id = request.content.userInfo["localBriefID"] as? String else { return false }
            return id != currentID
        }.map(\.identifier)
        center.removePendingNotificationRequests(withIdentifiers: stale)
    }

    static func cancelPending(center: UNUserNotificationCenter = .current()) async {
        let requests = await center.pendingNotificationRequests()
        center.removePendingNotificationRequests(withIdentifiers: requests.map(\.identifier).filter { $0.hasPrefix("local.brief.") })
    }
}
