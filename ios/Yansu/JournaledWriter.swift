import Foundation

/// What to do with a journaled write after the hub answered with an error.
enum WriteDisposition: Equatable, Sendable {
    /// No answer, a busy relay or a server error: keep the key so the next attempt reuses it.
    case keep
    /// The hub refused the write for good: forget the key.
    case drop
    /// The record changed on the Mac: forget the key and read the Mac again.
    case changed
}

enum JournaledOutcome<Value> {
    case sent(Value)
    /// The journal could not save the write, so nothing was sent.
    case notSaved
    /// The caller was cancelled. The key stays in the journal.
    case cancelled
    case failed(Error, WriteDisposition)
}

/// Runs one write under a journaled idempotency key. Every write the phone makes goes through here, so a
/// retry after a lost answer reuses the same key and the Mac applies it once.
struct JournaledWriter: Sendable {
    let journal: PendingWriteJournal
    let makeKey: @Sendable () -> String
    let now: @Sendable () -> Date

    func send<Value>(_ operation: PendingWrite.Operation, target: String, choice: String, fingerprint: String,
                     correctionWords: String? = nil,
                     _ body: (String) async throws -> Value) async -> JournaledOutcome<Value> {
        let write: PendingWrite
        do {
            write = try await reserve(operation, target: target, choice: choice, fingerprint: fingerprint,
                                      correctionWords: correctionWords)
        } catch {
            return .notSaved
        }
        do {
            let value = try await HubRequestScope.$hubID.withValue(write.hubID) {
                try await body(write.key)
            }
            await journal.remove(keys: [write.key])
            return .sent(value)
        } catch is CancellationError {
            return .cancelled
        } catch {
            let disposition = Self.disposition(for: error)
            if disposition != .keep {
                await journal.remove(keys: [write.key])
            }
            return .failed(error, disposition)
        }
    }

    private func reserve(_ operation: PendingWrite.Operation, target: String, choice: String,
                         fingerprint: String, correctionWords: String? = nil) async throws -> PendingWrite {
        try await journal.reserve(operation: operation, target: target, choice: choice, fingerprint: fingerprint,
                                  makeKey: makeKey, createdAt: now(), correctionWords: correctionWords)
    }

    static func disposition(for error: Error) -> WriteDisposition {
        if let hubError = error as? HubRequestError {
            switch hubError {
            case .conflict, .confirmSend:
                return .changed
            case .forbidden, .notPaired, .rejectedEnvelope, .invalidIdentifier:
                return .drop
            case .rejected(let status, _):
                return status == 429 || status >= 500 ? .keep : .drop
            case .noAnswer, .queueFull, .malformedResponse:
                return .keep
            }
        }
        if error is ChatHubError {
            return .drop
        }
        if let relayError = error as? RelayClientError {
            switch relayError {
            case .badRequest, .unauthorized, .forbidden, .notFound, .conflict, .gone,
                 .payloadTooLarge, .unsupportedMediaType:
                return .drop
            default:
                return .keep
            }
        }
        return .keep
    }
}
