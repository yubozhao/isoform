#if canImport(UIKit)
import UIKit
import UserNotifications

@MainActor
final class AgentPushAppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    private var pendingToken: Data?
    private var pendingFailure = false
    private var pendingBriefPush = false
    /// A tap on the brief ready notice that arrived before the app was ready: the brief, and whether to play it.
    private var pendingBriefOpen: (briefID: String, play: Bool)?
    private var pendingTodayOpen: (pairing: String?, kind: String?, id: String?)?

    weak var environment: AppEnvironment? {
        didSet {
            guard let environment else { return }
            BackgroundTodayRefresh.schedule(environment.keepAwake.briefSchedule, paired: environment.pairing.isPaired)
            if pendingBriefPush {
                pendingBriefPush = false
                environment.router.briefPushReceived()
            }
            if let open = pendingBriefOpen {
                pendingBriefOpen = nil
                Task { await environment.openBriefFromNotice(open.briefID, play: open.play) }
            }
            if let open = pendingTodayOpen {
                pendingTodayOpen = nil
                receiveTodayOpen(pairing: open.pairing, kind: open.kind, id: open.id)
            }
            if let token = pendingToken {
                pendingToken = nil
                Task { await environment.push.receiveToken(token) }
            }
            if pendingFailure {
                pendingFailure = false
                environment.push.receiveRegistrationFailure()
            }
        }
    }

    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        NotificationCategories.register()
        BackgroundTodayRefresh.register()
        BackgroundRecordingSend.register()
        return true
    }

    /// iOS started the app because recording uploads ended while it was closed.
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String,
                     completionHandler: @escaping () -> Void) {
        guard identifier == RecordingBackgroundUploads.sessionIdentifier else {
            completionHandler()
            return
        }
        RecordingBackgroundUploads.shared.systemWoke(completion: completionHandler)
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        guard let environment else {
            pendingToken = deviceToken
            return
        }
        Task { await environment.push.receiveToken(deviceToken) }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        guard let environment else {
            pendingFailure = true
            return
        }
        environment.push.receiveRegistrationFailure()
    }

    /// A silent `ready` push: a brief is ready on the computer, so fetch it (PROTOCOL.md 9.5). A silent `withdraw`
    /// push: notices read on another device leave this one (notify A).
    func application(_ application: UIApplication, didReceiveRemoteNotification userInfo: [AnyHashable: Any],
                     fetchCompletionHandler completionHandler: @escaping (UIBackgroundFetchResult) -> Void) {
        if BriefReadyWake.parse(userInfo) != nil {
            let app = environment ?? AppEnvironment.shared
            let once = BriefReadyWake.Completion(completionHandler)
            let work = Task {
                let result = await BriefReadyWake.handle(userInfo, pairedHub: app.vault.current()?.hubID) {
                    // As the scheduled wake does: the brief and Today beside the watch drain.
                    async let refreshed = app.backgroundBriefRefresh()
                    async let drained: Void = WatchLink.shared.drainInBackground()
                    let value = await refreshed
                    await drained
                    return value
                }
                once.finish(result ?? .noData)
            }
            // Answer iOS before its time is up, so it doesn't thin out this app's pushes.
            Task {
                try? await Task.sleep(for: .seconds(BriefReadyWake.answerWithin))
                work.cancel()
                once.finish(.newData)
            }
            return
        }
        guard let withdraw = NoticeWithdraw.parse(userInfo),
              let paired = (environment ?? AppEnvironment.shared).vault.current(), paired.hubID == withdraw.hub else {
            completionHandler(.noData)
            return
        }
        Task {
            await NoticeWithdraw.withdraw(Set(withdraw.ids))
            completionHandler(.newData)
        }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter,
                                            willPresent notification: UNNotification,
                                            withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list, .sound])
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter,
                                            didReceive response: UNNotificationResponse,
                                            withCompletionHandler completionHandler: @escaping () -> Void) {
        let request = response.notification.request
        let userInfo = request.content.userInfo
        let action = response.actionIdentifier
        // Later keeps the content as delivered, so the reminder shows the same sealed text.
        let isLater = action == NotificationCategories.Action.later
        let laterContent = isLater ? request.content.mutableCopy() as? UNMutableNotificationContent : nil
        Task { @MainActor in
            await self.receiveResponse(action: action, userInfo: userInfo, laterContent: laterContent)
            completionHandler()
        }
    }

    // MARK: Responses

    private func receiveResponse(action: String, userInfo: [AnyHashable: Any],
                                 laterContent: UNMutableNotificationContent?) async {
        // The brief ready notice opens its brief; Play also starts it. A tap on the notice itself reads the brief.
        if let briefID = userInfo["localBriefID"] as? String {
            switch action {
            case UNNotificationDefaultActionIdentifier, NotificationCategories.Action.read, NotificationCategories.Action.play:
                receiveBriefOpen(briefID, play: action == NotificationCategories.Action.play)
            default:
                break
            }
            return
        }
        if userInfo["localToday"] as? Bool == true {
            if action == UNNotificationDefaultActionIdentifier || action == NotificationCategories.Action.open {
                receiveTodayOpen(pairing: userInfo["localTodayPairing"] as? String,
                                 kind: userInfo["destinationKind"] as? String,
                                 id: userInfo["destinationId"] as? String)
            }
            return
        }
        if let notice = NoticePush.parse(userInfo) {
            await receiveNotice(action: action, notice: notice, userInfo: userInfo, laterContent: laterContent)
            return
        }
        // Pushes from before notices carry no notice id. A brief push opens the Brief, and so does a `needs_you`
        // push from an older Mac, because the Desk it pointed to is gone.
        guard action == UNNotificationDefaultActionIdentifier else { return }
        if NotificationRouter.opensBrief(userInfo) {
            receiveBriefTap()
        }
    }

    private func receiveTodayOpen(pairing: String?, kind: String?, id: String?) {
        guard let environment else {
            pendingTodayOpen = (pairing, kind, id)
            return
        }
        guard let paired = environment.vault.current(),
              pairing == RelayCrypto.sha256Hex(Data("\(paired.hubID)/\(paired.deviceID)".utf8)) else { return }
        environment.router.openTodayNotice(kind: kind, id: id)
    }

    private func receiveNotice(action: String, notice: NoticePush, userInfo: [AnyHashable: Any],
                               laterContent: UNMutableNotificationContent?) async {
        let app = AppEnvironment.shared
        guard let paired = app.vault.current(), paired.hubID == notice.hub, paired.deviceID == notice.dev else { return }
        switch action {
        case UNNotificationDismissActionIdentifier:
            await app.receipts.post(noticeID: notice.id, state: .dismissed)
        case UNNotificationDefaultActionIdentifier, NotificationCategories.Action.open,
             NotificationCategories.Action.read, NotificationCategories.Action.play:
            let plaintext = NoticePush.plaintext(in: userInfo)
            if notice.kind == "notification_test" {
                app.router.openSettings()
            } else if let plaintext, let kind = plaintext.destinationKind, let id = plaintext.destinationID {
                if kind == "brief" { await app.openBriefFromNotice(id, play: action == NotificationCategories.Action.play) }
                else { app.router.openTodayNotice(kind: kind, id: id) }
            } else {
                app.router.noticeOpened(NoticeRouting.route(kind: notice.kind, momentID: plaintext?.momentID))
            }
            await app.receipts.post(noticeID: notice.id, state: .opened)
        case NotificationCategories.Action.notNeeded, NotificationCategories.Action.useful:
            // Answered without opening the app (PROTOCOL.md 9.2). The receipt is measurement only, never the answer.
            if let answer = NoticeWorkAnswer(rawValue: action), let plaintext = NoticePush.plaintext(in: userInfo),
               let itemID = plaintext.workItemID {
                await app.answerWorkFromNotice(itemID: itemID, answer: answer, noticeID: notice.id)
            }
            await app.receipts.post(noticeID: notice.id, state: .acted, action: action)
        case NotificationCategories.Action.later:
            guard let laterContent else { return }
            do {
                try await LocalNotices.recordLater {
                    try await LocalNotices.scheduleLater(laterContent, noticeID: notice.id)
                } receipt: {
                    await app.receipts.post(noticeID: notice.id, state: .acted, action: action)
                }
            } catch { return }
        default:
            break
        }
    }

    private func receiveBriefOpen(_ briefID: String, play: Bool) {
        guard let environment else {
            pendingBriefOpen = (briefID, play)
            return
        }
        Task { await environment.openBriefFromNotice(briefID, play: play) }
    }

    private func receiveBriefTap() {
        guard let environment else {
            pendingBriefPush = true
            return
        }
        environment.router.briefPushReceived()
    }
}
#endif
