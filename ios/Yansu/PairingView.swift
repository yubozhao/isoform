import SwiftUI

// Pair with your computer. Scan the code on a device, or paste the pairing link anywhere.
// The Simulator has no camera, so the paste field is its only entry point.
struct PairingView: View {
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var brief: BriefStore
    @EnvironmentObject private var router: NotificationRouter
    @Environment(\.dismiss) private var dismiss
    @State private var pasted = PairingView.prefilledInvitation
    @State private var scanning = false
    @State private var scannerMessage: String?
    @FocusState private var pasteFocused: Bool

    init(startScanning: Bool = false) {
        #if targetEnvironment(simulator)
        _scanning = State(initialValue: false)
        #else
        _scanning = State(initialValue: startScanning)
        #endif
    }

    // UI tests prefill the paste field through the launch environment; release
    // builds ignore the variable.
    private static var prefilledInvitation: String {
        #if DEBUG
        return ProcessInfo.processInfo.environment["YANSU_UITEST_INVITATION"] ?? ""
        #else
        return ""
        #endif
    }

    private var canScan: Bool {
        #if targetEnvironment(simulator)
        return false
        #else
        return true
        #endif
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 24) {
                    switch pairing.phase {
                    case .idle, .declined, .expired, .failed:
                        entryContent
                    case .claiming:
                        claimingContent
                    case .waitingApproval(let code):
                        waitingContent(code: code)
                    case .paired:
                        pairedContent
                    }
                }
                .padding(.horizontal, YansuTheme.gutter)
                .padding(.top, 8)
                .padding(.bottom, 32)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .yansuScreen()
            .navigationTitle("Pair with your computer")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(isFinished ? "Done" : "Cancel") {
                        if isFinished {
                            finish()
                        } else {
                            pairing.cancel()
                            dismiss()
                        }
                    }
                    .accessibilityIdentifier("pairing.close")
                }
            }
        }
        .onChange(of: pairing.phase) { _, phase in
            guard phase == .paired else { return }
            scanning = false
            // Paired in one scan: Today opens on the latest brief at once, with "Paired with {computer}" (rules 6, 7).
            finish()
        }
    }

    private var isFinished: Bool {
        if case .paired = pairing.phase { return true }
        return false
    }

    // MARK: Entry

    @ViewBuilder
    private var entryContent: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(canScan ? "setup.point" : "setup.pairBody")
                .font(.subheadline)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if pairing.isPaired {
                Text("To replace an existing pairing, choose Pair again in this phone's Settings. Your current pairing keeps working until the new pairing is saved.")
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        outcomeNotice
        if canScan {
            scanSection
        }
        pasteSection
    }

    @ViewBuilder
    private var outcomeNotice: some View {
        switch pairing.phase {
        case .declined:
            NoticeBanner(message: "Your computer declined this pairing.", systemImage: "xmark.circle") {
                pairing.acknowledgeOutcome()
            }
        case .expired:
            NoticeBanner(message: String(localized: "setup.expired"), systemImage: "clock") {
                pairing.acknowledgeOutcome()
            }
        case .failed(let message):
            NoticeBanner(message: message, systemImage: "exclamationmark.circle") {
                pairing.acknowledgeOutcome()
            }
        default:
            EmptyView()
        }
    }

    private var scanSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            SectionLabel(title: "Scan the code")
            if scanning {
                QRScannerView(onCode: { code in
                    scanning = false
                    pairing.begin(invitation: code)
                }, onUnavailable: { message in
                    scanning = false
                    scannerMessage = message
                })
                .frame(height: 280)
                .clipShape(RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                        .strokeBorder(YansuTheme.border, lineWidth: 1)
                }
                Button("Stop scanning") {
                    scanning = false
                }
                .buttonStyle(YansuSecondaryButtonStyle())
            } else {
                Button("Scan the code from your computer") {
                    scannerMessage = nil
                    scanning = true
                }
                .buttonStyle(YansuPrimaryButtonStyle())
            }
            if let scannerMessage {
                NoticeBanner(message: scannerMessage, systemImage: "camera") {
                    self.scannerMessage = nil
                }
            }
        }
    }

    private var pasteSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            SectionLabel(title: canScan ? "Or paste the link" : "Paste the link",
                         detail: canScan ? nil : "The Simulator has no camera. Copy the isoform://pair link from your computer.")
            TextField("isoform://pair#", text: $pasted, axis: .vertical)
                .font(.system(.footnote, design: .monospaced))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .lineLimit(3...6)
                .focused($pasteFocused)
                .padding(12)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                        .strokeBorder(YansuTheme.border, lineWidth: 1)
                }
                .accessibilityIdentifier("pairing.pasteField")
            if canScan {
                Button("Pair with this link") {
                    pasteFocused = false
                    pairing.begin(invitation: pasted)
                }
                .buttonStyle(YansuSecondaryButtonStyle())
                .disabled(pasted.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                .accessibilityIdentifier("pairing.pasteButton")
            } else {
                Button("Pair with this link") {
                    pasteFocused = false
                    pairing.begin(invitation: pasted)
                }
                .buttonStyle(YansuPrimaryButtonStyle())
                .disabled(pasted.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                .accessibilityIdentifier("pairing.pasteButton")
            }
        }
    }

    // MARK: Claiming and waiting

    private var claimingContent: some View {
        VStack(alignment: .leading, spacing: 16) {
            ProgressView("Sending the request through your relay.")
            Text("Keep this screen open until your computer shows the code.")
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
        }
    }

    private func waitingContent(code: String) -> some View {
        VStack(alignment: .leading, spacing: 20) {
            YansuCard {
                VStack(alignment: .leading, spacing: 14) {
                    Text("Compare this code with your computer, then approve there.")
                        .font(.headline)
                        .foregroundStyle(YansuTheme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                    Text(Self.spaced(code))
                        .font(.system(size: 40, weight: .semibold, design: .rounded))
                        .monospacedDigit()
                        .foregroundStyle(YansuTheme.ink)
                        .frame(maxWidth: .infinity, alignment: .center)
                        .accessibilityLabel("Confirmation code \(code.map { String($0) }.joined(separator: " "))")
                }
            }
            Text("Pairing ends when your computer approves, declines, or the code expires.")
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
            Button("Cancel pairing") {
                pairing.cancel()
                dismiss()
            }
            .buttonStyle(YansuSecondaryButtonStyle())
        }
    }

    private var pairedContent: some View {
        VStack(alignment: .leading, spacing: 16) {
            YansuCard {
                VStack(alignment: .leading, spacing: 10) {
                    Label("This phone is paired with \(pairing.paired?.hubName ?? "your computer").", systemImage: "checkmark")
                        .font(.headline)
                        .foregroundStyle(YansuTheme.ink)
                        .labelStyle(.titleAndIcon)
                    Text(Self.pairedDetail)
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.secondary)
                    if let message = pairing.previousDeviceRemovalMessage {
                        Text(message)
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            Button("Done", action: finish)
            .buttonStyle(YansuPrimaryButtonStyle())
            .accessibilityIdentifier("pairing.done")
        }
    }

    /// Every Done after a successful pairing finishes the same way: fetch the first brief, close the sheet and
    /// show the Feed. Apart from the view so a test can check all three steps.
    static func finish(refreshBrief: () -> Void, dismiss: () -> Void, router: NotificationRouter) {
        refreshBrief()
        dismiss()
        router.pairingFinished()
    }

    /// The Mac sends the newest brief right after approval, so fetch it now instead of waiting for the next
    /// foreground refresh.
    private func finish() {
        router.pairedToast = pairing.paired?.hubName ?? String(localized: "your computer")
        Self.finish(refreshBrief: { Task { await brief.refresh() } }, dismiss: { dismiss() }, router: router)
    }

    /// Shown once the Mac approved. The first brief arrives on Today.
    static let pairedDetail = "Your brief will appear on Today in a moment."

    static func spaced(_ code: String) -> String {
        guard code.count == 6 else { return code }
        let mid = code.index(code.startIndex, offsetBy: 3)
        return "\(code[..<mid]) \(code[mid...])"
    }
}
