import Foundation
import Combine

struct PhoneHoldSlot: Codable, Equatable, Hashable, Sendable {
    let start: String
    let end: String
}

struct PhoneHoldThing: Codable, Equatable, Sendable {
    let title: String
    let sourceKind: String
    let sourceId: String
}

struct PhoneHoldProposal: Codable, Equatable, Sendable {
    let id: String
    let thing: PhoneHoldThing
    let calendarId: String
    let slots: [PhoneHoldSlot]
    var existing: PhoneHoldStatus? = nil
    var dismissed: Bool? = nil
}

struct PhoneHoldCalendar: Codable, Equatable, Identifiable, Sendable {
    let id: String
    let title: String
    var meetingsAccount: Bool? = nil
}

struct PhoneHoldOffer: Codable, Equatable, Identifiable, Sendable {
    var id: String { proposal.id }
    let proposal: PhoneHoldProposal
    let calendars: [PhoneHoldCalendar]
    var why: String? = nil
    let token: String
}

struct PhoneHoldStatus: Codable, Equatable, Sendable {
    let id: String
    let state: String
    var taskId: String? = nil
    var calendarId: String? = nil
    var title: String? = nil
    var start: String? = nil
    var end: String? = nil
    var writeAfter: String? = nil
    var removedAt: String? = nil
    var failureReason: String? = nil
    /// The event that filled the chosen slot, when the computer named it.
    var conflictTitle: String? = nil
    var conflictStart: String? = nil

    var canUndoHold: Bool {
        state == HoldsStore.queuedState || state == "pending_write" && (writeAfter.flatMap(RelayTime.parse).map { $0 > Date() } ?? false)
    }
}

struct PhoneHoldAction: Encodable, Sendable {
    let idempotencyKey: String
    let action: String
    var token: String? = nil
    var slot: PhoneHoldSlot? = nil
    var calendarId: String? = nil
    var holdId: String? = nil
}

protocol HoldsHubAPI: Sendable {
    func holdProposal(kind: String, id: String) async throws -> PhoneHoldOffer
    func holdStatus(id: String) async throws -> PhoneHoldStatus
    func holdAction(_ action: PhoneHoldAction) async throws -> PhoneHoldStatus
}

extension HubChannel: HoldsHubAPI {
    func holdProposal(kind: String, id: String) async throws -> PhoneHoldOffer {
        guard Identifiers.isSafePathID(id) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.holdProposal(kind: kind, id: id)).payload(PhoneHoldOffer.self)
    }
    func holdStatus(id: String) async throws -> PhoneHoldStatus {
        guard Identifiers.isSafePathID(id) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.holdStatus(id: id)).payload(PhoneHoldStatus.self)
    }
    func holdAction(_ action: PhoneHoldAction) async throws -> PhoneHoldStatus {
        try await post("/holds/actions", action, as: PhoneHoldStatus.self)
    }
}

/// A failure shown on the card or the thread, in place (handoff 8). The count makes the second try read
/// "Still didn't work."
enum PhoneHoldFailure: Equatable, Sendable {
    case hold(Int)
    case remove(Int)
    case restore(Int)
    case calendarAccessOff

    var retries: Bool { self != .calendarAccessOff }
}

@MainActor
final class HoldsStore: ObservableObject {
    /// A queued hold the computer has not seen: it goes when the computer is back, unless it expires first.
    static let queuedState = "queued"
    /// A queued hold whose time passed, or that waited 24 hours: it never goes (handoff 7.3).
    static let expiredState = "expired"
    static let queueLifetime: TimeInterval = 24 * 60 * 60

    @Published private(set) var checking: Set<String> = []
    @Published private(set) var states: [String: PhoneHoldStatus] = [:]
    @Published private(set) var failures: [String: PhoneHoldFailure] = [:]
    private let hub: HoldsHubAPI
    private let journal: PendingWriteJournal
    private let writer: JournaledWriter
    private let pairingID: @MainActor () -> String?
    private let now: () -> Date
    private var resending: Task<Void, Never>?
    var onChange: (@MainActor () async -> Void)?

    init(hub: HoldsHubAPI, journal: PendingWriteJournal, pairingID: @escaping @MainActor () -> String?,
         now: @escaping () -> Date = Date.init) {
        self.hub = hub
        self.journal = journal
        self.writer = JournaledWriter(journal: journal, makeKey: { UUID().uuidString.lowercased() }, now: now)
        self.pairingID = pairingID
        self.now = now
    }

    func proposal(kind: String, id: String) async -> PhoneHoldOffer? {
        try? await hub.holdProposal(kind: kind, id: id)
    }

    func place(_ offer: PhoneHoldOffer, slot: PhoneHoldSlot, calendarID: String) async -> PhoneHoldStatus? {
        let id = offer.proposal.id
        guard let pairing = pairingID(), !checking.contains(id) else { return nil }
        checking.insert(id)
        defer { checking.remove(id) }
        let fingerprint = WriteFingerprint.make([offer.token, slot.start, slot.end, calendarID])
        let result = await writer.send(.calendarHold, target: id, choice: "hold", fingerprint: fingerprint) { key in
            try await hub.holdAction(PhoneHoldAction(idempotencyKey: key, action: "hold", token: offer.token, slot: slot, calendarId: calendarID))
        }
        guard pairingID() == pairing else { return nil }
        let status: PhoneHoldStatus
        switch result {
        case .sent(let sent):
            status = sent
            failures[id] = nil
        case .failed(let error, .keep) where Self.isAway(error):
            // Only a journaled write may promise to go later; the key and what it carries stay on the phone.
            let replay = PendingWrite.CalendarHoldReplay(proposalID: id, token: offer.token, title: offer.proposal.thing.title,
                                                         sourceKind: offer.proposal.thing.sourceKind, sourceID: offer.proposal.thing.sourceId,
                                                         start: slot.start, end: slot.end, calendarID: calendarID)
            try? await journal.attachCalendarHold(target: id, fingerprint: fingerprint, replay: replay)
            status = Self.status(replay, state: Self.queuedState)
            failures[id] = nil
            scheduleResend()
        case .failed(HubRequestError.conflict(let message), _) where message.contains("HOLD_TIME_PASSED"):
            status = PhoneHoldStatus(id: id, state: Self.expiredState, title: offer.proposal.thing.title, start: slot.start, end: slot.end)
        default:
            if case .hold(let n) = failures[id] { failures[id] = .hold(n + 1) } else { failures[id] = .hold(1) }
            return nil
        }
        states[id] = status
        if status.state == "pending_write" || status.state == "checking" {
            Task { await self.follow(id: id, pairing: pairing) }
        }
        await onChange?()
        return status
    }

    /// The holds queued in the journal by an earlier launch: shown as queued (or expired) again, and sent the next
    /// time the computer answers (`resendQueued`). Without this a hold queued before the app quit never went.
    func restoreQueued() async {
        guard pairingID() != nil else { return }
        for write in await queuedWrites() {
            guard let replay = write.calendarHold, states[replay.proposalID] == nil else { continue }
            if expired(write, replay) {
                await journal.remove(keys: [write.key])
                states[replay.proposalID] = Self.status(replay, state: Self.expiredState)
            } else {
                states[replay.proposalID] = Self.status(replay, state: Self.queuedState)
            }
        }
    }

    /// Sends every queued hold again, or marks it expired once its time passed or it waited 24 hours. Nothing
    /// queued, nothing done: it runs each time the computer answers.
    func resendQueued() async {
        guard pairingID() != nil else { return }
        let queued = await queuedWrites()
        guard !queued.isEmpty else { return }
        for write in queued {
            guard let replay = write.calendarHold else { continue }
            if expired(write, replay) {
                await journal.remove(keys: [write.key])
                states[replay.proposalID] = Self.status(replay, state: Self.expiredState)
                continue
            }
            if states[replay.proposalID] == nil { states[replay.proposalID] = Self.status(replay, state: Self.queuedState) }
            let offer = PhoneHoldOffer(
                proposal: PhoneHoldProposal(id: replay.proposalID,
                                            thing: PhoneHoldThing(title: replay.title, sourceKind: replay.sourceKind, sourceId: replay.sourceID),
                                            calendarId: replay.calendarID,
                                            slots: [PhoneHoldSlot(start: replay.start, end: replay.end)]),
                calendars: [], token: replay.token)
            let placed = await place(offer, slot: PhoneHoldSlot(start: replay.start, end: replay.end), calendarID: replay.calendarID)
            // The computer refused it (the offer changed, access was taken away): its key is gone, so it is no
            // longer queued. Left queued, the row would wait forever and the resend loop would refresh Tasks
            // every five seconds.
            if placed == nil, states[replay.proposalID]?.state == Self.queuedState,
               !(await journal.entries()).contains(where: { $0.key == write.key }) {
                states[replay.proposalID] = nil
            }
        }
        await onChange?()
    }

    private func queuedWrites() async -> [PendingWrite] {
        await journal.entries().filter { $0.operation == .calendarHold && $0.choice == "hold" && $0.calendarHold != nil }
    }

    private func expired(_ write: PendingWrite, _ replay: PendingWrite.CalendarHoldReplay) -> Bool {
        let start = RelayTime.parse(replay.start) ?? .distantPast
        return start <= now() || now().timeIntervalSince(write.createdAt) >= Self.queueLifetime
    }

    func undo(id: String) async -> Bool {
        guard let pairing = pairingID() else { return false }
        if states[id]?.state == Self.queuedState {
            // The computer never saw it: dropping the key is the whole undo.
            let keys = await journal.entries().filter { $0.operation == .calendarHold && $0.target == id && $0.choice == "hold" }.map(\.key)
            await journal.remove(keys: keys)
            states[id] = nil
            await onChange?()
            return true
        }
        let result = await writer.send(.calendarHold, target: id, choice: "undo_hold", fingerprint: WriteFingerprint.make([id, "undo_hold"])) { key in
            try await hub.holdAction(PhoneHoldAction(idempotencyKey: key, action: "undo_hold", holdId: id))
        }
        guard pairingID() == pairing, case .sent(let status) = result else { return false }
        states[id] = status
        await onChange?()
        return true
    }

    /// Forgets an expired hold so Ask again can start fresh.
    func clear(id: String) {
        states[id] = nil
        failures[id] = nil
    }

    func remove(id: String) async -> PhoneHoldStatus? {
        await change(id: id, action: "remove_hold", failure: PhoneHoldFailure.remove)
    }

    func undoRemove(id: String) async -> PhoneHoldStatus? {
        await change(id: id, action: "undo_remove_hold", failure: PhoneHoldFailure.restore)
    }

    func selectCalendar(_ offer: PhoneHoldOffer, calendarID: String) async -> Bool {
        let result = await writer.send(.calendarHold, target: offer.proposal.id, choice: "select_calendar",
                                       fingerprint: WriteFingerprint.make([offer.token, calendarID])) { key in
            try await hub.holdAction(PhoneHoldAction(idempotencyKey: key, action: "select_calendar", token: offer.token, calendarId: calendarID))
        }
        if case .sent = result { return true }
        return false
    }

    func watch(_ status: PhoneHoldStatus) {
        guard let pairing = pairingID() else { return }
        if let known = states[status.id], known.state != Self.queuedState || status.state == known.state { return }
        states[status.id] = status
        if status.state == "pending_write" || status.state == "checking" {
            Task { await self.follow(id: status.id, pairing: pairing) }
        }
    }

    private func change(id: String, action: String, failure: (Int) -> PhoneHoldFailure) async -> PhoneHoldStatus? {
        guard let pairing = pairingID(), !checking.contains(id) else { return nil }
        checking.insert(id)
        defer { checking.remove(id) }
        // Each try is its own write: a retry after a failure must reach Calendar again.
        let attempt = UUID().uuidString
        let result = await writer.send(.calendarHold, target: id, choice: action, fingerprint: WriteFingerprint.make([id, action, attempt])) { key in
            try await hub.holdAction(PhoneHoldAction(idempotencyKey: key, action: action, holdId: id))
        }
        guard pairingID() == pairing else { return nil }
        switch result {
        case .sent(let status):
            states[id] = status
            failures[id] = nil
            await onChange?()
            return status
        case .failed(HubRequestError.rejected(status: 503, _), _):
            failures[id] = .calendarAccessOff
        default:
            let previous: Int
            switch failures[id] {
            case .remove(let n) where action == "remove_hold": previous = n
            case .restore(let n) where action == "undo_remove_hold": previous = n
            default: previous = 0
            }
            failures[id] = failure(previous + 1)
        }
        return nil
    }

    private func scheduleResend() {
        guard resending == nil else { return }
        resending = Task { [weak self] in
            while let self, self.states.values.contains(where: { $0.state == Self.queuedState }) {
                do { try await Task.sleep(for: .seconds(5)) } catch { break }
                await self.resendQueued()
            }
            self?.resending = nil
        }
    }

    private static func isAway(_ error: Error) -> Bool {
        switch error as? HubRequestError {
        case .noAnswer, .notPaired, .queueFull: return true
        case .rejected(let status, _): return status >= 500
        default: return !(error is HubRequestError) && !(error is DecodingError)
        }
    }

    private static func status(_ replay: PendingWrite.CalendarHoldReplay, state: String) -> PhoneHoldStatus {
        PhoneHoldStatus(id: replay.proposalID, state: state, taskId: replay.sourceKind == "task" ? replay.sourceID : nil,
                        calendarId: replay.calendarID, title: replay.title,
                        start: replay.start, end: replay.end)
    }

    private func follow(id: String, pairing: String) async {
        for _ in 0..<30 {
            do { try await Task.sleep(for: .seconds(1)) } catch { return }
            guard pairingID() == pairing, states[id]?.state != "removed" else { return }
            do {
                let status = try await hub.holdStatus(id: id)
                guard pairingID() == pairing else { return }
                states[id] = status
                await onChange?()
                if status.state != "pending_write" && status.state != "checking" { return }
            } catch { return }
        }
    }
}

enum PhoneHoldCopy {
    /// One line in place (handoff 8); a second failure reads "Still didn't work." with Try again (v1.0.1 §9).
    static func failure(_ failure: PhoneHoldFailure) -> String? {
        switch failure {
        case .hold(let n): return n > 1 ? String(localized: "hold.stillFailed") : String(localized: "hold.failedLine")
        case .remove(let n): return n > 1 ? String(localized: "hold.stillFailed") : String(localized: "hold.removeFailed")
        case .restore(let n): return n > 1 ? String(localized: "hold.stillFailed") : String(localized: "hold.restoreFailed")
        case .calendarAccessOff: return String(localized: "hold.noAccess")
        }
    }

    static func when(_ slot: PhoneHoldSlot, now: Date = Date(), locale: Locale) -> String? {
        guard let start = RelayTime.parse(slot.start), let end = RelayTime.parse(slot.end), end > start else { return nil }
        let calendar = Calendar.current
        let day: String
        if calendar.isDate(start, inSameDayAs: now) {
            day = String(localized: "hold.today", locale: locale)
        } else if let tomorrow = calendar.date(byAdding: .day, value: 1, to: now), calendar.isDate(start, inSameDayAs: tomorrow) {
            day = String(localized: "hold.tomorrow", locale: locale)
        } else {
            day = start.formatted(Date.FormatStyle().weekday(.wide).locale(locale))
        }
        let range = DateIntervalFormatter()
        range.locale = locale
        range.dateStyle = .none
        range.timeStyle = .short
        return String(format: String(localized: "hold.when", locale: locale), locale: locale,
                      arguments: [day, range.string(from: start, to: end)])
    }
}
