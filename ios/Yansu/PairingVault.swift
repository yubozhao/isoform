import Darwin
import Foundation

// The computer's direct path for a relay pairing: its iroh ticket and a bearer only its own relay knows.
struct DirectRoute: Codable, Equatable, Sendable {
    let ticket: String
    let bearer: String
}

// Sequence state for one paired hub (PROTOCOL.md section 4). `lastAcceptedBrief` and
// `lastAcceptedMessage` are the per-stream receive cursors; `sendSeq` is the device's own
// strictly increasing counter, shared by both directions of nonces.
struct SequenceCounters: Codable, Equatable, Sendable {
    var sendSeq: UInt64 = 0
    var lastAcceptedBrief: UInt64 = 0
    var lastAcceptedMessage: UInt64 = 0
}

struct PairedDevice: Codable, Equatable, Sendable {
    var hubID: String
    var hubName: String
    var hubPublicKey: String
    var relayOrigin: String
    var deviceID: String
    var deviceName: String
    var scopes: [String]
    var pairedAt: Date
    var expiresAt: Date?
    var counters: SequenceCounters
    var secretID: String? = nil
    var previousDeviceRemovalNeeded: Bool? = nil
}

enum BriefAcceptance: Equatable {
    case fresh
    case known
}

enum PairingVaultError: LocalizedError, Equatable {
    case unreadableRecord

    var errorDescription: String? {
        switch self {
        case .unreadableRecord:
            return "Isoform could not read the saved pairing. Your pairing has not been removed. Unlock your phone or update Isoform, then try again."
        }
    }
}

// A bearer needed only to finish removing a superseded device. It stays in the Keychain across
// replacement and launch until the relay acknowledges the revoke.
struct PendingDeviceRevocation: Codable, Equatable, Sendable {
    let deviceID: String
    let origin: URL
    let bearer: String
    var irohTicket: String?

    init(deviceID: String, origin: URL, bearer: String, irohTicket: String? = nil) {
        self.deviceID = deviceID
        self.origin = origin
        self.bearer = bearer
        self.irohTicket = irohTicket
    }

    var credentials: RelayCredentials { RelayCredentials(origin: origin, bearer: bearer) }
}

// Owns the paired record (Application Support, complete until first unlock) and the
// device secrets (Keychain). Every counter update is persisted before the value is used,
// so a sequence number is never reused after a crash.
final class PairingVault: @unchecked Sendable {
    private struct IrohTicketTransaction: Codable {
        let hubID: String
        let secretID: String
        let previousTicket: String?
    }

    private let lock = NSLock()
    private let directory: URL
    private let secrets: SecretStore
    private let remoteOrigin: @Sendable (String) -> URL?
    private let startRemote: @Sendable (String) -> Void
    private let readRecord: (URL) throws -> Data
    private let writeRecord: (Data, URL) throws -> Void
    private var record: PairedDevice?
    private var loaded = false
    private var cachedSession: SessionContext?
    /// Whether a relay pairing with a direct route should use it first. It starts on the relay at launch and
    /// moves only after a failed request.
    private var preferDirect = false
    private var recordLoadError: PairingVaultError?

    init(directory: URL, secrets: SecretStore,
         remoteOrigin: @escaping @Sendable (String) -> URL? = {
             RemoteAccess.shared.localOrigin(ticket: $0)
         },
         startRemote: @escaping @Sendable (String) -> Void = { ticket in
             Task { _ = await RemoteAccess.shared.origin(ticket: ticket) }
         },
         readRecord: @escaping (URL) throws -> Data = { try Data(contentsOf: $0) },
         writeRecord: @escaping (Data, URL) throws -> Void = { data, url in
             try data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
         }) {
        self.directory = directory
        self.secrets = secrets
        self.remoteOrigin = remoteOrigin
        self.startRemote = startRemote
        self.readRecord = readRecord
        self.writeRecord = writeRecord
    }

    static var defaultDirectory: URL {
        let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return support.appendingPathComponent("Isoform", isDirectory: true)
    }

    private var fileURL: URL {
        directory.appendingPathComponent("pairing.json")
    }

    private static let encoder: JSONEncoder = {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.sortedKeys]
        return encoder
    }()

    private static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return decoder
    }()

    private func locked<T>(_ body: () throws -> T) rethrows -> T {
        lock.lock()
        defer { lock.unlock() }
        return try body()
    }

    // MARK: Reads

    // An unreadable record is not an unpaired phone. Keep its failure stable until an explicit
    // retry so callers can present recovery without accidentally starting a replacement claim.
    var loadError: PairingVaultError? {
        locked {
            loadIfNeeded()
            return recordLoadError
        }
    }

    @discardableResult
    func retryLoading() -> PairedDevice? {
        locked {
            if recordLoadError != nil { loaded = false }
            loadIfNeeded()
            return record
        }
    }

    func current() -> PairedDevice? {
        locked {
            loadIfNeeded()
            return record
        }
    }

    func lastAcceptedMessage() -> UInt64 {
        locked {
            loadIfNeeded()
            return record?.counters.lastAcceptedMessage ?? 0
        }
    }

    func lastAcceptedMessage(deviceID: String) throws -> UInt64 {
        try locked {
            try requireReadableRecord()
            guard let record, record.deviceID == deviceID else { throw HubRequestError.notPaired }
            return record.counters.lastAcceptedMessage
        }
    }

    func lastAcceptedBrief() -> UInt64 {
        locked {
            loadIfNeeded()
            return record?.counters.lastAcceptedBrief ?? 0
        }
    }

    func session() throws -> SessionContext {
        try locked {
            try requireReadableRecord()
            guard let record else { throw HubRequestError.notPaired }
            let baseContext: SessionContext
            if let cachedSession {
                baseContext = cachedSession
            } else {
                guard let bearer = try secrets.read(PairingSecretAccount.deviceBearer(secretID: record.secretID)),
                      let privateKey = try secrets.read(PairingSecretAccount.devicePrivateKey(secretID: record.secretID)),
                      let origin = URL(string: record.relayOrigin) else {
                    throw HubRequestError.notPaired
                }
                let keys = try RelayCrypto.sessionKeys(
                    devicePrivate: RelayCrypto.decodeKey(privateKey),
                    hubPublic: RelayCrypto.decodeKey(record.hubPublicKey),
                    hubID: record.hubID, deviceID: record.deviceID)
                baseContext = SessionContext(credentials: RelayCredentials(origin: origin, bearer: bearer),
                                             hubID: record.hubID, deviceID: record.deviceID, keys: keys)
                cachedSession = baseContext
            }
            guard let ticket = try secrets.read(PairingSecretAccount.irohTicket(hubID: record.hubID)) else {
                // A relay pairing that also holds a direct route uses whichever path worked last. While the
                // direct tunnel is still opening, the relay serves the request.
                guard preferDirect, let direct = try readDirectRouteLocked(record) else { return baseContext }
                guard let origin = remoteOrigin(direct.ticket) else {
                    startRemote(direct.ticket)
                    return baseContext
                }
                return SessionContext(credentials: RelayCredentials(origin: origin, bearer: direct.bearer),
                                      hubID: baseContext.hubID, deviceID: baseContext.deviceID, keys: baseContext.keys,
                                      route: .direct)
            }
            guard let origin = remoteOrigin(ticket) else {
                startRemote(ticket)
                throw RelayClientError.offline
            }
            return SessionContext(credentials: RelayCredentials(origin: origin, bearer: baseContext.credentials.bearer),
                                  hubID: baseContext.hubID, deviceID: baseContext.deviceID, keys: baseContext.keys,
                                  route: .direct)
        }
    }

    // MARK: Direct route beside the relay (PROTOCOL.md section 21.4)

    /// Called after a request over `context` failed on the way to the computer. When this pairing has a second
    /// path, later sessions use it and the call returns true, so the caller may try once more.
    func switchRoute(after context: SessionContext) -> Bool {
        let (switched, ticket): (Bool, String?) = locked {
            guard let record, record.deviceID == context.deviceID,
                  (try? secrets.read(PairingSecretAccount.irohTicket(hubID: record.hubID))) == nil,
                  let direct = try? readDirectRouteLocked(record) else { return (false, nil) }
            switch context.route {
            case .relay:
                preferDirect = true
                return (true, direct.ticket)
            case .direct:
                preferDirect = false
                return (true, nil)
            }
        }
        if let ticket { startRemote(ticket) }
        return switched
    }

    /// True when this relay pairing also holds the computer's direct path.
    var hasDirectRoute: Bool {
        locked {
            loadIfNeeded()
            guard let record else { return false }
            return (try? readDirectRouteLocked(record)) != nil
        }
    }

    /// Saves the direct path the computer offered over the relay. A pairing that is already ticket-only
    /// (remote access mode) never needs one.
    func storeDirectRoute(_ route: DirectRoute, deviceID: String) throws {
        try locked {
            try requireReadableRecord()
            guard let record, record.deviceID == deviceID else { throw HubRequestError.notPaired }
            let data = try Self.encoder.encode(route)
            try secrets.save(String(decoding: data, as: UTF8.self),
                             account: PairingSecretAccount.directRoute(secretID: record.secretID))
        }
        startRemote(route.ticket)
    }

    /// The ticket the forwarder should keep open: the pairing's own, or its direct route's.
    func forwarderTicket() -> String? {
        if let ticket = try? irohTicket() { return ticket }
        return locked {
            loadIfNeeded()
            guard let record else { return nil }
            return (try? readDirectRouteLocked(record))?.ticket
        }
    }

    private func readDirectRouteLocked(_ record: PairedDevice) throws -> DirectRoute? {
        guard let text = try secrets.read(PairingSecretAccount.directRoute(secretID: record.secretID)) else { return nil }
        return try? Self.decoder.decode(DirectRoute.self, from: Data(text.utf8))
    }

    func irohTicket() throws -> String? {
        try locked {
            try requireReadableRecord()
            guard let record else { return nil }
            return try secrets.read(PairingSecretAccount.irohTicket(hubID: record.hubID))
        }
    }

    var isRemote: Bool {
        (try? irohTicket()) != nil
    }

    // Pairings made before notices existed have no notice key. The stored device key holds the same shared
    // secret the session keys come from, so the key is derived here. A pairing whose device key is gone keeps
    // the fixed notification text until it pairs again.
    func reconcileNoticeSecrets() {
        locked {
            loadIfNeeded()
            guard let record else { return }
            if SharedNoticeStore.matches(hubID: record.hubID, deviceID: record.deviceID,
                                         relayOrigin: record.relayOrigin, secretID: record.secretID,
                                         remote: (try? secrets.read(PairingSecretAccount.irohTicket(hubID: record.hubID))) != nil,
                                         secrets: secrets) {
                return
            }
            guard let text = try? secrets.read(PairingSecretAccount.devicePrivateKey(secretID: record.secretID)),
                  let privateKey = try? RelayCrypto.decodeKey(text) else { return }
            try? SharedNoticeStore.provision(hubID: record.hubID, deviceID: record.deviceID,
                                             relayOrigin: record.relayOrigin, hubPublicKey: record.hubPublicKey,
                                             devicePrivateKey: privateKey, secretID: record.secretID,
                                             remote: (try? secrets.read(PairingSecretAccount.irohTicket(hubID: record.hubID))) != nil,
                                             secrets: secrets)
        }
    }

    // True when this pairing can open sealed notices. Settings uses it to say when pairing again is needed.
    func hasNoticeSecrets() -> Bool {
        locked {
            loadIfNeeded()
            guard let record else { return false }
            return SharedNoticeStore.matches(hubID: record.hubID, deviceID: record.deviceID,
                                             relayOrigin: record.relayOrigin, secretID: record.secretID,
                                             remote: (try? secrets.read(PairingSecretAccount.irohTicket(hubID: record.hubID))) != nil,
                                             secrets: secrets)
        }
    }

    // The device private key, needed once during pairing and for nothing else afterwards.
    func devicePrivateKey() throws -> Data? {
        try locked {
            try requireReadableRecord()
            guard let record,
                  let text = try secrets.read(PairingSecretAccount.devicePrivateKey(secretID: record.secretID)) else {
                return nil
            }
            return try RelayCrypto.decodeKey(text)
        }
    }

    func pendingDeviceRevocations() throws -> [PendingDeviceRevocation] {
        try locked {
            try requireReadableRecord()
            return try readPendingRevocationsLocked().filter { $0.deviceID != record?.deviceID }
        }
    }

    func enqueueDeviceRevocation(deviceID: String, credentials: RelayCredentials,
                                 irohTicket: String? = nil) throws {
        try locked {
            try enqueueDeviceRevocationLocked(deviceID: deviceID, credentials: credentials,
                                              irohTicket: irohTicket)
        }
    }

    func completeDeviceRevocation(deviceID: String) throws {
        try locked {
            let remaining = try readPendingRevocationsLocked().filter { $0.deviceID != deviceID }
            try savePendingRevocationsLocked(remaining)
        }
    }

    // MARK: Writes

    // Stage new accounts before publishing their identifier. A failed write or a crash before the
    // atomic record replacement leaves the previous record and its secrets together and usable.
    func store(_ paired: PairedDevice, devicePrivateKey: String, bearer: String,
               irohTicket: String? = nil) throws {
        try locked {
            try requireReadableRecord()
            let previous = record
            let previousRevocations = try secrets.read(PairingSecretAccount.pendingRevocations)
            var replacement = paired
            if let previous, previous.deviceID != paired.deviceID {
                // Reading the bearer directly also works when the old private key is missing and
                // session() cannot derive encryption keys. A restored phone may have neither secret;
                // allow recovery, but retain guidance to remove the old device from the Mac manually.
                // An actual Keychain read error still aborts rather than being treated as missing.
                let previousBearer = try secrets.read(PairingSecretAccount.deviceBearer(secretID: previous.secretID))
                if let previousBearer, !previousBearer.isEmpty {
                    guard let origin = URL(string: previous.relayOrigin) else {
                        throw YansuError.message("Isoform could not read the previous pairing's credentials. Unlock your phone and try again.")
                    }
                    let previousIrohTicket = try secrets.read(PairingSecretAccount.irohTicket(hubID: previous.hubID))
                    try enqueueDeviceRevocationLocked(deviceID: previous.deviceID,
                        credentials: RelayCredentials(origin: origin, bearer: previousBearer),
                        irohTicket: previousIrohTicket)
                } else {
                    replacement.previousDeviceRemovalNeeded = true
                }
            }
            let secretID = UUID().uuidString.lowercased()
            replacement.secretID = secretID
            let privateAccount = PairingSecretAccount.devicePrivateKey(secretID: secretID)
            let bearerAccount = PairingSecretAccount.deviceBearer(secretID: secretID)
            let ticketAccount = PairingSecretAccount.irohTicket(hubID: replacement.hubID)
            let ticketTransaction = IrohTicketTransaction(
                hubID: replacement.hubID, secretID: secretID,
                previousTicket: try secrets.read(ticketAccount))
            do {
                let transaction = try Self.encoder.encode(ticketTransaction)
                try secrets.save(String(decoding: transaction, as: UTF8.self),
                                 account: PairingSecretAccount.irohTicketTransaction)
                try secrets.save(devicePrivateKey, account: privateAccount)
                try secrets.save(bearer, account: bearerAccount)
                if let irohTicket {
                    try secrets.save(irohTicket, account: ticketAccount)
                } else if ticketTransaction.previousTicket != nil {
                    try secrets.delete(ticketAccount)
                }
                try persistLocked(replacement)
                try? secrets.delete(PairingSecretAccount.irohTicketTransaction)
            } catch {
                let persisted: PairedDevice?
                let definitelyAbsent: Bool
                do {
                    persisted = try readPersistedRecord()
                    definitelyAbsent = false
                } catch {
                    persisted = nil
                    definitelyAbsent = recordIsDefinitelyAbsent(after: error)
                }
                if persisted?.secretID != replacement.secretID {
                    // A write can report failure after the atomic replacement. If the record
                    // cannot be read, leave the staged credentials for the next launch to reconcile.
                    if persisted != nil || definitelyAbsent {
                        try? secrets.delete(bearerAccount)
                        try? secrets.delete(privateAccount)
                        if let previousRevocations {
                            try? secrets.save(previousRevocations, account: PairingSecretAccount.pendingRevocations)
                        } else {
                            try? secrets.delete(PairingSecretAccount.pendingRevocations)
                        }
                        try? rollbackIrohTicketTransactionLocked()
                    } else {
                        // Either generation may now be current. Recovery must identify the record
                        // before serving a session, replacing it again, or revoking any queued device.
                        record = nil
                        cachedSession = nil
                        recordLoadError = .unreadableRecord
                    }
                    throw error
                }
                // The replacement is already durable. Publish it normally, preserving its secrets.
                try? secrets.delete(PairingSecretAccount.irohTicketTransaction)
            }
            record = replacement
            cachedSession = nil
            recordLoadError = nil
            // The notice key lets the extension open sealed pushes. Without it the push shows the fixed text,
            // so a failure here never fails the pairing. Provision only after the pairing is committed.
            if let privateKey = try? RelayCrypto.decodeKey(devicePrivateKey) {
                try? SharedNoticeStore.provision(hubID: paired.hubID, deviceID: paired.deviceID,
                                                 relayOrigin: paired.relayOrigin, hubPublicKey: paired.hubPublicKey,
                                                 devicePrivateKey: privateKey, secretID: replacement.secretID,
                                                 remote: irohTicket != nil,
                                                 secrets: secrets)
            }
            if let previous {
                try? secrets.delete(PairingSecretAccount.devicePrivateKey(secretID: previous.secretID))
                try? secrets.delete(PairingSecretAccount.deviceBearer(secretID: previous.secretID))
            }
            sweepUnusedPairingSecretsLocked()
        }
    }

    func nextSendSeq(deviceID: String? = nil) throws -> UInt64 {
        try locked {
            try requireReadableRecord()
            guard var current = record else { throw HubRequestError.notPaired }
            guard deviceID == nil || current.deviceID == deviceID else { throw HubRequestError.notPaired }
            // The relay bounds seq to 2^53-1. Past that the pairing must be replaced, not reused.
            guard current.counters.sendSeq < RelayCrypto.maxSequence else { throw RelayCryptoError.sequenceExhausted }
            current.counters.sendSeq += 1
            try persistLocked(current)
            record = current
            return current.counters.sendSeq
        }
    }

    // Requests and responses share one cursor. A response at or below it is a replay.
    func acceptMessage(seq: UInt64, deviceID: String? = nil) throws {
        try locked {
            try requireReadableRecord()
            if let deviceID, record?.deviceID != deviceID { throw HubRequestError.notPaired }
            guard var current = record, seq > current.counters.lastAcceptedMessage,
                  seq <= RelayCrypto.maxSequence else {
                throw RelayCryptoError.sequenceReplayed
            }
            current.counters.lastAcceptedMessage = seq
            try persistLocked(current)
            record = current
        }
    }

    // The latest brief may be re-fetched (`known`). An older one is a rollback and is refused.
    func acceptBrief(seq: UInt64, deviceID: String? = nil) throws -> BriefAcceptance {
        try locked {
            try requireReadableRecord()
            if let deviceID, record?.deviceID != deviceID { throw HubRequestError.notPaired }
            guard var current = record, seq >= current.counters.lastAcceptedBrief,
                  seq <= RelayCrypto.maxSequence else {
                throw RelayCryptoError.sequenceReplayed
            }
            if seq == current.counters.lastAcceptedBrief { return .known }
            current.counters.lastAcceptedBrief = seq
            try persistLocked(current)
            record = current
            return .fresh
        }
    }

    func wipe() throws {
        try locked {
            loadIfNeeded()
            do {
                try FileManager.default.removeItem(at: fileURL)
            } catch {
                guard recordIsDefinitelyAbsent(after: error) else { throw error }
            }
            record = nil
            loaded = true
            cachedSession = nil
            recordLoadError = nil
            try? secrets.delete(PairingSecretAccount.irohTicketTransaction)
            sweepUnusedPairingSecretsLocked()
            SharedNoticeStore.clear(secrets: secrets)
        }
    }

    // MARK: Persistence (callers hold the lock)

    private func loadIfNeeded() {
        guard !loaded else { return }
        loaded = true
        do {
            var loadedRecord = try readPersistedRecord()
            try reconcileIrohTicketTransactionLocked(with: loadedRecord)
            // Section 4: after a restart, skip the send counter ahead to cover an unflushed write.
            loadedRecord.counters.sendSeq = min(loadedRecord.counters.sendSeq &+ 1000, RelayCrypto.maxSequence)
            record = loadedRecord
            recordLoadError = nil
            sweepUnusedPairingSecretsLocked()
            try? persistLocked()
        } catch {
            record = nil
            cachedSession = nil
            if recordIsDefinitelyAbsent(after: error) {
                // No saved record means no pairing to protect. A cleanup record that cannot be read now is not
                // a broken pairing: it stays in the Keychain until the next launch reconciles it, and the next
                // store replaces it. Reporting it as unreadable would block a first pairing on a fresh install.
                try? reconcileIrohTicketTransactionLocked(with: nil)
                recordLoadError = nil
                sweepUnusedPairingSecretsLocked()
                SharedNoticeStore.clear(secrets: secrets)
            } else {
                // A locked/protected file, transient I/O failure, or newer record schema may
                // become readable later. None authorizes deletion of any pairing or notice secret.
                recordLoadError = .unreadableRecord
            }
        }
    }

    private func readPersistedRecord() throws -> PairedDevice {
        try Self.decoder.decode(PairedDevice.self, from: readRecord(fileURL))
    }

    private func requireReadableRecord() throws {
        loadIfNeeded()
        if let recordLoadError { throw recordLoadError }
    }

    private func recordIsDefinitelyAbsent(after error: Error) -> Bool {
        // A record can appear after a failed read. An existing path always prevents cleanup;
        // a false existence check only confirms absence when the read also reported ENOENT.
        Self.isMissingRecord(error) && !FileManager.default.fileExists(atPath: fileURL.path)
    }

    private static func isMissingRecord(_ error: Error) -> Bool {
        let error = error as NSError
        switch error.domain {
        case NSCocoaErrorDomain:
            guard error.code == CocoaError.Code.fileReadNoSuchFile.rawValue ||
                error.code == CocoaError.Code.fileNoSuchFile.rawValue else { return false }
            if let underlying = error.userInfo[NSUnderlyingErrorKey] as? NSError,
               underlying.domain == NSPOSIXErrorDomain {
                return underlying.code == Int(ENOENT)
            }
            return true
        case NSPOSIXErrorDomain:
            return error.code == Int(ENOENT)
        default:
            return false
        }
    }

    private func sweepUnusedPairingSecretsLocked() {
        guard let accounts = try? secrets.accountNames() else { return }
        let retained = record.map {
            Set([PairingSecretAccount.devicePrivateKey(secretID: $0.secretID),
                 PairingSecretAccount.deviceBearer(secretID: $0.secretID)])
        } ?? []
        let retainedTicket = record.map { PairingSecretAccount.irohTicket(hubID: $0.hubID) }
        let retainedRoute = record.map { PairingSecretAccount.directRoute(secretID: $0.secretID) }
        for account in accounts {
            if PairingSecretAccount.isDirectRoute(account) {
                if account != retainedRoute { try? secrets.delete(account) }
            } else if !retained.contains(account) &&
                (PairingSecretAccount.isDevicePrivateKey(account) || PairingSecretAccount.isDeviceBearer(account)) {
                try? secrets.delete(account)
            } else if PairingSecretAccount.isIrohTicket(account), account != retainedTicket {
                try? secrets.delete(account)
            }
        }
    }

    private func readPendingRevocationsLocked() throws -> [PendingDeviceRevocation] {
        guard let text = try secrets.read(PairingSecretAccount.pendingRevocations) else { return [] }
        return try Self.decoder.decode([PendingDeviceRevocation].self, from: Data(text.utf8))
    }

    private func enqueueDeviceRevocationLocked(deviceID: String, credentials: RelayCredentials,
                                               irohTicket: String? = nil) throws {
        var pending = try readPendingRevocationsLocked().filter { $0.deviceID != deviceID }
        pending.append(PendingDeviceRevocation(deviceID: deviceID, origin: credentials.origin,
                                               bearer: credentials.bearer, irohTicket: irohTicket))
        try savePendingRevocationsLocked(pending)
    }

    private func rollbackIrohTicketTransactionLocked() throws {
        guard let text = try secrets.read(PairingSecretAccount.irohTicketTransaction) else { return }
        let transaction = try Self.decoder.decode(IrohTicketTransaction.self, from: Data(text.utf8))
        let account = PairingSecretAccount.irohTicket(hubID: transaction.hubID)
        if let previousTicket = transaction.previousTicket {
            try secrets.save(previousTicket, account: account)
        } else {
            try secrets.delete(account)
        }
        try secrets.delete(PairingSecretAccount.irohTicketTransaction)
    }

    private func reconcileIrohTicketTransactionLocked(with current: PairedDevice?) throws {
        guard let text = try secrets.read(PairingSecretAccount.irohTicketTransaction) else { return }
        let transaction = try Self.decoder.decode(IrohTicketTransaction.self, from: Data(text.utf8))
        if current?.secretID == transaction.secretID {
            try secrets.delete(PairingSecretAccount.irohTicketTransaction)
        } else {
            try rollbackIrohTicketTransactionLocked()
        }
    }

    private func savePendingRevocationsLocked(_ pending: [PendingDeviceRevocation]) throws {
        guard !pending.isEmpty else {
            try secrets.delete(PairingSecretAccount.pendingRevocations)
            return
        }
        try secrets.save(String(decoding: Self.encoder.encode(pending), as: UTF8.self),
                         account: PairingSecretAccount.pendingRevocations)
    }

    private func persistLocked() throws {
        guard let record else { return }
        try persistLocked(record)
    }

    private func persistLocked(_ record: PairedDevice) throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let data = try Self.encoder.encode(record)
        try writeRecord(data, fileURL)
    }
}
