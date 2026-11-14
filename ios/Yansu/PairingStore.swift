import Foundation
import UIKit

// Pairing state machine (PROTOCOL.md sections 3.5 and 5, status codes). Nothing is written to the vault
// until the hub's approval decrypts and names this device, and the approval is written before anything
// else happens with it: the relay serves an approval exactly once. A declined, expired or failed attempt
// leaves any previous pairing untouched.
enum PairingPhase: Equatable {
    case idle
    case claiming
    case waitingApproval(code: String)
    case paired
    case declined
    case expired
    case failed(String)
}

@MainActor
final class PairingStore: ObservableObject {
    @Published private(set) var phase: PairingPhase = .idle
    @Published private(set) var paired: PairedDevice?
    @Published private(set) var isRemote = false
    @Published private(set) var recoveryMessage: String?

    // `desk:read` and `decide` are retired with the Desk and no longer asked for. A pairing that holds them keeps working.
    static let requestedScopes = ["brief:read", "brief:refresh", "presence:read", "push",
                                  "chat:read", "chat:write", "memory:correct", "handoff:create",
                                  "brief:feedback", "offers:read", "offers:decide", "goals:read", "goals:write",
                                  "library:read"]

    // An identical claim returns 202 again, so a timed-out claim may be retried with the same body.
    static let claimAttempts = 3
    // While open, a pairing answers 409 until claimed. A short wait covers the claim being processed.
    static let maxPollConflicts = 5
    static let maxTransientPollFailures = 3
    static var retryDelayNanoseconds: UInt64 = 2_000_000_000

    private let vault: PairingVault
    private let transport: PairingTransport
    private let remoteOrigin: @Sendable (String) async -> URL?
    private let activateRemote: @Sendable (String?) -> Void
    private let deactivateRemote: @Sendable () -> Void
    private let now: () -> Date
    private var task: Task<Void, Never>?
    private var attempt = UUID()
    private let prepareForPairingChange: () async -> Void
    private let finishPairingChange: () async -> Void
    private let didStorePairing: () async -> Bool
    var onPairingCommitted: (() -> Void)?
    private var revoking = false
    private var revocationRequested = false
    var canRevokePending: () -> Bool = { true }

    init(vault: PairingVault, transport: PairingTransport = RelayClient.shared,
         remoteOrigin: @escaping @Sendable (String) async -> URL? = {
             await RemoteAccess.shared.origin(ticket: $0)
         },
         now: @escaping () -> Date = { Date() },
         prepareForPairingChange: @escaping () async -> Void = {},
         finishPairingChange: @escaping () async -> Void = {},
         didStorePairing: @escaping () async -> Bool = { true },
         activateRemote: @escaping @Sendable (String?) -> Void = {
             RemoteAccess.shared.activate(ticket: $0)
         },
         deactivateRemote: @escaping @Sendable () -> Void = {
             RemoteAccess.shared.deactivate()
         }) {
        self.vault = vault
        self.transport = transport
        self.remoteOrigin = remoteOrigin
        self.activateRemote = activateRemote
        self.deactivateRemote = deactivateRemote
        self.now = now
        self.prepareForPairingChange = prepareForPairingChange
        self.finishPairingChange = finishPairingChange
        self.didStorePairing = didStorePairing
        self.paired = vault.current()
        self.isRemote = vault.isRemote
        if !surfaceLoadFailure(), paired != nil { phase = .paired }
    }

    var isPaired: Bool { paired != nil }

    var previousDeviceRemovalMessage: String? {
        paired?.previousDeviceRemovalNeeded == true ? Copy.previousDeviceRemoval : nil
    }

    func retrySavedPairing() {
        guard task == nil else { return }
        paired = vault.retryLoading()
        isRemote = vault.isRemote
        recoveryMessage = nil
        if !surfaceLoadFailure() { phase = isPaired ? .paired : .idle }
    }

    @discardableResult
    private func surfaceLoadFailure() -> Bool {
        guard let error = vault.loadError else { return false }
        recoveryMessage = error.localizedDescription
        paired = nil
        phase = .failed(error.localizedDescription)
        return true
    }

    // Opens the entry form without removing the pairing used by the rest of the app.
    func prepareToPair() {
        cancel()
        if recoveryMessage == nil { phase = .idle }
    }

    // Starts a claim, including while already paired, without replacing the vault until approval.
    // Returns its task so callers (and tests) can await the outcome.
    @discardableResult
    func begin(invitation text: String) -> Task<Void, Never>? {
        // A link opened while claiming must not replace the key and bearer already sent to the Mac.
        guard task == nil else { return nil }
        guard !surfaceLoadFailure() else { return nil }
        prepareToPair()
        let invitation: PairingInvitation
        do {
            invitation = try PairingInvitation.parse(text, now: now())
        } catch PairingInvitation.ParseError.expired {
            attempt = UUID()
            phase = .expired
            return nil
        } catch let error as PairingInvitation.ParseError {
            attempt = UUID()
            phase = .failed(error.localizedDescription)
            return nil
        } catch {
            attempt = UUID()
            phase = .failed(Copy.unreadableInvitation)
            return nil
        }
        let current = UUID()
        attempt = current
        phase = .claiming
        let running = Task { await run(invitation, attempt: current) }
        task = running
        return running
    }

    func cancel() {
        // Keep the pending poll alive until the code ends, so a late approval can be revoked.
        // It no longer owns the visible phase or permission to write the vault.
        task = nil
        attempt = UUID()
        if let recoveryMessage {
            phase = .failed(recoveryMessage)
        } else {
            phase = isPaired ? .paired : .idle
        }
    }

    func acknowledgeOutcome() {
        guard recoveryMessage == nil else { return }
        switch phase {
        case .declined, .expired, .failed:
            phase = .idle
        default:
            break
        }
    }

    // Removes the pairing on the relay (which also tells the hub), then on this iPhone.
    // If the relay cannot be reached the caller decides whether to forget it locally.
    func unpair() async throws {
        if let error = vault.loadError { throw error }
        let credentials: RelayCredentials?
        do { credentials = try vault.session().credentials }
        catch RelayClientError.offline { throw RelayClientError.offline }
        catch { credentials = nil }
        if let credentials {
            do {
                try await transport.unpair(credentials)
            } catch RelayClientError.unauthorized {
                // Already revoked from the Mac. Nothing left to tell the relay.
            } catch RelayClientError.notFound {
                // Already gone from the relay.
            }
        }
        try forgetLocally()
    }

    func forgetLocally() throws {
        cancel()
        try vault.wipe()
        paired = nil
        isRemote = false
        recoveryMessage = nil
        phase = .idle
        deactivateRemote()
    }

    // MARK: Flow

    private struct ClaimPlaintext: Encodable {
        let deviceId: String
        let deviceName: String
        let devicePub: String
        let bearerHash: String
        let platform: String
        let appVersion: String
        let scopes: [String]
        let pushEnvironment: String
        let simulator: Bool?
    }

    private struct ApprovalPlaintext: Decodable {
        let deviceId: String?
        let hubName: String?
        let scopes: [String]?
        let expiresAt: String?
        let hubPub: String?
    }

    // Ends the flow with a phase. Thrown rather than assigned so every exit has one place to report.
    private struct Stop: Error {
        let phase: PairingPhase
    }

    private func run(_ invitation: PairingInvitation, attempt current: UUID) async {
        let deviceID = UUID().uuidString.lowercased()
        let keyPair = RelayCrypto.makeDeviceKeyPair()
        let devicePublic = RelayCrypto.base64URLEncode(keyPair.publicKey)
        // 32 random bytes as unpadded base64url: exactly 43 characters. The relay hashes this text.
        let bearer = RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32))
        let deviceName = Self.deviceName()
        #if targetEnvironment(simulator)
        let simulator: Bool? = true
        #else
        let simulator: Bool? = nil
        #endif
        var claimSent = false
        var saved = false
        defer {
            if current == attempt { task = nil }
        }
        do {
            try checkAttempt(current)
            let kPair = try RelayCrypto.pairingKey(devicePrivate: keyPair.privateKey, hubPublic: invitation.hubPublicKey,
                                                   secret: invitation.secret, hubID: invitation.hubID,
                                                   pairingID: invitation.pairingID)
            let claimToken = RelayCrypto.base64URLEncode(
                RelayCrypto.claimToken(secret: invitation.secret, pairingID: invitation.pairingID))
            let plaintext = ClaimPlaintext(deviceId: deviceID, deviceName: deviceName, devicePub: devicePublic,
                                           bearerHash: Self.bearerHash(bearer), platform: "ios",
                                           appVersion: Self.appVersion(), scopes: Self.requestedScopes,
                                           pushEnvironment: PushEnvironment.current, simulator: simulator)
            let ct = try RelayCrypto.sealClaim(try JSONEncoder().encode(plaintext), kPair: kPair,
                                               hubID: invitation.hubID, pairingID: invitation.pairingID)
            let body = PairingClaimBody(hub: invitation.hubID, claimToken: claimToken, deviceId: deviceID,
                                        devicePub: devicePublic, ct: RelayCrypto.base64URLEncode(ct))

            claimSent = true
            do {
                try await claim(body, invitation: invitation, attempt: current)
            } catch is CancellationError {
                // A claim already sent may have reached the Mac. Stop sending it again, but
                // keep collecting a possible approval so the abandoned device can be revoked.
                guard current != attempt else { throw CancellationError() }
            }
            show(.waitingApproval(code: RelayCrypto.confirmationCode(kPair: kPair)), attempt: current)
            saved = try await awaitApproval(invitation: invitation, kPair: kPair, claimToken: claimToken, deviceID: deviceID,
                                    deviceName: deviceName, keyPair: keyPair, bearer: bearer, attempt: current)
        } catch let stop as Stop {
            show(stop.phase, attempt: current)
        } catch is CancellationError {
            // A dismissed attempt cannot publish a pairing, even if its approval arrived.
        } catch {
            show(.failed(Copy.generic), attempt: current)
        }
        if claimSent && !saved {
            // Also handles an approval that arrived but could not be validated or committed.
            let credentials = RelayCredentials(origin: invitation.relayOrigin, bearer: bearer)
            try? vault.enqueueDeviceRevocation(deviceID: deviceID, credentials: credentials,
                                               irohTicket: invitation.irohTicket)
            // An unreadable record may already contain this candidate after an ambiguous commit.
            // Let recovery identify the current device before executing its queued revoke.
            if vault.loadError == nil {
                await revoke(PendingDeviceRevocation(deviceID: deviceID, origin: credentials.origin,
                                                     bearer: bearer, irohTicket: invitation.irohTicket))
            }
        }
    }

    // Sends the claim. Status codes are mapped here; the caller only sees a Stop with the phase to show.
    private func claim(_ body: PairingClaimBody, invitation: PairingInvitation, attempt current: UUID) async throws {
        var attempts = 0
        while true {
            try checkAttempt(current)
            attempts += 1
            do {
                let origin = try await relayOrigin(for: invitation)
                try await transport.claimPairing(origin: origin,
                                                 pairingID: invitation.pairingID, body: body)
                return
            } catch RelayClientError.forbidden, RelayClientError.notFound, RelayClientError.unauthorized {
                // 403 is a bad claim token, 404 an unknown pairing or hub mismatch.
                throw Stop(phase: .failed(Copy.codeNotValid))
            } catch RelayClientError.conflict {
                throw Stop(phase: .failed(Copy.codeAlreadyUsed))
            } catch RelayClientError.gone {
                throw Stop(phase: .expired)
            } catch RelayClientError.rateLimited(let retryAfter) {
                throw Stop(phase: .failed(Copy.rateLimited(retryAfter: retryAfter)))
            } catch RelayClientError.offline, RelayClientError.timedOut, RelayClientError.unavailable,
                    RelayClientError.server, RelayClientError.invalidResponse {
                // A 502 or 504 from a proxy or tunnel in front of the relay, or a page that is not the relay's
                // answer, is a failure on the way like a timeout: the identical claim is safe to send again
                // (PROTOCOL.md section 5), where giving up revoked the new device and asked for a new code.
                try checkAttempt(current)
                guard attempts < Self.claimAttempts else { throw Stop(phase: .failed(Copy.offline)) }
                try await Task.sleep(nanoseconds: Self.retryDelayNanoseconds)
            }
        }
    }

    // Long-polls until the hub answers. The approval arrives exactly once, so it is written before anything else.
    private func awaitApproval(invitation: PairingInvitation, kPair: Data, claimToken: String, deviceID: String,
                               deviceName: String, keyPair: (privateKey: Data, publicKey: Data),
                               bearer: String, attempt current: UUID) async throws -> Bool {
        var conflicts = 0
        var transientFailures = 0
        while true {
            try Task.checkCancellation()
            guard now() < invitation.expiresAt else { throw Stop(phase: .expired) }
            do {
                let origin = try await relayOrigin(for: invitation)
                let poll = try await transport.pollPairing(origin: origin,
                                                           pairingID: invitation.pairingID, claimToken: claimToken)
                transientFailures = 0
                switch poll.state {
                case "claimed":
                    conflicts = 0
                    continue
                case "approved":
                    guard current == attempt else { return false }
                    guard let approval = poll.approval else {
                        throw Stop(phase: .failed(Copy.approvalLost))
                    }
                    try await store(approval.ct, kPair: kPair, invitation: invitation, deviceID: deviceID,
                              deviceName: deviceName, keyPair: keyPair, bearer: bearer, attempt: current)
                    return true
                case "declined":
                    throw Stop(phase: .declined)
                case "expired":
                    throw Stop(phase: .expired)
                default:
                    throw Stop(phase: .failed(Copy.unknownStatus))
                }
            } catch RelayClientError.conflict {
                // 409: still open and not claimed. This phone just claimed it, so wait briefly, then treat it as a race.
                conflicts += 1
                guard conflicts <= Self.maxPollConflicts else { throw Stop(phase: .failed(Copy.noLongerWaiting)) }
                try await Task.sleep(nanoseconds: Self.retryDelayNanoseconds)
            } catch RelayClientError.notFound {
                // The janitor removed an expired row, or the approval was already delivered and deleted.
                if now() >= invitation.expiresAt { throw Stop(phase: .expired) }
                throw Stop(phase: .failed(Copy.approvalLost))
            } catch RelayClientError.gone {
                throw Stop(phase: .expired)
            } catch RelayClientError.forbidden {
                throw Stop(phase: .failed(Copy.codeNotValid))
            } catch RelayClientError.unauthorized {
                throw Stop(phase: .failed(Copy.pollRejected))
            } catch RelayClientError.offline, RelayClientError.timedOut, RelayClientError.unavailable,
                    RelayClientError.server, RelayClientError.invalidResponse {
                // A lost response may mean the one-time approval was delivered. Retry briefly, then say so.
                transientFailures += 1
                guard current != attempt || transientFailures < Self.maxTransientPollFailures else {
                    throw Stop(phase: .failed(Copy.approvalLost))
                }
                // An abandoned claim may still be approved. Keep checking until its code expires.
                try await Task.sleep(nanoseconds: Self.retryDelayNanoseconds * UInt64(min(transientFailures, 3)))
            }
        }
    }

    // Decrypts, checks and writes the approval. The vault write comes before any other use of the approval.
    private func store(_ approvalCT: String, kPair: Data, invitation: PairingInvitation, deviceID: String,
                       deviceName: String, keyPair: (privateKey: Data, publicKey: Data), bearer: String,
                       attempt current: UUID) async throws {
        try checkAttempt(current)
        let plaintext: Data
        do {
            plaintext = try RelayCrypto.openApproval(try RelayCrypto.base64URLDecode(approvalCT), kPair: kPair,
                                                     hubID: invitation.hubID, pairingID: invitation.pairingID)
        } catch {
            throw Stop(phase: .failed(Copy.approvalRejected))
        }
        guard let approval = try? JSONDecoder().decode(ApprovalPlaintext.self, from: plaintext),
              approval.deviceId == deviceID,
              let hubPub = approval.hubPub,
              (try? RelayCrypto.decodeKey(hubPub)) == invitation.hubPublicKey else {
            throw Stop(phase: .failed(Copy.approvalRejected))
        }
        let record = PairedDevice(
            hubID: invitation.hubID,
            hubName: approval.hubName ?? "Your computer",
            hubPublicKey: RelayCrypto.base64URLEncode(invitation.hubPublicKey),
            relayOrigin: invitation.relayOrigin.absoluteString,
            deviceID: deviceID,
            deviceName: deviceName,
            scopes: approval.scopes ?? Self.requestedScopes,
            pairedAt: now(),
            expiresAt: approval.expiresAt.flatMap(RelayTime.parse),
            counters: SequenceCounters())
        await prepareForPairingChange()
        do {
            try checkAttempt(current)
        } catch {
            await finishPairingChange()
            throw error
        }
        do {
            try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(keyPair.privateKey),
                            bearer: bearer, irohTicket: invitation.irohTicket)
        } catch {
            await finishPairingChange()
            if surfaceLoadFailure(), let recoveryMessage { throw Stop(phase: .failed(recoveryMessage)) }
            throw Stop(phase: .failed(Copy.approvalNotSaved))
        }
        onPairingCommitted?()
        paired = vault.current()
        isRemote = vault.isRemote
        activateRemote(invitation.irohTicket)
        show(.paired, attempt: current)
        await finishPairingChange()
        if await didStorePairing() {
            await retryPendingRevocations()
        }
    }

    // Called on launch and after push registration settles. Credentials are captured durably by
    // the vault before replacement, so cleanup does not depend on reopening the old session.
    func retryPendingRevocations() async {
        revocationRequested = true
        guard !revoking else { return }
        revoking = true
        var restoresRemote = false
        defer {
            if restoresRemote {
                activateRemote(try? vault.irohTicket())
            }
        }
        defer { revoking = false }
        while revocationRequested && canRevokePending() {
            revocationRequested = false
            guard let pending = try? vault.pendingDeviceRevocations() else { return }
            for item in pending {
                guard canRevokePending() else { return }
                if item.irohTicket != nil { restoresRemote = true }
                await revoke(item)
            }
        }
    }

    private func revoke(_ item: PendingDeviceRevocation) async {
        let origin: URL
        if let ticket = item.irohTicket {
            guard let mappedOrigin = await remoteOrigin(ticket) else { return }
            origin = mappedOrigin
        } else {
            origin = item.origin
        }
        do {
            try await transport.unpair(RelayCredentials(origin: origin, bearer: item.bearer))
        } catch RelayClientError.unauthorized, RelayClientError.notFound {
            // A previous attempt or the Mac already removed it.
        } catch {
            return
        }
        try? vault.completeDeviceRevocation(deviceID: item.deviceID)
    }

    private func relayOrigin(for invitation: PairingInvitation) async throws -> URL {
        guard let ticket = invitation.irohTicket else { return invitation.relayOrigin }
        guard let origin = await remoteOrigin(ticket) else { throw RelayClientError.offline }
        return origin
    }

    // A cancelled or superseded request may still return an approval from the transport.
    private func checkAttempt(_ current: UUID) throws {
        try Task.checkCancellation()
        guard current == attempt else { throw CancellationError() }
    }

    // Only the attempt that is still current may change the visible phase.
    private func show(_ newPhase: PairingPhase, attempt current: UUID) {
        guard current == attempt else { return }
        phase = newPhase
    }

    // MARK: Device facts

    static func bearerHash(_ bearer: String) -> String {
        RelayCrypto.sha256Hex(Data(bearer.utf8))
    }

    static func deviceName() -> String {
        var name = UIDevice.current.name
        while name.utf8.count > 120 { name.removeLast() }
        return name.isEmpty ? "Phone" : name
    }

    static func appVersion() -> String {
        let info = Bundle.main.infoDictionary
        let short = info?["CFBundleShortVersionString"] as? String ?? "0"
        let build = info?["CFBundleVersion"] as? String ?? "0"
        return "\(short) (\(build))"
    }
}

// Copy for pairing outcomes. Sentence case, and each message says what the user can do next.
private enum Copy {
    static let previousDeviceRemoval = "The old phone may still be listed on your computer. You can remove it in Isoform under Settings > Phone."
    static let unreadableInvitation = "That pairing code could not be read. Copy it again from your computer."
    static let codeNotValid = "This pairing code is not valid. Create a new one on your computer."
    static let codeAlreadyUsed = "This pairing code was already used or closed on your computer. Create a new one on your computer."
    static let noLongerWaiting = "Your computer is no longer waiting for this pairing code. Create a new one on your computer."
    static var offline: String { String(localized: "Your phone could not reach your computer. Make sure Isoform is open and remote access is on, check your internet connection, then try again.") }
    static let approvalLost = "Your computer's answer did not reach this phone. If your computer approved it, create a new pairing code on your computer and pair again."
    static let approvalRejected = "Your computer's answer does not match this pairing code. Create a new pairing code on your computer and start again."
    static let approvalNotSaved = "Your computer approved this phone, but Isoform could not save the approval. Create a new pairing code on your computer and pair again."
    static let pollRejected = "Isoform could not check this pairing. Create a new pairing code on your computer and try again."
    static let unknownStatus = "The relay sent a status Isoform does not understand. Try again."
    static let generic = "Isoform could not finish pairing. Try again."

    static func rateLimited(retryAfter: TimeInterval?) -> String {
        guard let retryAfter else { return "Too many attempts. Wait a few minutes, then try again." }
        let minutes = max(1, Int((retryAfter / 60).rounded(.up)))
        return "Too many attempts. Wait \(minutes) minute\(minutes == 1 ? "" : "s"), then try again."
    }
}

enum RelayTime {
    private static let fractional: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()

    private static let whole: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter
    }()

    static func parse(_ text: String) -> Date? {
        whole.date(from: text) ?? fractional.date(from: text)
    }

    static func format(_ date: Date) -> String {
        whole.string(from: date)
    }
}

enum PushEnvironment {
    // Build setting YANSU_APNS_ENVIRONMENT reaches the app through Info.plist and the entitlement.
    static var current: String {
        let value = Bundle.main.object(forInfoDictionaryKey: "YansuAPNSEnvironment") as? String
        return value == "development" ? "development" : "production"
    }
}
