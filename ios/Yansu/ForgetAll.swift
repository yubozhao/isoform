import Foundation

// Forget everything (Settings › Your data on the computer; PROTOCOL.md 28, envelope kind forget_all). The computer sends a sealed
// `forget_all` envelope; the phone deletes everything it keeps for that computer and tells the watch to delete its
// recordings, keeps the pairing, then confirms. The computer sends it again until it hears the confirm, so the phone
// may get the same job twice: it deletes again (nothing is left) and confirms again.

/// The envelope's plaintext.
struct ForgetAllJob: Codable, Equatable, Sendable {
    let v: Int
    let jobId: String
    var at: String? = nil

    /// A job the phone acts on: version 1 and an id that is safe in a path.
    var isValid: Bool { v == 1 && Identifiers.isSafePathID(jobId) }
}

/// What the phone reports it forgot, in the confirm.
enum ForgetAllPart: String, CaseIterable, Sendable {
    case briefs, audio, work, answers, chat, topics, today, widget, recordings, watch, notifications
}

struct ForgetAllConfirm: Codable, Equatable, Sendable {
    let idempotencyKey: String
    let forgot: [String]
}

struct ForgetAllConfirmed: Decodable, Equatable, Sendable {
    var confirmed: Bool? = nil
}

protocol ForgetAllAPI: Sendable {
    func confirmForgetAll(jobID: String, confirm: ForgetAllConfirm) async throws
}

extension HubChannel: ForgetAllAPI {
    func confirmForgetAll(jobID: String, confirm: ForgetAllConfirm) async throws {
        guard Identifiers.isSafePathID(jobID) else { throw HubRequestError.invalidIdentifier }
        let body = try JSONEncoder().encode(confirm)
        _ = try await perform(.write(path: "/forget-all/\(jobID)/done", body: String(decoding: body, as: UTF8.self)))
            .payload(ForgetAllConfirmed.self)
    }
}

/// Runs one job: erase, then confirm. A job whose erase failed is not confirmed, so the computer asks again.
enum ForgetAllRunner {
    /// The confirm's idempotency key is derived from the job, so a retried confirm of the same job is the same request.
    static func key(for job: ForgetAllJob) -> String { "forget-all-" + job.jobId }

    @MainActor
    static func run(_ job: ForgetAllJob, erase: () async -> [ForgetAllPart]?, api: ForgetAllAPI) async -> Bool {
        guard job.isValid, let forgot = await erase() else { return false }
        do {
            try await api.confirmForgetAll(jobID: job.jobId,
                                           confirm: ForgetAllConfirm(idempotencyKey: key(for: job), forgot: forgot.map(\.rawValue)))
            return true
        } catch {
            return false
        }
    }
}
