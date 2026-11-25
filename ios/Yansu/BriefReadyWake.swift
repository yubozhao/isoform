#if canImport(UIKit)
import UIKit

/// The silent `ready` push (PROTOCOL.md 9.5): the computer stored a brief for this phone. It carries the hub id and
/// nothing about the brief, so the phone fetches the newest brief it is due, and Today, in the background time iOS
/// gives, without being opened. iOS throttles background pushes; the reads on open and the scheduled wakes stay the
/// guarantee.
enum BriefReadyWake {
    /// iOS gives a background push about 30 seconds; the completion handler is called by then.
    static let answerWithin: TimeInterval = 25

    /// Calls a background push's completion handler once: when the work ends or the time is nearly up.
    @MainActor
    final class Completion {
        private var handler: ((UIBackgroundFetchResult) -> Void)?
        init(_ handler: @escaping (UIBackgroundFetchResult) -> Void) { self.handler = handler }
        func finish(_ result: UIBackgroundFetchResult) {
            guard let handler else { return }
            self.handler = nil
            handler(result)
        }
    }

    /// `yansu {"kind":"ready","hub"}`, or nil for any other push.
    static func parse(_ userInfo: [AnyHashable: Any]) -> String? {
        guard let yansu = userInfo["yansu"] as? [String: Any], yansu["kind"] as? String == "ready",
              let hub = yansu["hub"] as? String, !hub.isEmpty else { return nil }
        return hub
    }

    /// Runs `refresh` for a ready push from the paired computer and reports the result iOS asks for; nil when the push
    /// is not a ready push, so the caller handles it.
    static func handle(_ userInfo: [AnyHashable: Any], pairedHub: String?,
                       refresh: () async -> Bool) async -> UIBackgroundFetchResult? {
        guard let hub = parse(userInfo) else { return nil }
        guard let pairedHub, pairedHub == hub else { return .noData }
        return await refresh() ? .newData : .failed
    }
}
#endif
