import Foundation
import SwiftUI

enum DuoTab: Hashable {
    case today
    case watch
    case chat

    /// The rail's tabs, top to bottom: Today, Watch, Chat (phone-map v1.0.0 rule 4). Watch shows
    /// by the same rule as on the narrow bar (rule 80, `WatchRecordingsModel.showsWatchTab`).
    static func rail(showsWatch: Bool, showsChat: Bool = true) -> [DuoTab] {
        PhoneTabs.root(chatAvailable: showsChat, watchPaired: showsWatch).compactMap(DuoTab.init(routerTab:))
    }

    init?(routerTab: NotificationRouter.Tab) {
        switch routerTab {
        case .today: self = .today
        case .watch: self = .watch
        case .chat: self = .chat
        case .tasks, .settings: return nil
        }
    }

    /// The tab the wide layout shows for the app's selected tab. With no watch there is no
    /// Watch tab, and a link to it lands on Today.
    static func shown(for tab: NotificationRouter.Tab, showsWatch: Bool, showsChat: Bool = true) -> DuoTab {
        switch tab {
        case .chat: return showsChat ? .chat : .today
        case .watch: return showsWatch ? .watch : .today
        case .today, .tasks, .settings: return .today
        }
    }

    /// The app's tab a rail button selects.
    var routerTab: NotificationRouter.Tab {
        switch self {
        case .today: return .today
        case .watch: return .watch
        case .chat: return .chat
        }
    }
}

enum DuoStep: Hashable {
    case today
    case item(String)
    case topics
    case thread(String)
    case draft(String)
    case task(String)
}

struct DuoMetrics: Equatable {
    let width: CGFloat
    let height: CGFloat
    let foldX: CGFloat?

    var isWide: Bool {
        width >= DuoLayout.wideThreshold && foldX != nil
    }
}

struct DuoPaneFrames: Equatable {
    let left: CGRect
    let right: CGRect
    let toolbar: CGRect
    let fullScreen: CGRect
    let text: CGRect
}

enum DuoLayout {
    static let wideThreshold: CGFloat = 800
    static let gutter: CGFloat = 30
    static let foldClearance: CGFloat = 16
    static let rightPaneToolbarInset: CGFloat = 80
    static let headerToolbarClearance: CGFloat = 56
    /// Inside each pane, on both sides (phone-duo v1.0.0 rule 2).
    static let paneInset: CGFloat = 24
    /// Where both panes begin, just under the status bar (rule 5).
    static let paneTop: CGFloat = 8
    static let textInset: CGFloat = 80
    static let coordinateSpaceName = "duo.root"

    static func metrics(width: CGFloat, height: CGFloat,
                        horizontalSizeClassIsRegular: Bool,
                        reportedFoldX: CGFloat? = nil) -> DuoMetrics {
        let wide = width >= wideThreshold && (horizontalSizeClassIsRegular || reportedFoldX != nil)
        return DuoMetrics(width: width, height: height,
                          foldX: wide ? (reportedFoldX ?? width / 2) : nil)
    }

    static func frames(for tab: DuoTab, metrics: DuoMetrics,
                       hasSelection: Bool = false, fullScreen: Bool = false) -> DuoPaneFrames {
        let width = metrics.width
        let height = metrics.height
        // The fold splits the page: the left pane runs from the edge to the fold, the right pane
        // from the fold to the rail. With no fold reported the split is the middle. The same for
        // every tab and every state; nothing crosses the fold (phone-duo v1.0.0 rule 1).
        let split = min(max(metrics.foldX ?? width / 2, 0), max(0, width - rightPaneToolbarInset))
        let left = CGRect(x: 0, y: 0, width: split, height: height)
        let right = CGRect(x: split, y: 0, width: max(0, width - split - rightPaneToolbarInset), height: height)
        let full = CGRect(x: 0, y: 0, width: width, height: height)
        let text = CGRect(x: textInset, y: 0,
                          width: max(0, width - textInset * 2), height: height)
        return DuoPaneFrames(left: left, right: right,
                             toolbar: CGRect(x: max(0, width - 64), y: 0, width: 64, height: height),
                             fullScreen: fullScreen ? full : .zero,
                             text: text)
    }

    static func clear(_ frame: CGRect, ofFoldAt foldX: CGFloat,
                      containerWidth: CGFloat, clearance: CGFloat = foldClearance) -> CGRect {
        guard frame.minX < foldX + clearance, frame.maxX > foldX - clearance else { return frame }
        let left = CGRect(x: min(frame.minX, foldX - clearance - frame.width),
                          y: frame.minY, width: frame.width, height: frame.height)
        let right = CGRect(x: max(frame.minX, foldX + clearance),
                           y: frame.minY, width: frame.width, height: frame.height)
        let leftFits = left.minX >= 0
        let rightFits = right.maxX <= containerWidth
        if rightFits && (!leftFits || frame.midX >= foldX) { return right }
        if leftFits { return left }
        return right
    }

    static func controlFrames(for tab: DuoTab, metrics: DuoMetrics,
                              hasSelection: Bool) -> [CGRect] {
        let right = frames(for: tab, metrics: metrics, hasSelection: hasSelection).right
        if hasSelection {
            let inset: CGFloat = switch tab {
            case .today: YansuTheme.gutter
            case .watch, .chat: 0
            }
            let close = CGRect(x: right.minX + inset, y: 16, width: 72, height: 40)
            return [metrics.foldX.map {
                clear(close, ofFoldAt: $0, containerWidth: metrics.width)
            } ?? close]
        }

        return []
    }
}

/// A column that never straddles the fold: when it would, it starts just right of it, so every row in it (Your work's
/// item and its Yes and No) stays on one side (phone-today v1.x on a wide phone).
struct DuoFoldClearColumn: ViewModifier {
    let foldX: CGFloat?
    /// Narrower than this right of the fold, the column stays as it is.
    var minimumWidth: CGFloat = 300
    @State private var inset: CGFloat = 0

    func body(content: Content) -> some View {
        content
            .padding(.leading, inset)
            .background(GeometryReader { geometry in
                Color.clear
                    .onAppear { update(geometry.frame(in: .named(DuoLayout.coordinateSpaceName))) }
                    .onChange(of: geometry.frame(in: .named(DuoLayout.coordinateSpaceName))) { _, frame in update(frame) }
            })
    }

    private func update(_ frame: CGRect) {
        let next = DuoLayout.foldClearInset(frame, foldX: foldX, minimumWidth: minimumWidth)
        if next != inset { inset = next }
    }
}

extension DuoLayout {
    /// How far a column must move right to start clear of the fold; 0 when it doesn't cross it or wouldn't fit.
    static func foldClearInset(_ frame: CGRect, foldX: CGFloat?, minimumWidth: CGFloat = 300,
                               clearance: CGFloat = foldClearance) -> CGFloat {
        guard let foldX, frame.minX < foldX + clearance, frame.maxX > foldX - clearance else { return 0 }
        let start = foldX + clearance
        guard frame.maxX - start >= minimumWidth else { return 0 }
        return start - frame.minX
    }
}

extension View {
    func duoFoldClear(_ foldX: CGFloat?) -> some View { modifier(DuoFoldClearColumn(foldX: foldX)) }
}

/// The one title of a tab on the open wide phone: at the top of the left pane, in the large
/// title style, scrolling with the pane (phone-duo v1.0.0 rules 3 and 6).
struct DuoPaneTitle: View {
    let text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text)
            .font(.largeTitle.weight(.bold))
            .foregroundStyle(YansuTheme.ink)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier("duo.pane.title")
    }
}

/// Full screen for the page open in the right pane; the wide layout sets it, a page's bar shows it.
struct DuoFullScreen {
    let isOn: Bool
    let toggle: () -> Void
}

private struct DuoFullScreenKey: EnvironmentKey {
    static let defaultValue: DuoFullScreen? = nil
}

extension EnvironmentValues {
    var duoFullScreen: DuoFullScreen? {
        get { self[DuoFullScreenKey.self] }
        set { self[DuoFullScreenKey.self] = newValue }
    }
}

/// The Full screen button at the trailing end of an opened page's bar (rule 7). Nothing on a
/// narrow phone.
struct DuoFullScreenButton: View {
    @Environment(\.duoFullScreen) private var fullScreen

    var body: some View {
        if let fullScreen {
            Button(action: fullScreen.toggle) {
                Image(systemName: fullScreen.isOn ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right")
            }
            .accessibilityLabel(String(localized: fullScreen.isOn ? "Exit full screen" : "Full screen"))
            .accessibilityIdentifier("duo.fullScreen.toggle")
        }
    }
}

/// A page opened in the right pane: it gets the system bar, in that pane only, with a back button
/// named for the tab, its title inline and Full screen at the trailing end. The bar is solid in
/// the page's color and the content starts under it (phone-duo v1.0.0 rule 7).
struct DuoOpenedPage<Content: View>: View {
    let back: String
    let title: String
    let onBack: () -> Void
    @ViewBuilder let content: () -> Content

    var body: some View {
        NavigationStack {
            content()
                .navigationTitle(title)
                .navigationBarTitleDisplayMode(.inline)
                .navigationBarBackButtonHidden(true)
                .toolbarBackground(YansuTheme.background, for: .navigationBar)
                .toolbarBackground(.visible, for: .navigationBar)
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button(action: onBack) {
                            HStack(spacing: 4) {
                                Image(systemName: "chevron.backward").font(.body.weight(.semibold))
                                Text(back)
                            }
                        }
                        .accessibilityIdentifier("duo.page.back")
                    }
                    ToolbarItem(placement: .topBarTrailing) { DuoFullScreenButton() }
                }
        }
    }
}

struct DuoFoldSafeControl<Content: View>: View {
    let foldX: CGFloat?
    let windowWidth: CGFloat
    let width: CGFloat
    let height: CGFloat
    let content: Content

    init(foldX: CGFloat?, windowWidth: CGFloat, width: CGFloat, height: CGFloat,
         @ViewBuilder content: () -> Content) {
        self.foldX = foldX
        self.windowWidth = windowWidth
        self.width = width
        self.height = height
        self.content = content()
    }

    var body: some View {
        GeometryReader { geometry in
            let frame = geometry.frame(in: .named(DuoLayout.coordinateSpaceName))
            let cleared = foldX.map {
                DuoLayout.clear(frame, ofFoldAt: $0, containerWidth: windowWidth)
            } ?? frame
            content
                .frame(width: width, height: height)
                .offset(x: cleared.minX - frame.minX)
        }
        .frame(width: width, height: height)
    }
}
