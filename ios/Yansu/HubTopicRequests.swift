import Foundation

/// The RC 1 topic routes (PROTOCOL.md 15.8), as the phone sees them. HubChannel conforms; tests substitute a stub.
/// A computer without the routes answers 404, which the phone reads as "this computer has no topics yet", the way
/// it reads the brief-opener route.
protocol TopicHubAPI: Sendable {
    /// Whether the computer's `GET /status` carries `"topics": true`.
    func supportsTopics() async throws -> Bool
    func topics() async throws -> MobileTopicList
    /// The newest page without a cursor, otherwise the page strictly before it.
    func topic(id: String, cursor: String?, limit: Int) async throws -> MobileTopicPage
    /// Sends to the chat with the topic attached. The key comes from `PendingWriteJournal`.
    func sendTopicMessage(topicID: String, text: String, idempotencyKey: String) async throws -> ChatMessageAccepted
}

extension HubRequestError {
    /// The route or the topic is not on this computer (404).
    var isNotFound: Bool {
        if case .rejected(404, _) = self { return true }
        return false
    }

    /// Observing is paused on the computer, so it shows no topic and sends none (409 `TODAY_CHAT_OBSERVATION_PAUSED`).
    var isObservationPaused: Bool {
        if case .conflict(let message) = self { return message == "TODAY_CHAT_OBSERVATION_PAUSED" }
        return false
    }
}

extension HubChannel: TopicHubAPI {
    func supportsTopics() async throws -> Bool {
        try await abilities().topics == true
    }

    func topics() async throws -> MobileTopicList {
        try await perform(.topics).payload(MobileTopicList.self)
    }

    func topic(id: String, cursor: String?, limit: Int) async throws -> MobileTopicPage {
        try await perform(.topic(id: id, cursor: cursor, limit: limit)).payload(MobileTopicPage.self)
    }

    func sendTopicMessage(topicID: String, text: String, idempotencyKey: String) async throws -> ChatMessageAccepted {
        guard Identifiers.isSafePathID(topicID) else { throw HubRequestError.invalidIdentifier }
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        guard let body = String(data: try encoder.encode(TopicMessageRequest(idempotencyKey: idempotencyKey, text: text)),
                                encoding: .utf8) else {
            throw HubRequestError.malformedResponse
        }
        let response = try await perform(.write(path: "/topics/\(topicID)/messages", body: body))
        // A turn already running in the chat answers 409 "busy", as an ordinary chat send does.
        if response.status == 409,
           let error = try? JSONDecoder().decode(ChatErrorBody.self, from: Data(response.body.utf8)),
           error.error == "busy" {
            throw ChatHubError.busy
        }
        return try response.payload(ChatMessageAccepted.self)
    }
}
