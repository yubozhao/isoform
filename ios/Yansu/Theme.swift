import SwiftUI

// The design system's two themes (docs/design-system/tokens.json): Paper in light mode, One Dark in dark mode.
// Brand violet: violet is Isoform, copper waits on you, and ink is the user.
// Essential text holds 4.5:1 on its surfaces. Tertiary is reserved for metadata at 12pt and above.
enum YansuTheme {
    static let background = dynamic(light: 0xf6f6f9, dark: 0x121217) // color-background
    static let surface = dynamic(light: 0xffffff, dark: 0x1a1a21) // color-panel
    static let surfaceWarm = dynamic(light: 0xf0f1f6, dark: 0x1f1f27) // color-panel-warm
    static let surfaceUser = dynamic(light: 0xebecf2, dark: 0x26262f) // color-panel-user
    static let border = dynamic(light: 0xe2e3ec, dark: 0x2a2b35) // color-border
    static let borderStrong = dynamic(light: 0xd4d6e2, dark: 0x353746) // color-border-strong
    static let borderSubtle = dynamic(light: 0xe8e9f0, dark: 0x23232c) // color-border-subtle
    static let ink = dynamic(light: 0x16161d, dark: 0xececf2) // color-ink
    static let secondary = dynamic(light: 0x5d5f6e, dark: 0xa6a8b8) // color-muted
    static let tertiary = dynamic(light: 0x66687c, dark: 0x868899) // color-subtle
    static let faint = dynamic(light: 0xb4b6c4, dark: 0x4f5162) // color-faint
    static let accent = dynamic(light: 0x4f5ed8, dark: 0x7a8aff) // color-accent
    static let accentWash = dynamic(light: 0x5968df, 0.1, dark: 0x7a8aff, 0.14) // color-accent-light
    static let accentWaiting = dynamic(light: 0x4f5ed8, 0.22, dark: 0x7a8aff, 0.26) // acc-bg2, a message waiting
    // The primary action: violet with its paired foreground in both themes.
    static let solid = dynamic(light: 0x5968df, dark: 0x7a8aff) // color-solid-btn-bg
    static let onSolid = dynamic(light: 0xffffff, dark: 0x101016) // color-solid-btn-fg
    // The secondary action beside a primary one: violet text and a matching outline.
    static let sampleText = dynamic(light: 0x4f5ed8, dark: 0x7a8aff) // color-sample-text
    static let sampleBorder = dynamic(light: 0x5968df, 0.35, dark: 0x7a8aff, 0.4) // color-sample-border
    // Prepared questions and the message box.
    static let chip = dynamic(light: 0xf0f1f6, dark: 0x1f1f27) // color-chip-bg
    static let chipBorder = dynamic(light: 0xd4d6e2, dark: 0x353746) // color-chip-border
    static let composer = dynamic(light: 0xffffff, dark: 0x1a1a21) // color-composer-bg
    static let composerBorder = dynamic(light: 0xd4d6e2, dark: 0x353746) // color-composer-border
    // Status: always paired with a word or an icon.
    static let error = dynamic(light: 0xa12538, dark: 0xf07380) // color-error
    static let errorWash = dynamic(light: 0xa12538, 0.12, dark: 0xf07380, 0.16) // color-error-bg
    static let errorBorder = dynamic(light: 0xa12538, 0.22, dark: 0xf07380, 0.3) // color-error-border
    static let success = dynamic(light: 0x2a7a44, dark: 0x62c98d) // color-success
    static let successWash = dynamic(light: 0x2a7a44, 0.12, dark: 0x62c98d, 0.16) // color-success-bg
    static let successBorder = dynamic(light: 0x2a7a44, 0.22, dark: 0x62c98d, 0.3) // color-success-border
    static let warning = dynamic(light: 0x9b4f1d, dark: 0xe8a066) // color-warning
    static let warningWash = dynamic(light: 0x9b4f1d, 0.12, dark: 0xe8a066, 0.16) // color-warning-bg
    static let warningBorder = dynamic(light: 0x9b4f1d, 0.22, dark: 0xe8a066, 0.3) // color-warning-border
    static let info = dynamic(light: 0x4f5ed8, dark: 0x7a8aff) // color-info
    static let infoWash = dynamic(light: 0x5968df, 0.12, dark: 0x7a8aff, 0.16) // color-info-bg
    static let infoBorder = dynamic(light: 0x5968df, 0.22, dark: 0x7a8aff, 0.3) // color-info-border

    static let cornerRadius: CGFloat = 14
    static let gutter: CGFloat = 16

    private static func dynamic(light: UInt32, _ lightAlpha: Double = 1,
                                dark: UInt32, _ darkAlpha: Double = 1) -> Color {
        Color(uiColor: UIColor { traits in
            traits.userInterfaceStyle == .dark ? rgb(dark, darkAlpha) : rgb(light, lightAlpha)
        })
    }

    private static func rgb(_ hex: UInt32, _ alpha: Double) -> UIColor {
        UIColor(red: Double((hex >> 16) & 0xff) / 255, green: Double((hex >> 8) & 0xff) / 255,
                blue: Double(hex & 0xff) / 255, alpha: alpha)
    }
}

extension View {
    // Full-screen neutral background behind a scrolling page.
    func yansuScreen() -> some View {
        background(YansuTheme.background.ignoresSafeArea())
    }
}

// Flat mark: two soft shapes on a neutral tile, in the accent ink. Replaces the earlier gradient mark.
struct YansuMark: View {
    var size: CGFloat = 56

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: size * 0.3, style: .continuous)
                .fill(YansuTheme.accentWash)
            Circle()
                .fill(YansuTheme.accent)
                .frame(width: size * 0.42, height: size * 0.42)
                .offset(x: -size * 0.08, y: -size * 0.06)
            Circle()
                .fill(YansuTheme.accent.opacity(0.45))
                .frame(width: size * 0.30, height: size * 0.30)
                .offset(x: size * 0.15, y: size * 0.13)
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

struct RefreshingMark: View {
    var size: CGFloat = 18
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 20, paused: reduceMotion)) { timeline in
            let closed = !reduceMotion &&
                timeline.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 2) < 0.12
            YansuMark(size: size)
                .scaleEffect(x: 1, y: closed ? 0.12 : 1)
                .animation(reduceMotion ? nil : .easeInOut(duration: 0.1), value: closed)
        }
    }
}

struct YansuCard<Content: View>: View {
    private let content: Content

    init(@ViewBuilder content: () -> Content) {
        self.content = content()
    }

    var body: some View {
        content
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(16)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                    .strokeBorder(YansuTheme.border, lineWidth: 1)
            }
    }
}

// Section labels use ink and sentence case. They are headings for a group of items, not buttons.
struct SectionLabel: View {
    let title: String
    var detail: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
                .accessibilityAddTraits(.isHeader)
            if let detail {
                Text(detail)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct YansuPrimaryButtonStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.body.weight(.semibold))
            .multilineTextAlignment(.center)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.vertical, 12)
            .padding(.horizontal, 16)
            .frame(maxWidth: .infinity, minHeight: 48)
            .foregroundStyle(YansuTheme.onSolid)
            .background(YansuTheme.solid, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
            .opacity(!isEnabled ? 0.45 : configuration.isPressed ? 0.8 : 1)
    }
}

struct YansuSecondaryButtonStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.body.weight(.semibold))
            .multilineTextAlignment(.center)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.vertical, 12)
            .padding(.horizontal, 16)
            .frame(maxWidth: .infinity, minHeight: 48)
            .foregroundStyle(YansuTheme.ink)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                    .strokeBorder(YansuTheme.border, lineWidth: 1)
            }
            .opacity(!isEnabled ? 0.45 : configuration.isPressed ? 0.7 : 1)
    }
}

/// A full-width pill for a pair of answers (RC 1): the primary in solid warm ink, the other in a warm outline.
struct YansuPillButtonStyle: ButtonStyle {
    var prominent: Bool
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .multilineTextAlignment(.center)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.vertical, 10)
            .padding(.horizontal, 18)
            .frame(maxWidth: .infinity, minHeight: 44)
            .foregroundStyle(prominent ? YansuTheme.onSolid : YansuTheme.sampleText)
            .background(prominent ? YansuTheme.solid : Color.clear, in: Capsule())
            .overlay {
                if !prominent { Capsule().strokeBorder(YansuTheme.sampleBorder, lineWidth: 1) }
            }
            .contentShape(Capsule())
            .opacity(!isEnabled ? 0.45 : configuration.isPressed ? 0.8 : 1)
    }
}

/// A prepared question: a small pill that puts its words in the box.
struct YansuChipButtonStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline)
            .foregroundStyle(YansuTheme.ink)
            .lineLimit(1)
            .padding(.horizontal, 14)
            .frame(minHeight: 36)
            .background(YansuTheme.chip, in: Capsule())
            .overlay { Capsule().strokeBorder(YansuTheme.chipBorder, lineWidth: 1) }
            .padding(.vertical, 4)
            .contentShape(Rectangle())
            .opacity(!isEnabled ? 0.45 : configuration.isPressed ? 0.7 : 1)
    }
}

// A quiet notice: a short message and an optional dismiss control. Never urgent in tone.
struct NoticeBanner: View {
    let message: String
    var systemImage: String = "info.circle"
    var dismiss: (() -> Void)?

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: systemImage)
                .foregroundStyle(YansuTheme.secondary)
                .padding(.top, 2)
                .accessibilityHidden(true)
            Text(message)
                .font(.subheadline)
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("notice.message")
                .frame(maxWidth: .infinity, alignment: .leading)
            if let dismiss {
                Button(action: dismiss) {
                    Image(systemName: "xmark")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(YansuTheme.tertiary)
                        .frame(width: 44, height: 44)
                }
                .accessibilityLabel("Dismiss")
            }
        }
        .padding(.leading, 14)
        .padding(.trailing, 6)
        .padding(.vertical, 8)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                .strokeBorder(YansuTheme.border, lineWidth: 1)
        }
        .accessibilityElement(children: .contain)
    }
}

struct EmptyStateCard: View {
    let title: String
    let detail: String

    var body: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 6) {
                Text(title)
                    .font(.headline)
                    .foregroundStyle(YansuTheme.ink)
                Text(detail)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}
