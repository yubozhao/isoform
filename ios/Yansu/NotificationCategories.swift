import UserNotifications

// Categories and buttons for Isoform notices (notification design spec). They are registered at launch, so a
// notice delivered while the app is closed still has its buttons.
enum NotificationCategories {
    enum Action {
        static let later = "later"
        static let open = "open"
        /// The brief ready notice (LocalBriefNotices): open its brief view, or open it and play it.
        static let read = "read"
        static let play = "play"
        /// Answers from the notification (PROTOCOL.md 9.2), recorded without opening the app.
        static let notNeeded = NoticeWorkAnswer.notNeeded.rawValue
        static let useful = NoticeWorkAnswer.useful.rawValue
    }

    /// The category of the "Your brief is ready" notice, which has its own two buttons.
    static let localBriefCategory = "yansu.brief.ready"

    static var all: Set<UNNotificationCategory> {
        let open = UNNotificationAction(identifier: Action.open, title: "Open", options: [.foreground])
        let later = UNNotificationAction(identifier: Action.later, title: "Not now", options: [])
        let read = UNNotificationAction(identifier: Action.read, title: String(localized: "Read it"), options: [.foreground])
        let play = UNNotificationAction(identifier: Action.play, title: String(localized: "Play"), options: [.foreground])
        // customDismissAction delivers dismissals to the app, so a `dismissed` receipt can be sent.
        let options: UNNotificationCategoryOptions = [.customDismissAction]
        return [
            UNNotificationCategory(identifier: NoticeCategory.moment.rawValue,
                                   actions: [open, later],
                                   intentIdentifiers: [], options: options),
            UNNotificationCategory(identifier: NoticeCategory.brief.rawValue,
                                   actions: [read, play],
                                   intentIdentifiers: [], options: options),
            UNNotificationCategory(identifier: NoticeCategory.prep.rawValue,
                                   actions: [read, UNNotificationAction(identifier: Action.notNeeded,
                                                                        title: String(localized: "work.reason.notNeeded"), options: [])],
                                   intentIdentifiers: [], options: options),
            UNNotificationCategory(identifier: NoticeCategory.result.rawValue,
                                   actions: [read, UNNotificationAction(identifier: Action.useful,
                                                                        title: String(localized: "work.useful"), options: [])],
                                   intentIdentifiers: [], options: options),
            UNNotificationCategory(identifier: localBriefCategory,
                                   actions: [read, play],
                                   intentIdentifiers: [], options: options),
        ]
    }

    static func register(center: UNUserNotificationCenter = .current()) {
        center.setNotificationCategories(all)
    }
}

// Local notifications the app schedules itself: Later and the Settings test.
enum LocalNotices {
    static let snoozeInterval: TimeInterval = 60 * 60
    static let testDelay: TimeInterval = 5

    /// Receipt only follows a reminder accepted by the notification center.
    static func recordLater(schedule: () async throws -> Void, receipt: () async -> Void) async throws {
        try await schedule()
        await receipt()
    }

    /// Later: the same content, one hour later. The sealed text is already in the content.
    static func scheduleLater(_ content: UNMutableNotificationContent, noticeID: String,
                              center: UNUserNotificationCenter = .current()) async throws {
        let trigger = UNTimeIntervalNotificationTrigger(timeInterval: snoozeInterval, repeats: false)
        let request = UNNotificationRequest(identifier: "yansu.later.\(noticeID)", content: content, trigger: trigger)
        try await center.add(request)
    }

    /// The Settings test: a moment notice, so the design can be checked without APNs.
    static func scheduleTest(center: UNUserNotificationCenter = .current()) async throws {
        let id = "test-\(UUID().uuidString.lowercased())"
        let content = UNMutableNotificationContent()
        content.title = "Your meeting notes are ready."
        content.body = "This is a test from Settings."
        content.categoryIdentifier = NoticeCategory.moment.rawValue
        content.threadIdentifier = "yansu.test"
        content.interruptionLevel = .active
        content.sound = .default
        content.userInfo = ["yansu": ["kind": "meeting_ended", "id": id]]
        let trigger = UNTimeIntervalNotificationTrigger(timeInterval: testDelay, repeats: false)
        try await center.add(UNNotificationRequest(identifier: id, content: content, trigger: trigger))
    }
}
