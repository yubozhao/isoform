import Foundation
import SwiftUI

enum DuoTab: Hashable {
    case today
    case chat
    case tasks
}

enum DuoStep: Hashable {
    case today
    case item(String)
    case topics
    case thread(String)
    case draft(String)
    case tasks
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
        let leftWidth: CGFloat = switch tab {
        case .today: hasSelection ? 360 : 250
        case .chat: 300
        case .tasks: 360
        }
        let left = CGRect(x: 0, y: 0, width: leftWidth, height: height)
        let right = CGRect(x: leftWidth + gutter, y: 0,
                           width: max(0, width - leftWidth - gutter - rightPaneToolbarInset),
                           height: height)
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
            case .chat: 0
            case .tasks: YansuTheme.gutter * 2
            }
            let close = CGRect(x: right.minX + inset, y: 16, width: 72, height: 40)
            return [metrics.foldX.map {
                clear(close, ofFoldAt: $0, containerWidth: metrics.width)
            } ?? close]
        }

        guard tab == .tasks else { return [] }

        let buttonWidth: CGFloat = 112
        let buttonSpacing: CGFloat = 12
        let rowX = right.minX + YansuTheme.gutter * 2
        let rowWidth = buttonWidth * 2 + buttonSpacing
        let row = CGRect(x: rowX, y: 16, width: rowWidth, height: 44)
        let clearedRow = metrics.foldX.map {
            clear(row, ofFoldAt: $0, containerWidth: metrics.width)
        } ?? row
        return [
            CGRect(x: clearedRow.minX, y: clearedRow.minY,
                   width: buttonWidth, height: clearedRow.height),
            CGRect(x: clearedRow.minX + buttonWidth + buttonSpacing, y: clearedRow.minY,
                   width: buttonWidth, height: clearedRow.height)
        ]
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
