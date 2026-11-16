import Foundation
import UserNotifications

/// A notice whose sealed copy was opened and checked against its push.
struct OpenedNotice: Equatable, Sendable {
    let plaintext: NoticePlaintext
    let json: String
}

// Opens a push's sealed notice and applies it to the notification content. Used by the notification
// service extension before the banner is shown. Every check must pass; any miss returns nil and the caller
// keeps the fallback the relay sent.
enum NoticeDelivery {
    static func open(_ push: NoticePush, secrets: SecretStore) -> OpenedNotice? {
        guard let ct = push.ct,
              let stored = SharedNoticeStore.load(secrets: secrets),
              stored.context.hubID == push.hub,
              stored.context.deviceID == push.dev else { return nil }
        guard let json = try? NoticeCrypto.open(ct, key: stored.key, hub: push.hub, dev: push.dev, noticeID: push.id),
              let plaintext = try? NoticeCrypto.decode(json),
              plaintext.id == push.id,
              plaintext.kind == push.kind,
              NoticeCategory(rawValue: plaintext.category) != nil,
              let text = String(data: json, encoding: .utf8) else { return nil }
        return OpenedNotice(plaintext: plaintext, json: text)
    }

    /// Sets the sealed title, body and category, and attaches the JSON under `yansu.notice` for the app.
    /// The thread identifier stays the hub id the relay already set.
    /// The Notification Center group for a notice kind: "isoform.notice.<kind>".
    static func threadIdentifier(kind: String) -> String { "isoform.notice." + kind }

    static func apply(_ notice: OpenedNotice, to content: UNMutableNotificationContent) {
        content.title = notice.plaintext.title
        content.body = notice.plaintext.body ?? ""
        // A notice that can be answered from the notification gets the phone category with that button (9.2).
        content.categoryIdentifier = NoticeWorkAnswer.category(for: notice.plaintext)?.rawValue ?? notice.plaintext.category
        // Notices of one kind stack together in Notification Center, so several read as one group (P145).
        content.threadIdentifier = NoticeDelivery.threadIdentifier(kind: notice.plaintext.kind)
        var userInfo = content.userInfo
        userInfo[NoticePush.noticeUserInfoKey] = notice.json
        content.userInfo = userInfo
    }
}
