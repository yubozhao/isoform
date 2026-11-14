import Foundation

/// Gets the computer's direct path for a relay pairing (PROTOCOL.md section 21.4). A computer pinned to a relay
/// also serves its own relay over iroh; the phone asks for that path over the relay once, keeps it in the Keychain,
/// and falls back to it whenever the relay cannot reach the computer. A computer without a direct path answers 503,
/// so the request is repeated at most every 15 minutes.
@MainActor
final class DirectRouteKeeper {
    static let retryInterval: TimeInterval = 15 * 60

    private let vault: PairingVault
    private let fetch: @Sendable () async throws -> RemoteAccessGrant
    private var lastAttempt: (pairing: String, at: Date)?
    private var running = false

    init(vault: PairingVault, fetch: @escaping @Sendable () async throws -> RemoteAccessGrant) {
        self.vault = vault
        self.fetch = fetch
    }

    /// Called whenever the phone hears from the computer. Cheap when the route is already saved.
    func ensure(now: Date = Date()) async {
        guard !running, let paired = vault.current(), !vault.isRemote, !vault.hasDirectRoute else { return }
        let pairing = "\(paired.hubID)/\(paired.deviceID)"
        if let lastAttempt, lastAttempt.pairing == pairing, now.timeIntervalSince(lastAttempt.at) < Self.retryInterval { return }
        running = true
        lastAttempt = (pairing, now)
        defer { running = false }
        guard let grant = try? await fetch(), PairingInvitation.isValidIrohTicket(grant.ticket),
              grant.bearer.utf8.count == 43 else { return }
        try? vault.storeDirectRoute(DirectRoute(ticket: grant.ticket, bearer: grant.bearer), deviceID: paired.deviceID)
    }
}
