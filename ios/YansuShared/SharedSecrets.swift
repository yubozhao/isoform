import Foundation

// Legacy pairings use the fixed accounts. New pairings stage a separate generation and publish its
// identifier in the record only after both secrets have been saved; the secrets themselves stay here.
enum PairingSecretAccount {
    static let devicePrivateKey = "device-private-key"
    static let deviceBearer = "device-bearer"
    static let pendingRevocations = "pairing-pending-revocations"
    static let irohTicketTransaction = "pairing-iroh-ticket-transaction"

    static func devicePrivateKey(secretID: String?) -> String {
        secretID.map { "\(devicePrivateKey).\($0)" } ?? devicePrivateKey
    }

    static func deviceBearer(secretID: String?) -> String {
        secretID.map { "\(deviceBearer).\($0)" } ?? deviceBearer
    }

    static func irohTicket(hubID: String) -> String {
        "iroh-ticket.\(hubID)"
    }

    static func isDevicePrivateKey(_ account: String) -> Bool {
        account == devicePrivateKey || account.hasPrefix("\(devicePrivateKey).")
    }

    static func isDeviceBearer(_ account: String) -> Bool {
        account == deviceBearer || account.hasPrefix("\(deviceBearer).")
    }

    static func directRoute(secretID: String?) -> String {
        "direct-route.\(secretID ?? "legacy")"
    }

    static func isDirectRoute(_ account: String) -> Bool {
        account.hasPrefix("direct-route.")
    }

    static func isIrohTicket(_ account: String) -> Bool {
        account.hasPrefix("iroh-ticket.")
    }
}

// Keychain accounts for what the notification service extension needs to open a sealed notice.
enum SharedNoticeAccount {
    static let key = "notice-key"
    static let context = "notice-context"
}

// Where the relay is and which pairing the notice belongs to. Stored as JSON in the shared Keychain.
struct NoticeRelayContext: Codable, Equatable, Sendable {
    let origin: String
    let hubID: String
    let deviceID: String
    var secretID: String? = nil
    var remote: Bool = false

    private enum CodingKeys: String, CodingKey {
        case origin, hubID, deviceID, secretID, remote
    }

    init(origin: String, hubID: String, deviceID: String, secretID: String? = nil, remote: Bool = false) {
        self.origin = origin
        self.hubID = hubID
        self.deviceID = deviceID
        self.secretID = secretID
        self.remote = remote
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        origin = try container.decode(String.self, forKey: .origin)
        hubID = try container.decode(String.self, forKey: .hubID)
        deviceID = try container.decode(String.self, forKey: .deviceID)
        secretID = try container.decodeIfPresent(String.self, forKey: .secretID)
        remote = try container.decodeIfPresent(Bool.self, forKey: .remote) ?? false
    }
}

struct NoticeSecrets: Equatable, Sendable {
    let key: Data
    let context: NoticeRelayContext
}

// kNotice and the relay context, written at pairing and checked at launch for pairings made by earlier builds.
// Both the app and the notification service extension read this through the shared access group.
enum SharedNoticeStore {
    // Derives kNotice from the device's X25519 private key and the hub's public key (the same secret S the
    // session keys come from), then stores it with the relay context. Nothing is written when the inputs are
    // malformed, so a bad pairing record never leaves a half-written notice key behind.
    static func provision(hubID: String, deviceID: String, relayOrigin: String, hubPublicKey: String,
                          devicePrivateKey: Data, secretID: String? = nil, remote: Bool = false,
                          secrets: SecretStore) throws {
        let hubPublic = try RelayCrypto.decodeKey(hubPublicKey)
        let key = try RelayCrypto.noticeKey(devicePrivate: devicePrivateKey, hubPublic: hubPublic,
                                            hubID: hubID, deviceID: deviceID)
        try save(key: key, context: NoticeRelayContext(origin: relayOrigin, hubID: hubID, deviceID: deviceID,
                                                     secretID: secretID, remote: remote),
                 secrets: secrets)
    }

    static func save(key: Data, context: NoticeRelayContext, secrets: SecretStore) throws {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        let contextText = String(decoding: try encoder.encode(context), as: UTF8.self)
        try secrets.save(RelayCrypto.base64URLEncode(key), account: SharedNoticeAccount.key)
        try secrets.save(contextText, account: SharedNoticeAccount.context)
    }

    // nil when nothing has been provisioned yet. A value that cannot be read is treated as missing, so the
    // notification falls back to the fixed text instead of failing the push.
    static func load(secrets: SecretStore) -> NoticeSecrets? {
        guard let keyText = try? secrets.read(SharedNoticeAccount.key),
              let contextText = try? secrets.read(SharedNoticeAccount.context),
              let key = try? RelayCrypto.decodeKey(keyText),
              let context = try? JSONDecoder().decode(NoticeRelayContext.self, from: Data(contextText.utf8)) else {
            return nil
        }
        return NoticeSecrets(key: key, context: context)
    }

    // Origin and bearer for the device routes that do not need an envelope (receipts).
    static func relayCredentials(secrets: SecretStore) -> RelayCredentials? {
        guard let loaded = load(secrets: secrets), !loaded.context.remote,
              let bearer = try? secrets.read(PairingSecretAccount.deviceBearer(secretID: loaded.context.secretID)),
              !bearer.isEmpty,
              let origin = URL(string: loaded.context.origin) else {
            return nil
        }
        return RelayCredentials(origin: origin, bearer: bearer)
    }

    static func clear(secrets: SecretStore) {
        try? secrets.delete(SharedNoticeAccount.key)
        try? secrets.delete(SharedNoticeAccount.context)
    }

    // True when the stored key and context already belong to this pairing.
    static func matches(hubID: String, deviceID: String, relayOrigin: String, secretID: String? = nil,
                        remote: Bool = false, secrets: SecretStore) -> Bool {
        guard let loaded = load(secrets: secrets) else { return false }
        return loaded.context == NoticeRelayContext(origin: relayOrigin, hubID: hubID, deviceID: deviceID,
                                                     secretID: secretID, remote: remote)
    }
}
