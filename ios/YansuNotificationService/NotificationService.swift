import Foundation
import UserNotifications

// Notification Service Extension. Opens a sealed notice before its banner is shown and posts the `delivered`
// receipt. A legacy push, a missing key, a failed check or a timeout hands back the content the relay sent,
// which is the fixed fallback text (PROTOCOL.md section 9). The weekly recap fallback is localized on-device
// when opening fails.
final class NotificationService: UNNotificationServiceExtension {
    private var delivery: PendingDelivery?
    private var work: Task<Void, Never>?

    override func didReceive(_ request: UNNotificationRequest,
                             withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void) {
        let kind = (request.content.userInfo["yansu"] as? [String: Any])?["kind"] as? String
        let pending = PendingDelivery(fallback: request.content, localizedKind: kind,
                                      handler: contentHandler)
        delivery = pending
        let userInfo = request.content.userInfo
        if let push = NoticePush.parse(userInfo),
           let context = SharedNoticeStore.load(secrets: KeychainSecretStore())?.context,
           context.hubID == push.hub, context.deviceID == push.dev {
            SharedNoticeDedup.record(hub: push.hub, device: push.dev, id: push.id)
        }
        let content = request.content.mutableCopy() as? UNMutableNotificationContent
        work = Task {
            let sealed = await NotificationService.deliver(userInfo: userInfo, content: content)
            pending.finish(sealed)
        }
    }

    override func serviceExtensionTimeWillExpire() {
        work?.cancel()
        delivery?.finish(nil)
    }

    // Posts the delivered receipt (best effort, five seconds) and returns the decrypted content when the notice opens.
    private static func deliver(userInfo: [AnyHashable: Any], content: UNMutableNotificationContent?) async -> UNNotificationContent? {
        guard let push = NoticePush.parse(userInfo) else { return nil }
        let secrets = KeychainSecretStore()
        let opened = NoticeDelivery.open(push, secrets: secrets)
        if push.kind == "notification_test", opened != nil {
            SharedNoticeDedup.recordVerifiedTest(hub: push.hub, device: push.dev, id: push.id)
        }
        let relay = RelayClient(requestTimeout: 5, resourceTimeout: 5)
        await NoticeReceipts(relay: relay, secrets: secrets, attempts: 1).post(noticeID: push.id, state: .delivered)
        guard let opened, let content else { return nil }
        if opened.plaintext.kind == "brief_ready", opened.plaintext.destinationKind == "brief",
           let briefID = opened.plaintext.destinationID {
            // The computer's brief notice is the brief's one notification: the local one yields (PROTOCOL.md 9.1).
            SharedNoticeDedup.recordRemoteBrief(hub: push.hub, device: push.dev, briefID: briefID)
            let center = UNUserNotificationCenter.current()
            let local = await center.pendingNotificationRequests()
                .filter { $0.identifier.hasPrefix("local.brief.") && ($0.content.userInfo["localBriefID"] as? String) == briefID }
                .map(\.identifier)
            center.removePendingNotificationRequests(withIdentifiers: local)
        }
        NoticeDelivery.apply(opened, to: content)
        return content
    }
}

// Hands the content to the system exactly once: the sealed copy when there is one, otherwise the fallback.
private final class PendingDelivery: @unchecked Sendable {
    private let lock = NSLock()
    private var handler: ((UNNotificationContent) -> Void)?
    private let fallback: UNNotificationContent
    private let localizedKind: String?

    init(fallback: UNNotificationContent, localizedKind: String?,
         handler: @escaping (UNNotificationContent) -> Void) {
        self.fallback = fallback
        self.localizedKind = localizedKind
        self.handler = handler
    }

    func finish(_ content: UNNotificationContent?) {
        let pending = lock.withLock { () -> ((UNNotificationContent) -> Void)? in
            let current = handler
            handler = nil
            return current
        }
        guard let pending else { return }
        if let content {
            pending(content)
            return
        }
        let chinese = Locale.current.language.languageCode?.identifier == "zh"
        let body: String?
        switch localizedKind {
        case "weekly_recap": body = chinese ? "你的一周已准备好。" : "Your week is ready."
        case "chat_answer": body = chinese ? "Isoform 已回复。" : "Isoform answered."
        default: body = nil
        }
        guard let body, let localized = fallback.mutableCopy() as? UNMutableNotificationContent else {
            pending(fallback)
            return
        }
        localized.body = body
        pending(localized)
    }
}
