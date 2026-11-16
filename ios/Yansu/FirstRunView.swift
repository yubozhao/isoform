import SwiftUI

/// First run (phone-setup v1.1.0, board frames 9a to 9e): four screens on what Isoform does, in the app's order of
/// importance, then one job: pair. Nothing is asked on these screens; the camera is asked at Scan the code.
struct FirstRunView: View {
    @ObservedObject var firstRun: FirstRunStore
    @EnvironmentObject private var router: NotificationRouter
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var page = 0

    static let pageCount = 4

    var body: some View {
        Group {
            if page < Self.pageCount {
                onboarding
            } else {
                pairScreen
            }
        }
        .background(YansuTheme.surface.ignoresSafeArea())
        .animation(reduceMotion ? .easeOut(duration: 0.2) : .easeInOut(duration: 0.3), value: page)
    }

    // MARK: 9a to 9d

    private var onboarding: some View {
        VStack(spacing: 0) {
            HStack {
                Spacer()
                Button(String(localized: "ob.skip")) { page = Self.pageCount }
                    .font(.body)
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("welcome.skip")
                    .frame(minHeight: 44)
            }
            .padding(.horizontal, YansuTheme.gutter + 4)
            TabView(selection: $page) {
                ForEach(0..<Self.pageCount, id: \.self) { index in
                    OnboardingPage(index: index).tag(index)
                }
            }
            .tabViewStyle(.page(indexDisplayMode: .never))
            HStack(spacing: 6) {
                ForEach(0..<Self.pageCount, id: \.self) { index in
                    Capsule()
                        .fill(index == page ? YansuTheme.accent : YansuTheme.borderStrong)
                        .frame(width: index == page ? 18 : 6, height: 6)
                }
            }
            .accessibilityElement()
            .accessibilityLabel(String.localizedStringWithFormat(String(localized: "work.position"), page + 1, Self.pageCount))
            .padding(.bottom, 16)
            Button(String(localized: "ob.continue")) { page += 1 }
                .buttonStyle(YansuPrimaryButtonStyle())
                .accessibilityIdentifier("welcome.continue")
                .padding(.horizontal, YansuTheme.gutter + 4)
                .padding(.bottom, 12)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("welcome.onboarding")
    }

    // MARK: 9e

    private var pairScreen: some View {
        VStack(spacing: 18) {
            Spacer()
            YansuMark(size: 84)
            Text("setup.pairTitle")
                .font(.system(size: 30, weight: .bold))
                .multilineTextAlignment(.center)
                .foregroundStyle(YansuTheme.ink)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("welcome.title")
            Text("setup.pairBody")
                .font(.body)
                .multilineTextAlignment(.center)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Spacer()
            Button(String(localized: "setup.scan")) { firstRun.pair(using: router, scanCode: true) }
                .buttonStyle(YansuPrimaryButtonStyle())
                .accessibilityIdentifier("welcome.pair")
            Text("setup.noApp")
                .font(.footnote)
                .multilineTextAlignment(.center)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("welcome.download")
        }
        .padding(.horizontal, YansuTheme.gutter + 8)
        .padding(.bottom, 24)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("welcome.pairScreen")
    }
}

/// One onboarding screen: the real thing in a soft panel, a title, one or two sentences.
private struct OnboardingPage: View {
    let index: Int

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack { Spacer(minLength: 0); sample; Spacer(minLength: 0) }
                .padding(18)
                .frame(maxWidth: .infinity)
                .frame(height: 340)
                .background(YansuTheme.accentWash, in: RoundedRectangle(cornerRadius: 28))
                .accessibilityElement(children: .combine)
            if index == 2 {
                Text("ob.3.label")
                    .font(.footnote.weight(.semibold))
                    .textCase(.uppercase)
                    .foregroundStyle(YansuTheme.accent)
            }
            Text(title)
                .font(.system(size: 26, weight: .bold))
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
            Text(body(index))
                .font(.body)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, YansuTheme.gutter + 4)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("welcome.page.\(index)")
    }

    private var title: LocalizedStringKey {
        ["ob.1.title", "ob.2.title", "ob.3.title", "ob.4.title"][index]
    }

    private func body(_ index: Int) -> LocalizedStringKey {
        ["ob.1.body", "ob.2.body", "ob.3.body", "ob.4.body"][index]
    }

    /// The app's own parts with sample content (rule 4).
    @ViewBuilder
    private var sample: some View {
        switch index {
        case 0:
            panel {
                Text("ob.sample.briefTitle").font(.subheadline.weight(.semibold)).foregroundStyle(YansuTheme.ink)
                HStack(spacing: 10) {
                    Image(systemName: "play.fill").font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(YansuTheme.onSolid).frame(width: 32, height: 32)
                        .background(YansuTheme.solid, in: Circle())
                    Text("today.brief.hear").font(.subheadline.weight(.medium)).foregroundStyle(YansuTheme.ink)
                    Text(String.localizedStringWithFormat(String(localized: "today.brief.length"), 2))
                        .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                }
                Text("ob.sample.briefText").font(.subheadline).foregroundStyle(YansuTheme.ink)
                    .fixedSize(horizontal: false, vertical: true)
            }
        case 1:
            VStack(spacing: 10) {
                panel {
                    Text("ob.sample.itemSource").font(.caption).foregroundStyle(YansuTheme.secondary)
                    Text("ob.sample.itemTitle").font(.subheadline.weight(.semibold)).foregroundStyle(YansuTheme.ink)
                    WorkAnswerButtons(height: 34, answer: { _ in }).allowsHitTesting(false)
                }
                Text("ob.2.agents").font(.caption).foregroundStyle(YansuTheme.secondary)
                HStack(spacing: 6) {
                    ForEach(["Claude", "ChatGPT", "Codex", "Muse", "Instinct"], id: \.self) { name in
                        Text(verbatim: name).font(.caption.weight(.medium)).foregroundStyle(YansuTheme.ink)
                            .padding(.horizontal, 7).padding(.vertical, 3)
                            .background(YansuTheme.surface, in: Capsule())
                    }
                }
                .lineLimit(1)
                .minimumScaleFactor(0.7)
            }
        case 2:
            panel {
                HStack {
                    YansuMark(size: 18)
                    Text("ob.sample.talkTitle").font(.subheadline.weight(.semibold)).foregroundStyle(YansuTheme.ink)
                    Spacer()
                    Text("ob.sample.talkLength").font(.caption).foregroundStyle(YansuTheme.secondary)
                }
                Text("ob.sample.talkNote1").font(.subheadline).foregroundStyle(YansuTheme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                Text("ob.sample.talkNote2").font(.subheadline).foregroundStyle(YansuTheme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                Text(String.localizedStringWithFormat(String(localized: "work.count"), 1))
                    .font(.caption.weight(.semibold)).foregroundStyle(YansuTheme.onSolid)
                    .padding(.horizontal, 7).padding(.vertical, 3)
                    .background(YansuTheme.solid, in: Capsule())
            }
        default:
            VStack(spacing: 10) {
                panel {
                    Toggle(isOn: .constant(false)) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("ob.sample.closedEyes").font(.subheadline).foregroundStyle(YansuTheme.ink)
                            Text("ob.sample.closedEyesDetail").font(.caption).foregroundStyle(YansuTheme.secondary)
                        }
                    }
                    .allowsHitTesting(false)
                }
                panel {
                    HStack(spacing: 10) {
                        Image(systemName: "laptopcomputer").foregroundStyle(YansuTheme.accent)
                        VStack(alignment: .leading, spacing: 2) {
                            Text("ob.sample.kept").font(.subheadline.weight(.medium)).foregroundStyle(YansuTheme.ink)
                            Text("ob.sample.keptDetail").font(.caption).foregroundStyle(YansuTheme.secondary)
                        }
                    }
                }
            }
        }
    }

    private func panel<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 10) { content() }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 16))
    }
}
