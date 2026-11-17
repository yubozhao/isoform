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
}

struct PhoneHoldAction: Encodable, Sendable {
    let idempotencyKey: String
    let action: String
    var token: String? = nil
    var slot: PhoneHoldSlot? = nil
    var calendarId: String? = nil
    var holdId: String? = nil
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
