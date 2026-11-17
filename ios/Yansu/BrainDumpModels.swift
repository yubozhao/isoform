import SwiftUI

struct BrainDumpItem: Codable, Identifiable, Equatable, Sendable {
    let id: String
    let title: String
    let who: String
    var when: String? = nil
    var project: String? = nil
    var person: String? = nil
    var noRunner: Bool? = nil
}

struct BrainDumpParseRequest: Encodable { let idempotencyKey: String; let text: String }
struct BrainDumpParseResult: Decodable { let items: [BrainDumpItem] }
struct BrainDumpAddRequest: Encodable { let idempotencyKey: String; let items: [BrainDumpItem] }
struct BrainDumpAddResult: Decodable { let ids: [String] }
