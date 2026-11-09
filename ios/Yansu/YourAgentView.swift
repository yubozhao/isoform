import MessageUI
import SwiftUI

struct YourAgentView: View {
    @ObservedObject var store: YourAgentStore
    @EnvironmentObject private var router: NotificationRouter
    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.locale) private var locale
    @ScaledMetric(relativeTo: .body) private var rowSize = 16
    @ScaledMetric(relativeTo: .subheadline) private var howSize = 14
    @FocusState private var focus: Field?
    @State private var showsMail = false
    @State private var showsShare = false
    @State private var copied = false
    private enum Field { case name, email, code }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    header
                    // Gray bars and headings show only while the copy is being read. A copy that cannot be read leaves both
                    // sections out, and they appear when it arrives (P146). A section with nothing to list is left out too.
                    if store.copy == nil ? store.reading : (store.isStillLearning || !(store.copy?.specialties ?? []).isEmpty) {
                        specialties
                    }
                    if store.copy == nil {
                        if store.reading {
                            VStack(alignment: .leading, spacing: 7) {
                                heading("A taste of what it knows")
                                placeholders(taste: true)
                            }
                        }
                    } else if store.copy?.stillLearning == false, let taste = store.copy?.taste, !taste.isEmpty {
                        tasteSection(taste)
                    }
                    reservationSection
                    if store.status != .failed { writeBo }
                    howItWorks
                }
                .padding(.horizontal, YansuTheme.gutter).padding(.top, 10).padding(.bottom, 40)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            // P164: the scroll area ends above the bottom gesture zone, and above the keyboard while it is up.
            .endsAboveGestureZone()
            .scrollDismissesKeyboard(.interactively)
            .onChange(of: focus) { old, new in
                if old == .name { store.nameDidLoseFocus() }
                if old == .email { store.emailDidLoseFocus() }
                // Once the keyboard is up, scroll so the fields and Send request sit above it (P164).
                if new == .name || new == .email {
                    Task {
                        try? await Task.sleep(for: .milliseconds(350))
                        withAnimation { proxy.scrollTo(Self.sendAnchor, anchor: .bottom) }
                    }
                }
            }
        }
        .yansuScreen().foregroundStyle(YansuTheme.ink).tint(YansuTheme.accent)
        .hardTitleEdge()
        .navigationTitle("").navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done") { router.sheet = nil }.accessibilityIdentifier("settings.done")
            }
        }
        .accessibilityIdentifier("yourAgent.page")
        .task { await store.open() }
        .onDisappear { store.close() }
        .onChange(of: store.step) { _, step in
            if step == .code { focus = .code }
        }
        .onChange(of: store.code) { _, value in
            // A wrong code clears the boxes and puts the cursor back in the first one.
            if value.isEmpty, store.step == .code { focus = .code }
        }
        .sheet(isPresented: $showsShare) {
            if let reservation = store.reservation, let url = URL(string: "https://isoform.ai/agents/\(reservation.name)") {
                AgentShareSheet(items: [shareText, url])
            }
        }
        .sheet(isPresented: $showsMail) {
            AgentMailComposer(subject: store.reservation.map {
                String(format: String(localized: "My agent: isoform.ai/agents/%@"), $0.name)
            } ?? String(localized: "My agent"))
        }
    }

    /// The scroll target for Send request, so the form scrolls above the keyboard while it is typing.
    private static let sendAnchor = "yourAgent.sendRequest"

    // One name, Agent of You (P146); the product name stays in English in every language.
    private var title: some View { Text("Agent of You").font(.title.bold()).fixedSize(horizontal: false, vertical: true) }
    private var badge: some View {
        Text("Coming soon").font(.caption2.weight(.semibold)).foregroundStyle(YansuTheme.warning)
            .padding(.horizontal, 7).padding(.vertical, 3)
            .background(YansuTheme.warningWash, in: Capsule())
    }
    private var header: some View {
        VStack(alignment: .leading, spacing: 10) {
            title
            badge
            Text("Soon, people can hire your Agent of You.")
                .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Text("It works the way you do, makes things for them in minutes, and you get paid for every minute it works.")
                .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }.padding(.horizontal, 4)
    }
    private func heading(_ text: LocalizedStringKey) -> some View {
        // Sentence case: labels are headings for a group, not capitals (P146).
        Text(text).font(.footnote).tracking(0.3)
            .foregroundStyle(YansuTheme.tertiary).padding(.horizontal, 16)
            .accessibilityAddTraits(.isHeader).fixedSize(horizontal: false, vertical: true)
    }
    private func group<Content: View>(@ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 0, content: content)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(colorScheme == .dark ? YansuTheme.surfaceWarm : YansuTheme.surface,
                        in: RoundedRectangle(cornerRadius: 12))
    }
    private func footnote(_ text: LocalizedStringKey) -> some View {
        Text(text).font(.footnote).foregroundStyle(YansuTheme.tertiary)
            .padding(.horizontal, 16).fixedSize(horizontal: false, vertical: true)
    }
    private var specialties: some View {
        VStack(alignment: .leading, spacing: 7) {
            heading("What it would specialize in")
            if store.copy == nil {
                placeholders(taste: false)
            } else if store.isStillLearning {
                group {
                    Text("Isoform is still learning how you work. Your specialties show here once it has seen you do something a few times.")
                        .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true).padding(16)
                }.accessibilityIdentifier("yourAgent.stillLearning")
            } else {
                group {
                    let items = Array((store.copy?.specialties ?? []).prefix(3))
                    ForEach(items.indices, id: \.self) { index in
                        if index > 0 { Divider().overlay(YansuTheme.borderSubtle) }
                        HStack(alignment: .firstTextBaseline, spacing: 12) {
                            Text(items[index].name).font(.system(size: rowSize))
                        }.fixedSize(horizontal: false, vertical: true)
                            .padding(.horizontal, 16).padding(.vertical, 12)
                    }
                }.accessibilityIdentifier("yourAgent.specialties")
                footnote("From what Isoform learned on your computer.")
            }
        }
    }
    private func placeholders(taste: Bool) -> some View {
        group {
            ForEach(0..<3) { index in
                if index > 0 { Divider().overlay(YansuTheme.borderSubtle) }
                RoundedRectangle(cornerRadius: 3).fill(YansuTheme.borderSubtle)
                    .frame(width: taste ? 220 : 140, height: 8)
                    .frame(maxWidth: .infinity, minHeight: taste
                        ? UIFont.preferredFont(forTextStyle: .subheadline).lineHeight * (index == 2 ? 1 : 2)
                        : rowSize * 1.25, alignment: .leading)
                    .padding(.horizontal, 16).padding(.vertical, 12)
            }
        }.accessibilityHidden(true)
    }
    private func tasteSection(_ items: [String]) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            heading("A taste of what it knows")
            group {
                ForEach(items.indices, id: \.self) { index in
                    if index > 0 { Divider().overlay(YansuTheme.borderSubtle) }
                    HStack(alignment: .top, spacing: 8) {
                        Circle().fill(YansuTheme.accent).frame(width: 5, height: 5).padding(.top, 7)
                        Text(items[index]).font(.subheadline).fixedSize(horizontal: false, vertical: true)
                    }.padding(.horizontal, 16).padding(.vertical, 12)
                }
            }.accessibilityIdentifier("yourAgent.taste")
        }
    }
    private var reservationSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            heading(store.step == .yours || store.step == .confirm ? "Your page" : "Request your page")
            switch store.step {
            case .form:
                form
                if store.status == .failed {
                    group {
                        statusLine.padding(.top, 16)
                        writeBo.padding(.vertical, 12)
                    }
                } else { statusLine }
                Button("Send request") { focus = nil; Task { await store.reserve() } }
                    .buttonStyle(YansuPrimaryButtonStyle()).disabled(!store.canReserve)
                    .accessibilityIdentifier("yourAgent.reserve")
                    .id(Self.sendAnchor)
                footnote("Only the name and this email are sent. We'll email a code to confirm it's you, and write once more when it opens.")
            case .code:
                if let reservation = store.reservation { codeCard(reservation) }
            case .choice:
                if let reservation = store.reservation { choiceCard(reservation) }
            case .yours:
                if let reservation = store.reservation { reservedCard(reservation) }
            case .confirm:
                if let reservation = store.reservation { confirmCard(reservation) }
            }
        }
    }
    private var form: some View {
        group {
            HStack(alignment: .firstTextBaseline, spacing: 0) {
                Text(verbatim: "isoform.ai/agents/").foregroundStyle(YansuTheme.tertiary)
                TextField("yourname", text: $store.name)
                    .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                    .submitLabel(.next).focused($focus, equals: .name).onSubmit { focus = .email }
                    .accessibilityLabel(Text("Page name, after isoform.ai/agents/"))
                    .accessibilityIdentifier("yourAgent.name")
            }.padding(16)
            Divider().overlay(YansuTheme.borderSubtle)
            TextField("Your email", text: $store.email)
                .keyboardType(.emailAddress).textInputAutocapitalization(.never).autocorrectionDisabled()
                .textContentType(.emailAddress).submitLabel(.send).focused($focus, equals: .email)
                .onSubmit { if store.canReserve { focus = nil; Task { await store.reserve() } } }
                .accessibilityLabel(Text("Your email"))
                .accessibilityIdentifier("yourAgent.email").padding(16)
        }.font(.system(size: rowSize))
    }
    private var statusLine: some View {
        VStack(alignment: .leading, spacing: 6) {
            if !store.reachable {
                Text("Connect to the internet to send the request.").foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("yourAgent.offline")
            } else {
                if store.ranOut {
                    Text("The 10 minutes ran out, so the name was let go. Send the request again.")
                        .foregroundStyle(YansuTheme.secondary).accessibilityIdentifier("yourAgent.ranOut")
                }
                Group {
                    switch store.status {
                    case .clear: EmptyView()
                    case .taken(let suggestion):
                        if let suggestion {
                            Text(takenText(suggestion))
                                .environment(\.openURL, OpenURLAction { _ in store.useSuggestion(); return .handled })
                                .accessibilityIdentifier("yourAgent.suggestion")
                        } else { Text("That name isn't available.") }
                    case .notAllowed: Text("That name isn't available.")
                    case .rules: Text("Use 3 to 30 letters, numbers or dots, not starting or ending with a dot.")
                    case .failed: Text("Couldn't send the request. Try again.")
                    }
                }.foregroundStyle(YansuTheme.error)
                if let retryAt = store.retryAt {
                    Text(String(format: String(localized: "Too many tries. Try again at %@."), shortTime(retryAt)))
                        .foregroundStyle(YansuTheme.error).accessibilityIdentifier("yourAgent.rateLimited")
                }
            }
            if store.emailInvalid { Text("Check the email address.").foregroundStyle(YansuTheme.error) }
        }.font(.footnote).padding(.horizontal, 16).fixedSize(horizontal: false, vertical: true)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("yourAgent.status")
    }
    private func shortTime(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.locale = .autoupdatingCurrent
        formatter.dateStyle = .none
        formatter.timeStyle = .short
        return formatter.string(from: date)
    }
    private func takenText(_ suggestion: String) -> AttributedString {
        var result = AttributedString(String(format: String(localized: "Taken. Try %@."), suggestion))
        if let range = result.range(of: suggestion) {
            result[range].link = URL(string: "isoform-agent://suggestion")
            result[range].foregroundColor = YansuTheme.accent
        }
        return result
    }

    // MARK: - Cards for each request state

    /// Check your email: six boxes for the code, then the lines under them.
    private func codeCard(_ reservation: AgentReservation) -> some View {
        group {
            VStack(alignment: .leading, spacing: 6) {
                Text("Check your email").font(.body.weight(.semibold))
                Text(String(format: String(localized: "We sent a 6-digit code to %@. Enter it within 10 minutes to keep isoform.ai/agents/%@."),
                            reservation.email, reservation.name))
                    .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                codeBoxes.padding(.top, 8).padding(.bottom, 4)
                cardStatus
                Text(resendLine).font(.subheadline).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .environment(\.openURL, OpenURLAction { url in
                        if url.host == "change" { Task { await store.cancelRequest() } }
                        else if url.host == "resend" { Task { await store.sendCode() } }
                        return .handled
                    })
                    .accessibilityIdentifier("yourAgent.resend")
            }
            .padding(.horizontal, 16).padding(.vertical, 14)
        }.accessibilityElement(children: .contain).accessibilityIdentifier("yourAgent.code")
    }
    private var codeBoxes: some View {
        HStack(spacing: 8) {
            ForEach(0..<3, id: \.self) { digitBox($0) }
            Color.clear.frame(width: 6, height: 1)
            ForEach(3..<6, id: \.self) { digitBox($0) }
        }
        .frame(maxWidth: 330, alignment: .leading)
        .opacity(store.verifying ? 0.45 : 1)
        .overlay {
            // One hidden field takes the digits, paste and the one-time code from Mail.
            TextField("", text: Binding(get: { store.code }, set: { value in
                if !store.verifying && !store.inFlight { store.code = value }
            }))
                .keyboardType(.numberPad).textContentType(.oneTimeCode)
                .focused($focus, equals: .code)
                .foregroundStyle(.clear).tint(.clear)
                .frame(maxWidth: .infinity, minHeight: 56, maxHeight: 56)
                .opacity(0.02)
                .accessibilityLabel(Text("6-digit code"))
                .accessibilityIdentifier("yourAgent.codeField")
        }
    }
    private func digitBox(_ index: Int) -> some View {
        let digits = Array(store.code)
        let current = focus == .code && index == digits.count && index < 6
        return Text(index < digits.count ? String(digits[index]) : "")
            .font(.system(size: 24, weight: .semibold))
            .frame(maxWidth: 46).frame(height: 56)
            .background(YansuTheme.surfaceWarm, in: RoundedRectangle(cornerRadius: 11))
            .overlay(RoundedRectangle(cornerRadius: 11)
                .strokeBorder(current ? YansuTheme.accent : YansuTheme.border, lineWidth: 1.5))
            .background(RoundedRectangle(cornerRadius: 14)
                .fill(current ? YansuTheme.accentWash : Color.clear).padding(-3))
            .accessibilityHidden(true)
    }
    private var resendLine: AttributedString {
        var result = AttributedString(String(localized: "No code?") + (locale.identifier.hasPrefix("zh") ? "" : " "))
        var send = AttributedString(store.sent ? String(localized: "Sent") : String(localized: "Send it again"))
        send.link = URL(string: "isoform-agent://resend")
        send.foregroundColor = YansuTheme.accent
        var change = AttributedString(String(localized: "Change the name or email"))
        change.link = URL(string: "isoform-agent://change")
        change.foregroundColor = YansuTheme.accent
        result += send
        result += AttributedString(" · ")
        result += change
        return result
    }
    /// Lines under a card's controls: the code's message, a failed send, a rate limit, or offline.
    private var cardStatus: some View {
        VStack(alignment: .leading, spacing: 6) {
            if !store.reachable {
                Text("Connect to the internet to send the request.").foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("yourAgent.offline")
            } else {
                Group {
                    switch store.codeStatus {
                    case .wrong?: Text("That code didn't match. Try again.")
                    case .expired?: Text("That code has expired. Send it again.")
                    case nil: EmptyView()
                    }
                    if store.status == .failed { Text("Couldn't send the request. Try again.") }
                }.foregroundStyle(YansuTheme.error)
                if let retryAt = store.retryAt {
                    Text(String(format: String(localized: "Too many tries. Try again at %@."), shortTime(retryAt)))
                        .foregroundStyle(YansuTheme.error).accessibilityIdentifier("yourAgent.rateLimited")
                }
            }
        }.font(.subheadline)
    }

    /// Already has a page: keep the old one, or switch to the new name.
    private func choiceCard(_ reservation: AgentReservation) -> some View {
        let old = reservation.oldName ?? ""
        return group {
            VStack(alignment: .leading, spacing: 6) {
                Text(String(format: String(localized: "%@ already has isoform.ai/agents/%@."), reservation.email, old))
                    .font(.body.weight(.semibold)).fixedSize(horizontal: false, vertical: true)
                Text(String(format: String(localized: "One page per email. Keep it, or switch to %@? Switching lets %@ go."),
                            reservation.name, old))
                    .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                VStack(spacing: 8) {
                    Button(String(format: String(localized: "Keep %@"), old)) { Task { await store.choose(keep: true) } }
                        .buttonStyle(YansuSecondaryButtonStyle()).disabled(store.inFlight)
                        .accessibilityIdentifier("yourAgent.keep")
                    Button(String(format: String(localized: "Switch to %@"), reservation.name)) { Task { await store.choose(keep: false) } }
                        .buttonStyle(YansuPrimaryButtonStyle()).disabled(store.inFlight)
                        .accessibilityIdentifier("yourAgent.switch")
                }.padding(.top, 6)
                cardStatus
            }
            .padding(.horizontal, 16).padding(.vertical, 14)
        }.accessibilityElement(children: .contain).accessibilityIdentifier("yourAgent.choice")
    }

    /// Verified: the page is yours, with the place in line and Move up the line.
    private func reservedCard(_ reservation: AgentReservation) -> some View {
        group {
            VStack(alignment: .leading, spacing: 6) {
                Text(String(format: String(localized: "isoform.ai/agents/%@ is yours."), reservation.name))
                    .font(.body.weight(.semibold)).fixedSize(horizontal: false, vertical: true)
                Text(String(format: String(localized: "We'll email %@ when it opens."), reservation.email))
                    .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Button("Change") { store.change() }.foregroundStyle(YansuTheme.accent)
                    .padding(.vertical, 4).accessibilityIdentifier("yourAgent.change")
                if let place = reservation.place {
                    Text(String(format: String(localized: "You're #%lld in line."), place))
                        .font(.subheadline.weight(.semibold)).padding(.top, 4)
                }
                VStack(alignment: .leading, spacing: 4) {
                    Text("Move up the line").font(.subheadline.weight(.semibold))
                    Text("Each person who requests a page from your link moves you up 10 places.")
                        .font(.footnote).foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    Button("Share your link") { showsShare = true }
                        .buttonStyle(YansuPrimaryButtonStyle()).padding(.top, 8)
                        .accessibilityIdentifier("yourAgent.share")
                }
                .padding(.horizontal, 14).padding(.vertical, 12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(YansuTheme.accentWash, in: RoundedRectangle(cornerRadius: 12))
                .padding(.top, 6)
            }
            .padding(.horizontal, 16).padding(.vertical, 14)
        }.accessibilityElement(children: .contain).accessibilityIdentifier("yourAgent.reserved")
    }

    /// An earlier request, made before codes: confirm it with a code to keep the name.
    private func confirmCard(_ reservation: AgentReservation) -> some View {
        group {
            VStack(alignment: .leading, spacing: 6) {
                Text("Confirm it's you").font(.body.weight(.semibold))
                Text(String(format: String(localized: "Requests now need a code from your email. Send one to %@ to keep isoform.ai/agents/%@."),
                            reservation.email, reservation.name))
                    .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Button("Send code") { Task { await store.sendCode() } }
                    .buttonStyle(YansuPrimaryButtonStyle()).disabled(store.inFlight)
                    .padding(.top, 8).accessibilityIdentifier("yourAgent.sendCode")
                Button("Change") { store.change() }.foregroundStyle(YansuTheme.accent)
                    .padding(.vertical, 4).accessibilityIdentifier("yourAgent.change")
                cardStatus
            }
            .padding(.horizontal, 16).padding(.vertical, 14)
        }.accessibilityElement(children: .contain).accessibilityIdentifier("yourAgent.confirm")
    }

    private var shareText: String {
        let chinese = locale.identifier.hasPrefix("zh")
        let names = (store.isStillLearning ? [] : store.copy?.specialties ?? []).prefix(3).map { $0.name.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
        let joined: String
        if names.count < 2 { joined = names.first ?? "" }
        else if chinese { joined = names.dropLast().joined(separator: "、") + "和" + names.last! }
        else { joined = names.dropLast().joined(separator: ", ") + " and " + names.last! }
        let work = String(joined.trimmingCharacters(in: .whitespacesAndNewlines.union(.punctuationCharacters)).prefix(chinese ? 30 : 60))
        if work.isEmpty { return String(localized: "agent.share.withoutWork", locale: locale) }
        return String(format: String(localized: "agent.share.withWork", locale: locale), work)
    }
    private var writeBo: some View {
        Button {
            if MFMailComposeViewController.canSendMail() { showsMail = true }
            else { UIPasteboard.general.string = "bo@isoform.ai"; copied = true }
        } label: {
            Text(copied ? AttributedString(String(localized: "Copied bo@isoform.ai")) : emailLine)
                .font(.footnote).foregroundStyle(YansuTheme.tertiary)
                .fixedSize(horizontal: false, vertical: true).multilineTextAlignment(.leading)
        }.buttonStyle(.plain).padding(.horizontal, 16).accessibilityIdentifier("yourAgent.writeBo")
    }
    private var emailLine: AttributedString {
        var result = AttributedString(String(localized: "Ideas, or want in early? Email Bo at bo@isoform.ai."))
        if let range = result.range(of: "bo@isoform.ai") { result[range].foregroundColor = YansuTheme.accent }
        return result
    }
    private var howItWorks: some View {
        VStack(alignment: .leading, spacing: 5) {
            bullet("Clients ask for anything; it's best at your specialties.")
            bullet("You set your rate, and can offer your review for a fee.")
            bullet("Your memory, Mail and Messages never go into it.")
        }.font(.system(size: howSize)).foregroundStyle(YansuTheme.secondary).padding(.horizontal, 16)
    }
    private func bullet(_ text: LocalizedStringKey) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Circle().fill(YansuTheme.tertiary).frame(width: 2, height: 2).padding(.top, 7)
            Text(text).fixedSize(horizontal: false, vertical: true)
        }
    }
}

private struct AgentMailComposer: UIViewControllerRepresentable {
    let subject: String
    @Environment(\.dismiss) private var dismiss
    func makeCoordinator() -> Coordinator { Coordinator(dismiss: { dismiss() }) }
    func makeUIViewController(context: Context) -> MFMailComposeViewController {
        let controller = MFMailComposeViewController()
        controller.mailComposeDelegate = context.coordinator
        controller.setToRecipients(["bo@isoform.ai"])
        controller.setSubject(subject)
        return controller
    }
    func updateUIViewController(_ controller: MFMailComposeViewController, context: Context) { }
    final class Coordinator: NSObject, MFMailComposeViewControllerDelegate {
        let dismiss: () -> Void
        init(dismiss: @escaping () -> Void) { self.dismiss = dismiss }
        func mailComposeController(_ controller: MFMailComposeViewController,
                                   didFinishWith result: MFMailComposeResult, error: Error?) { dismiss() }
    }
}

private struct AgentShareSheet: UIViewControllerRepresentable {
    let items: [Any]
    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }
    func updateUIViewController(_ controller: UIActivityViewController, context: Context) { }
}
