import SwiftUI
import UIKit

/// P164: the bottom gesture zone is the strip where the home indicator lives. A page's scroll area ends where that
/// zone begins, so nothing scrolls under the home indicator and no control rests where a tap cannot reach it.
/// The page's last content keeps its own bottom margin above that edge.
extension View {
    /// Ends the scroll area above the bottom safe area (the gesture zone). Apply it to a page's scroll view.
    /// The keyboard is handled by SwiftUI as usual, so a form stays above it while it is up.
    func endsAboveGestureZone() -> some View {
        modifier(EndsAboveGestureZone())
    }
}

enum GestureZone {
    /// The window's bottom safe area: 34 pt on an iPhone with a home indicator, 0 on a home-button iPhone.
    static var bottomInset: CGFloat {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        return scenes.flatMap(\.windows).first(where: \.isKeyWindow)?.safeAreaInsets.bottom ?? 0
    }
}

private struct EndsAboveGestureZone: ViewModifier {
    func body(content: Content) -> some View {
        content.padding(.bottom, GestureZone.bottomInset)
    }
}
