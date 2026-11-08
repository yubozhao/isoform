import Foundation

/// What the first read shows after Not quite and Send (P163, the first read correction v1.0.0).
enum FirstReadCorrectionPhase: Equatable, Sendable {
    /// Nothing sent: the card shows its buttons, or the correction field when Not quite is open.
    case none
    /// Sent and held for the computer (P156): the words, then "Waits for your computer" with Undo.
    case waiting(words: String)
    /// The computer has the answer. `words` is nil after a relaunch, when only the portrait line is known.
    case answered(words: String?)
    /// Held a day without the computer: the words, then "This waited a day" with Send again.
    case expired(words: String)
    /// The send failed: the field comes back with the words still in it.
    case failed(words: String)

    /// True while the card shows the sent words in place of its buttons or field.
    var showsAnswer: Bool {
        switch self {
        case .none, .failed: false
        case .waiting, .answered, .expired: true
        }
    }
}

/// How one send to the computer came back, as far as the first read cares.
enum FirstReadSendResult: Equatable, Sendable {
    /// The computer answered.
    case answered
    /// No answer yet. The journal keeps the write, so it waits for the computer.
    case held
    /// The write could not be saved, or the computer refused it.
    case failed
}

enum FirstReadCorrectionRules {
    /// Undo stays this long once the computer has the answer (the same 5 s as That's me).
    static let undoWindow: TimeInterval = 5

    /// The person's words, exactly, with only the outer whitespace removed.
    static func words(from field: String) -> String {
        field.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Maps a journaled send onto the first read's result. Nil when the caller was cancelled, since
    /// the card then keeps whatever it showed before.
    static func result<Value>(_ outcome: JournaledOutcome<Value>) -> FirstReadSendResult? {
        switch outcome {
        case .sent: .answered
        case .failed(_, .keep): .held
        case .failed, .notSaved: .failed
        case .cancelled: nil
        }
    }

    static func phase(after result: FirstReadSendResult, words: String) -> FirstReadCorrectionPhase {
        switch result {
        case .answered: .answered(words: words)
        case .held: .waiting(words: words)
        case .failed: .failed(words: words)
        }
    }

    static func undoOpen(answeredAt: Date?, now: Date) -> Bool {
        guard let answeredAt else { return false }
        return now.timeIntervalSince(answeredAt) < undoWindow
    }

    /// A correction the phone is still holding for the computer, read back from the journal after a relaunch:
    /// its words and when it was sent. Nil when no such correction is held.
    static func restoredWaiting(from entries: [PendingWrite], target: String, now: Date)
        -> (phase: FirstReadCorrectionPhase, sentAt: Date)? {
        guard let write = entries.last(where: {
            $0.target == target && $0.operation == .chatProactiveAction && $0.choice == "correct_portrait"
        }), let words = write.correctionWords else { return nil }
        let phase: FirstReadCorrectionPhase = expired(sentAt: write.createdAt, now: now)
            ? .expired(words: words) : .waiting(words: words)
        return (phase, write.createdAt)
    }

    /// The same day limit a held Today or chat send gets before the relay lets it go (ChatDelivery).
    static func expired(sentAt: Date, now: Date) -> Bool {
        now.timeIntervalSince(sentAt) >= TaskSendWait.relayRetention
    }
}
