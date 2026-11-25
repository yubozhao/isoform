import Foundation
import SwiftUI

// Scope cut A6 (docs/plans/scope-cut-2026-11.md section 6): the phone hides Chat when the computer stops
// offering it. The computer's `chat` switch (A1) leaves `chatMainSend`, `chatB10`, `scopeRequest` and
// `briefAsk` out of /status, and every route under /chats answers 404. An older computer always sends
// `chatMainSend`, so the phone keeps Chat there.

extension HubStatus {
    /// The one seam for Chat on the phone: Chat is on exactly when the computer sends `chatMainSend: true`.
    var chatAvailable: Bool { chatMainSend == true }
    /// The brief's Ask and Talk, read the same way.
    var briefAskAvailable: Bool { chatAvailable && briefAsk == true }
}

/// Whether the paired computer offers Chat, as an explicit state machine. `unknown` is a phone that has not
/// read /status since it was paired (or since this build); it keeps the screens it showed before.
enum ChatAvailability: Equatable, Codable, Sendable {
    case unknown
    case on(briefAsk: Bool)
    /// `cachePurged` is set once the phone's Chat cache is gone, so the purge runs once per switch-off.
    case off(cachePurged: Bool)

    enum Event: Equatable, Sendable {
        /// /status answered: the two flags A1 removes when Chat is off.
        case statusRead(chatMainSend: Bool?, briefAsk: Bool?)
        case purgeFinished
        case purgeFailed
        /// The phone was unpaired or forgot everything.
        case unpaired
    }

    enum Effect: Equatable, Sendable {
        case purgeChatCache
        case save
    }

    /// Chat's tab, the brief's Ask and Talk, question cards, Useful and Your week show unless the computer said
    /// Chat is off.
    var chatShown: Bool {
        switch self {
        case .unknown, .on: return true
        case .off: return false
        }
    }

    var briefAskShown: Bool {
        switch self {
        case .unknown: return true
        case .on(let briefAsk): return briefAsk
        case .off: return false
        }
    }

    /// The one transition function: pure, exhaustive, no default.
    static func transition(_ state: ChatAvailability, _ event: Event) -> (ChatAvailability, [Effect]) {
        switch (state, event) {
        case (_, .statusRead(chatMainSend: .some(true), let briefAsk)):
            let next = ChatAvailability.on(briefAsk: briefAsk == true)
            return (next, next == state ? [] : [.save])
        case (.unknown, .statusRead(chatMainSend: _, briefAsk: _)),
             (.on, .statusRead(chatMainSend: _, briefAsk: _)):
            return (.off(cachePurged: false), [.save, .purgeChatCache])
        case (.off(cachePurged: true), .statusRead(chatMainSend: _, briefAsk: _)):
            return (state, [])
        case (.off(cachePurged: false), .statusRead(chatMainSend: _, briefAsk: _)):
            // A purge that failed or was cut short by the app closing runs again.
            return (state, [.purgeChatCache])
        case (.off(cachePurged: false), .purgeFinished):
            return (.off(cachePurged: true), [.save])
        case (.off(cachePurged: true), .purgeFinished),
             (.unknown, .purgeFinished),
             (.on, .purgeFinished):
            return (state, [])
        case (.unknown, .purgeFailed), (.on, .purgeFailed), (.off, .purgeFailed):
            return (state, [])
        case (.unknown, .unpaired):
            return (state, [])
        case (.on, .unpaired), (.off, .unpaired):
            return (.unknown, [.save])
        }
    }
}

/// Holds the phone's ChatAvailability, saves it for the next launch so Chat does not flash in, and carries out
/// the effects. The purge removes only the Chat cache (ChatStore.purgeForChatOff): never the brief cache,
/// pairing, recordings or the watch.
@MainActor
final class ChatAvailabilityStore: ObservableObject {
    @Published private(set) var state: ChatAvailability
    private let readStatus: @MainActor () async throws -> HubStatus
    private let purge: @MainActor () -> Bool
    private let defaults: UserDefaults
    static let defaultsKey = "phone.chatAvailability"

    init(state: ChatAvailability? = nil, defaults: UserDefaults = .standard,
         readStatus: @escaping @MainActor () async throws -> HubStatus,
         purge: @escaping @MainActor () -> Bool) {
        self.defaults = defaults
        self.readStatus = readStatus
        self.purge = purge
        if let state {
            self.state = state
        } else if let data = defaults.data(forKey: Self.defaultsKey),
                  let saved = try? JSONDecoder().decode(ChatAvailability.self, from: data) {
            self.state = saved
        } else {
            self.state = .unknown
        }
    }

    var chatShown: Bool { state.chatShown }
    var briefAskShown: Bool { state.briefAskShown }

    func send(_ event: ChatAvailability.Event) {
        let (next, effects) = ChatAvailability.transition(state, event)
        state = next
        for effect in effects {
            switch effect {
            case .save:
                if let data = try? JSONEncoder().encode(state) { defaults.set(data, forKey: Self.defaultsKey) }
            case .purgeChatCache:
                send(purge() ? .purgeFinished : .purgeFailed)
            }
        }
    }

    func accept(_ status: HubStatus) {
        send(.statusRead(chatMainSend: status.chatMainSend, briefAsk: status.briefAsk))
    }

    /// Reads /status (shared with the other readers through HubChannel.abilities). A failed read changes nothing.
    func refresh() async {
        guard let status = try? await readStatus() else { return }
        accept(status)
    }
}

/// The phone's root pages: the one place the tab layout is decided (scope cut section 1: "Without a watch the
/// phone has one page and needs no tab bar. Isoform design decides that shape."). When design answers "one page
/// or two", change it here.
enum PhoneTabs {
    static func root(chatAvailable: Bool, watchPaired: Bool) -> [NotificationRouter.Tab] {
        var tabs: [NotificationRouter.Tab] = [.today]
        if watchPaired { tabs.append(.watch) }
        if chatAvailable { tabs.append(.chat) }
        return tabs
    }

    /// One page needs no tab bar.
    static func showsTabBar(_ tabs: [NotificationRouter.Tab]) -> Bool { tabs.count > 1 }

    /// The tab to show when the selected one is no longer there (the watch was unpaired, Chat was switched off).
    static func landing(_ selected: NotificationRouter.Tab, in tabs: [NotificationRouter.Tab]) -> NotificationRouter.Tab {
        switch selected {
        case .today, .watch, .chat: return tabs.contains(selected) ? selected : .today
        case .tasks, .settings: return selected
        }
    }
}

/// Scope cut B2 (phone half): Hear it is play, pause and a position bar. Speed, 15 s back and ahead and the
/// read-along highlight stay in the code behind this switch, the phone's side of the `brief-extras` idea, and
/// the phone does not show them. The audio file and its sentence marks are unchanged.
enum BriefPlayerExtras {
    static let onPhone = false
}
