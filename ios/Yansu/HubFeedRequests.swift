import Foundation

// Audio metadata stays inside the authorized, encrypted bridge response. The bytes themselves
// are downloaded once from its sealed attachment; this phone never asks a speech model to run.
extension HubChannel {
    func briefAudio(brief: MobileBriefV1) async throws -> BriefAudioResource {
        do {
            let response = try await perform(.briefAudio(id: brief.identifier,
                                                         version: BriefAudioResource.versionKey(for: brief)))
            return try response.briefAudioPayload(for: brief)
        } catch HubRequestError.noAnswer {
            throw BriefAudioError.computerOffline
        } catch RelayClientError.offline {
            throw BriefAudioError.computerOffline
        } catch RelayClientError.timedOut {
            throw BriefAudioError.computerOffline
        }
    }
}

extension HubResponse {
    func briefAudioPayload(for brief: MobileBriefV1) throws -> BriefAudioResource {
        if status == 404 {
            struct Unavailable: Decodable { let reason: String? }
            let reason = (try? JSONDecoder().decode(HubEnvelope<Unavailable>.self, from: Data(body.utf8)))?.data?.reason
            switch reason {
            case "voice_not_installed": throw BriefAudioError.voiceNotInstalled
            case "audio_rendering": throw BriefAudioError.rendering
            default: throw BriefAudioError.notAvailable
            }
        }
        if status == 409 { throw BriefAudioError.versionChanged }
        let resource = try payload(BriefAudioResource.self)
        guard resource.briefId == brief.identifier,
              resource.version == BriefAudioResource.versionKey(for: brief) else {
            throw BriefAudioError.versionChanged
        }
        return resource
    }
}

/// Like or Not right on a Feed card (PROTOCOL.md section 20.3).
enum FeedFeedbackAction: String, Codable, Sendable {
    case like
    case notRight = "not_right"
    case notRelevant = "not_relevant"
}

struct BriefFeedbackRequest: Encodable, Equatable, Sendable {
    let idempotencyKey: String
    let action: String
    let reason: String?
}

/// What the Mac did with a feedback: `topic_more`, `topic_less`, `memory_removed` or `recorded`.
struct BriefFeedbackPayload: Decodable, Equatable, Sendable {
    let recorded: Bool
    let effect: String?
}

/// The Feed's writes, as the phone sees them. HubChannel conforms; tests substitute a stub.
protocol FeedHubAPI: Sendable {
    func sendFeedback(briefID: String, itemID: String, action: FeedFeedbackAction, reason: String?,
                      idempotencyKey: String) async throws -> BriefFeedbackPayload
}

extension HubChannel: FeedHubAPI {
    func sendFeedback(briefID: String, itemID: String, action: FeedFeedbackAction, reason: String?,
                      idempotencyKey: String) async throws -> BriefFeedbackPayload {
        guard Identifiers.isSafePathID(briefID), Identifiers.isSafePathID(itemID) else {
            throw HubRequestError.invalidIdentifier
        }
        if action == .notRelevant, try await abilities().briefRelevanceFeedback != true {
            throw HubRequestError.rejected(status: 404, message: "")
        }
        return try await post("/briefs/\(briefID)/items/\(itemID)/feedback",
                              BriefFeedbackRequest(idempotencyKey: idempotencyKey, action: action.rawValue,
                                                   reason: reason),
                              as: BriefFeedbackPayload.self)
    }
}
