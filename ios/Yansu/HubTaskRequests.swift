import Foundation

protocol TasksHubAPI: Sendable {
    func brainDumpAvailable() async throws -> Bool
    func parseBrainDump(text: String, idempotencyKey: String) async throws -> [BrainDumpItem]
    func addBrainDump(items: [BrainDumpItem], idempotencyKey: String) async throws
    func tasks() async throws -> MobileTasksPage
    func task(id: String) async throws -> MobileTaskDetail
    func stopTask(id: String, request: MobileTaskStopRequest) async throws -> MobileTask
    /// A reply that stays in its task (PROTOCOL.md 20.7b).
    func replyToTask(id: String, request: MobileTaskReplyRequest) async throws -> MobileTaskDetail
    /// One ··· item, a tapped choice, or "opened".
    func actOnTask(id: String, request: MobileTaskActionRequest) async throws -> MobileTaskDetail
}

extension HubChannel: TasksHubAPI {
    func brainDumpAvailable() async throws -> Bool { try await abilities().brainDump == true }

    func parseBrainDump(text: String, idempotencyKey: String) async throws -> [BrainDumpItem] {
        let body = try JSONEncoder().encode(BrainDumpParseRequest(idempotencyKey: idempotencyKey, text: text))
        return try await perform(.write(path: "/tasks/parse", body: String(decoding: body, as: UTF8.self)))
            .payload(BrainDumpParseResult.self).items
    }

    func addBrainDump(items: [BrainDumpItem], idempotencyKey: String) async throws {
        let body = try JSONEncoder().encode(BrainDumpAddRequest(idempotencyKey: idempotencyKey, items: items))
        _ = try await perform(.write(path: "/tasks/add", body: String(decoding: body, as: UTF8.self)))
            .payload(BrainDumpAddResult.self)
    }
    func tasks() async throws -> MobileTasksPage {
        try await perform(.tasks).payload(MobileTasksPage.self)
    }

    func task(id: String) async throws -> MobileTaskDetail {
        guard Identifiers.isSafePathID(id) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.task(id: id)).payload(MobileTaskDetail.self)
    }

    func stopTask(id: String, request: MobileTaskStopRequest) async throws -> MobileTask {
        guard Identifiers.isSafePathID(id) else { throw HubRequestError.invalidIdentifier }
        let body = try JSONEncoder().encode(request)
        return try await perform(.write(path: "/tasks/\(id)/stop",
                                        body: String(decoding: body, as: UTF8.self)))
            .payload(MobileTask.self)
    }

    func replyToTask(id: String, request: MobileTaskReplyRequest) async throws -> MobileTaskDetail {
        guard Identifiers.isSafePathID(id) else { throw HubRequestError.invalidIdentifier }
        let body = try JSONEncoder().encode(request)
        return try await perform(.write(path: "/tasks/\(id)/messages",
                                        body: String(decoding: body, as: UTF8.self)))
            .payload(MobileTaskDetail.self)
    }

    func actOnTask(id: String, request: MobileTaskActionRequest) async throws -> MobileTaskDetail {
        guard Identifiers.isSafePathID(id) else { throw HubRequestError.invalidIdentifier }
        let body = try JSONEncoder().encode(request)
        return try await perform(.write(path: "/tasks/\(id)/actions",
                                        body: String(decoding: body, as: UTF8.self)))
            .payload(MobileTaskDetail.self)
    }
}

extension TasksHubAPI {
    func brainDumpAvailable() async throws -> Bool { false }
    func parseBrainDump(text: String, idempotencyKey: String) async throws -> [BrainDumpItem] {
        throw HubRequestError.rejected(status: 404, message: "not found")
    }
    func addBrainDump(items: [BrainDumpItem], idempotencyKey: String) async throws {
        throw HubRequestError.rejected(status: 404, message: "not found")
    }
    /// A computer from before task threads has no reply route.
    func replyToTask(id: String, request: MobileTaskReplyRequest) async throws -> MobileTaskDetail {
        throw HubRequestError.rejected(status: 404, message: "not found")
    }

    func actOnTask(id: String, request: MobileTaskActionRequest) async throws -> MobileTaskDetail {
        throw HubRequestError.rejected(status: 404, message: "not found")
    }
}
