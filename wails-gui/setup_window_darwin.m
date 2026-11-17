// First-run setup v2, native half. Decisions and bookkeeping are in
// setup_window.go; this file draws and animates with Core Animation, so the
// per-frame work runs on the render server, not on the main thread.
//
// Values follow docs/handoff-2026-10-28/setup-2.0 (the prototype's .mbmark, .callout
// and finish()). Timings: window fade 0.28 s, flight 0.72 s with ease in and out
// (cubic-bezier 0.45, 0, 0.55, 1) and a 70 pt arc, icon 72 pt to 18 pt, fade over
// the last 18 %, rings 2 pt x 1.0 s, 0.7 to 1.5 scale, .9 to 0 opacity.

#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#include <math.h>
#include <string.h>
#import "setup_window_darwin.h"
#import "main_window_darwin.h"

// Defined in setup_window_darwin.go (exported to C).
extern void goSetupFinishLanded(void);

// The status item belongs to the menu bar code; it is registered here, not retained.
static NSStatusItem *_markItem = nil;
static BOOL _markStartsVisible = YES;

// The callout text for the landing in progress (owned).
static NSString *_pendingCallout = nil;

// The flight panel and the callout panel (owned while they are on screen).
static NSPanel *_flightPanel = nil;
static NSPanel *_calloutPanel = nil;
static int _calloutToken = 0;

// ── Helpers ───────────────────────────────────────────────────────────

static void isoformRunOnMainSync(void (^block)(void)) {
    if ([NSThread isMainThread]) {
        block();
        return;
    }
    dispatch_sync(dispatch_get_main_queue(), block);
}

static BOOL isoformIsDarkAppearance(NSAppearance *appearance) {
    if (!appearance) {
        return NO;
    }
    NSString *best = [appearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
    return [best isEqualToString:NSAppearanceNameDarkAqua];
}

static NSColor *isoformRGB(int r, int g, int b) {
    return [NSColor colorWithSRGBRed:r / 255.0 green:g / 255.0 blue:b / 255.0 alpha:1.0];
}

// CSS cubic-bezier(0.45, 0, 0.55, 1): the eased progress for a linear progress x.
static double isoformBezier(double a, double b, double s) {
    double u = 1.0 - s;
    return 3.0 * u * u * s * a + 3.0 * u * s * s * b + s * s * s;
}

static double isoformEaseInOut(double x) {
    double lo = 0.0, hi = 1.0;
    for (int i = 0; i < 48; i++) {
        double mid = (lo + hi) / 2.0;
        if (isoformBezier(0.45, 0.55, mid) < x) {
            lo = mid;
        } else {
            hi = mid;
        }
    }
    return isoformBezier(0.0, 1.0, (lo + hi) / 2.0);
}

// The union of every display, where the flight panel and its icon may go.
static NSRect isoformAllScreensFrame(void) {
    NSRect united = NSZeroRect;
    BOOL first = YES;
    for (NSScreen *screen in NSScreen.screens) {
        united = first ? screen.frame : NSUnionRect(united, screen.frame);
        first = NO;
    }
    return united;
}

// A web point (top-left origin of the window content) in AppKit screen coordinates.
static NSPoint isoformScreenPointForWebPoint(NSWindow *window, double x, double y) {
    NSView *view = window.contentView;
    CGFloat localY = view.isFlipped ? y : NSHeight(view.bounds) - y;
    NSPoint inWindow = [view convertPoint:NSMakePoint(x, localY) toView:nil];
    return [window convertPointToScreen:inWindow];
}

static void isoformFadeWindow(NSWindow *window, CGFloat alpha, CGFloat duration) {
    [NSAnimationContext runAnimationGroup:^(NSAnimationContext *context) {
        context.duration = duration;
        // CSS "ease".
        context.timingFunction = [CAMediaTimingFunction functionWithControlPoints:0.25:0.1:0.25:1.0];
        [window.animator setAlphaValue:alpha];
    } completionHandler:nil];
}

// ── Menu bar mark ─────────────────────────────────────────────────────

void isoformSetupMarkItemSet(void *item) {
    _markItem = (NSStatusItem *)item;
}

void isoformSetupMarkSetStartsVisible(int visible) {
    _markStartsVisible = visible != 0;
}

int isoformSetupMarkStartsVisible(void) {
    return _markStartsVisible ? 1 : 0;
}

// Two rings around the mark (CSS .mbmark.pulse::after): a 2 pt violet rounded square,
// 22 pt, corner 7, scaling 0.7 to 1.5 while the opacity goes .9 to 0, 1.0 s per cycle,
// ease-out. The ring layer is removed when the cycles are done.
static void isoformPlayMarkRings(NSStatusItem *item, NSInteger cycles, CFTimeInterval delay) {
    NSStatusBarButton *button = item.button;
    if (!button || cycles < 1) {
        return;
    }
    button.wantsLayer = YES;
    CALayer *host = button.layer;
    NSRect bounds = button.bounds;
    CGFloat side = 22.0;

    CALayer *ring = [CALayer layer];
    ring.frame = CGRectMake(NSMidX(bounds) - side / 2.0, NSMidY(bounds) - side / 2.0, side, side);
    ring.cornerRadius = 7.0;
    ring.borderWidth = 2.0;
    NSColor *color = isoformIsDarkAppearance(button.effectiveAppearance)
        ? isoformRGB(0x7a, 0x8a, 0xff)
        : isoformRGB(0x59, 0x68, 0xdf);
    ring.borderColor = color.CGColor;
    ring.opacity = 0.0;
    [host addSublayer:ring];

    CABasicAnimation *grow = [CABasicAnimation animationWithKeyPath:@"transform.scale"];
    grow.fromValue = @0.7;
    grow.toValue = @1.5;
    grow.duration = 1.0;
    CABasicAnimation *fade = [CABasicAnimation animationWithKeyPath:@"opacity"];
    fade.fromValue = @0.9;
    fade.toValue = @0.0;
    fade.duration = 1.0;

    CAAnimationGroup *cycle = [CAAnimationGroup animation];
    cycle.animations = @[grow, fade];
    cycle.duration = 1.0;
    cycle.repeatCount = (float)cycles;
    cycle.timingFunction = [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseOut];
    if (delay > 0) {
        cycle.beginTime = [ring convertTime:CACurrentMediaTime() fromLayer:nil] + delay;
    }
    [ring addAnimation:cycle forKey:@"isoform.mark.ring"];

    CFTimeInterval total = delay + (CFTimeInterval)cycles * 1.0 + 0.1;
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(total * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
        [ring removeFromSuperlayer];
    });
}

// The mark's own appearance (CSS .mbmark.show): spring scale .5 to 1 with a fade,
// 0.35 s.
static void isoformAnimateMarkAppear(NSStatusItem *item) {
    NSStatusBarButton *button = item.button;
    if (!button) {
        return;
    }
    button.wantsLayer = YES;
    CALayer *layer = button.layer;

    CASpringAnimation *spring = [CASpringAnimation animationWithKeyPath:@"transform.scale"];
    spring.fromValue = @0.5;
    spring.toValue = @1.0;
    spring.mass = 1.0;
    spring.stiffness = 400.0;
    spring.damping = 23.0;
    spring.initialVelocity = 0.0;
    spring.duration = spring.settlingDuration;

    CABasicAnimation *fade = [CABasicAnimation animationWithKeyPath:@"opacity"];
    fade.fromValue = @0.0;
    fade.toValue = @1.0;
    fade.duration = 0.35;
    fade.timingFunction = [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseOut];

    [layer addAnimation:spring forKey:@"isoform.mark.spring"];
    [layer addAnimation:fade forKey:@"isoform.mark.fade"];
}

// Shows or hides the mark. Showing it with pulse, from hidden, plays the appearance
// and then the two rings.
int isoformSetupMarkSetVisible(int visible, int pulse) {
    __block int ok = 0;
    isoformRunOnMainSync(^{
        NSStatusItem *item = _markItem;
        if (!item) {
            return;
        }
        BOOL wasVisible = item.visible;
        item.visible = visible != 0;
        if (visible && !wasVisible && pulse) {
            isoformAnimateMarkAppear(item);
            isoformPlayMarkRings(item, 2, 0.35);
        }
        ok = 1;
    });
    return ok;
}

// ── Setup window size and styling ─────────────────────────────────────

static void isoformSetWindowSetupStyle(NSWindow *window, BOOL setup) {
    NSUInteger mask = window.styleMask;
    NSUInteger next = setup ? (mask & ~NSWindowStyleMaskResizable) : (mask | NSWindowStyleMaskResizable);
    if (next != mask) {
        window.styleMask = next;
    }
    [[window standardWindowButton:NSWindowZoomButton] setEnabled:!setup];
}

// A frame of the given content size, centred on the menu bar's display.
static NSRect isoformCenteredFrame(NSWindow *window, CGFloat width, CGFloat height) {
    NSRect frame = [window frameRectForContentRect:NSMakeRect(0, 0, width, height)];
    NSScreen *display = NSScreen.screens.firstObject ? NSScreen.screens.firstObject : NSScreen.mainScreen;
    NSRect visible = display ? display.visibleFrame : NSMakeRect(0, 0, 1440, 900);
    frame.origin.x = NSMidX(visible) - frame.size.width / 2.0;
    frame.origin.y = NSMidY(visible) - frame.size.height / 2.0;
    return frame;
}

// Applies a window mode (see setup_window.go). Main thread, synchronous.
int isoformSetupWindowApply(int mode, double x, double y, double w, double h) {
    __block int ok = 0;
    isoformRunOnMainSync(^{
        NSWindow *window = isoformMainWindow();
        if (!window) {
            return;
        }
        BOOL setup = (mode == 0 || mode == 1);
        isoformSetWindowSetupStyle(window, setup);
        switch (mode) {
            case 1:
                // The setup's size comes from Go (setupWindowSize); 600 x 560 is setup v2's.
                [window setFrame:isoformCenteredFrame(window, w > 0 ? w : 600, h > 0 ? h : 560) display:YES animate:NO];
                break;
            case 2:
                [window setFrame:NSMakeRect(x, y, w, h) display:YES animate:NO];
                break;
            case 3:
                [window setFrame:isoformCenteredFrame(window, w, h) display:YES animate:NO];
                break;
            default:
                break;
        }
        ok = 1;
    });
    return ok;
}

// The main window's frame in screen coordinates.
int isoformSetupWindowFrame(double *out) {
    __block int ok = 0;
    isoformRunOnMainSync(^{
        NSWindow *window = isoformMainWindow();
        if (!window) {
            return;
        }
        NSRect frame = window.frame;
        out[0] = frame.origin.x;
        out[1] = frame.origin.y;
        out[2] = frame.size.width;
        out[3] = frame.size.height;
        ok = 1;
    });
    return ok;
}

// Where the mark and the displays are, for the landing choice.
int isoformSetupLandingInputs(IsoformSetupLanding *out) {
    __block int ok = 0;
    isoformRunOnMainSync(^{
        memset(out, 0, sizeof(*out));
        NSArray<NSScreen *> *screens = NSScreen.screens;
        if (screens.count == 0) {
            return;
        }
        NSScreen *bar = screens.firstObject;
        out->menuBarTop = NSMaxY(bar.frame);
        out->menuBarVisible = [NSMenu menuBarVisible] ? 1 : 0;

        NSRect bounds = isoformAllScreensFrame();
        out->boundsX = NSMinX(bounds);
        out->boundsY = NSMinY(bounds);
        out->boundsW = NSWidth(bounds);
        out->boundsH = NSHeight(bounds);

        NSWindow *window = isoformMainWindow();
        NSScreen *windowScreen = window.screen ? window.screen : (NSScreen.mainScreen ? NSScreen.mainScreen : bar);
        NSRect windowFrame = windowScreen.frame;
        out->windowX = NSMinX(windowFrame);
        out->windowY = NSMinY(windowFrame);
        out->windowW = NSWidth(windowFrame);
        out->windowH = NSHeight(windowFrame);

        if (@available(macOS 12.0, *)) {
            NSRect left = bar.auxiliaryTopLeftArea;
            NSRect right = bar.auxiliaryTopRightArea;
            if (NSWidth(left) > 0 && NSWidth(right) > 0 && NSMaxX(left) < NSMinX(right)) {
                out->hasNotch = 1;
                out->notchX = NSMaxX(left);
                out->notchW = NSMinX(right) - NSMaxX(left);
            }
        }

        NSStatusItem *item = _markItem;
        if (item && item.visible && item.button.window) {
            NSRect rect = [item.button convertRect:item.button.bounds toView:nil];
            rect = [item.button.window convertRectToScreen:rect];
            out->itemVisible = item.button.window.isVisible ? 1 : 0;
            out->itemX = NSMinX(rect);
            out->itemY = NSMinY(rect);
            out->itemW = NSWidth(rect);
            out->itemH = NSHeight(rect);
        }
        ok = 1;
    });
    return ok;
}

// ── Callout ───────────────────────────────────────────────────────────

static void isoformFadeOutCallout(void);

// Passes clicks on to the fade-out. The panel never takes key status.
@interface IsoformSetupCalloutView : NSView
@end

@implementation IsoformSetupCalloutView
- (BOOL)acceptsFirstMouse:(NSEvent *)event {
    return YES;
}
- (void)mouseDown:(NSEvent *)event {
    isoformFadeOutCallout();
}
@end

static void isoformRemovePanel(NSPanel *panel) {
    [panel orderOut:nil];
    [panel release];
}

static void isoformFadeOutCallout(void) {
    NSPanel *panel = _calloutPanel;
    if (!panel) {
        return;
    }
    _calloutPanel = nil;
    _calloutToken++;
    [NSAnimationContext runAnimationGroup:^(NSAnimationContext *context) {
        context.duration = 0.3;
        context.timingFunction = [CAMediaTimingFunction functionWithControlPoints:0.25:0.1:0.25:1.0];
        [panel.animator setAlphaValue:0.0];
    } completionHandler:^{
        isoformRemovePanel(panel);
    }];
}

// The callout under the mark (CSS .callout): panel fill and 1 pt border, corner 10,
// padding 8 x 12, 12.5 pt system font, shadows 0 1 2 .06 and 0 18 50 .16, and a
// 10 pt square tip pointing up at the mark. It rises 4 pt as it fades in over 0.3 s,
// stays 6 s or until clicked, then fades.
static void isoformShowCallout(double anchorX, double anchorY, NSString *text) {
    if (_calloutPanel) {
        NSPanel *old = _calloutPanel;
        _calloutPanel = nil;
        _calloutToken++;
        isoformRemovePanel(old);
    }
    NSScreen *bar = NSScreen.screens.firstObject;
    NSRect screen = bar ? bar.frame : NSMakeRect(0, 0, 1440, 900);

    BOOL dark = isoformIsDarkAppearance(NSApp.effectiveAppearance);
    NSColor *fill = dark ? isoformRGB(0x1a, 0x1a, 0x21) : isoformRGB(0xff, 0xff, 0xff);
    NSColor *edge = dark ? isoformRGB(0x2a, 0x2b, 0x35) : isoformRGB(0xe2, 0xe3, 0xec);
    NSColor *ink = dark ? isoformRGB(0xec, 0xec, 0xf2) : isoformRGB(0x16, 0x16, 0x1d);
    NSColor *shadow = isoformRGB(0x16, 0x16, 0x1d);

    NSFont *font = [NSFont systemFontOfSize:12.5];
    NSSize textSize = [text sizeWithAttributes:@{NSFontAttributeName: font}];
    // Padding 8 x 12 plus the 1 pt border on every side.
    CGFloat cardW = ceil(textSize.width) + 26.0;
    CGFloat cardH = ceil(textSize.height) + 18.0;
    CGFloat tipRoom = 9.0;
    CGFloat panelW = cardW;
    CGFloat panelH = cardH + tipRoom;

    // Right-aligned to the mark, kept on the display, with the tip on the mark's centre.
    CGFloat left = anchorX + 28.0 - panelW;
    left = MAX(NSMinX(screen) + 8.0, MIN(left, NSMaxX(screen) - 8.0 - panelW));
    CGFloat tipX = MAX(14.0, MIN(anchorX - left, panelW - 14.0));
    CGFloat top = anchorY - 2.0;
    NSRect frame = NSMakeRect(left, top - panelH, panelW, panelH);

    NSPanel *panel = [[NSPanel alloc] initWithContentRect:frame
                                                styleMask:(NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel)
                                                  backing:NSBackingStoreBuffered
                                                    defer:NO];
    panel.opaque = NO;
    panel.backgroundColor = [NSColor clearColor];
    panel.hasShadow = NO;
    panel.level = NSStatusWindowLevel + 1;
    panel.hidesOnDeactivate = NO;
    panel.releasedWhenClosed = NO;
    panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces
        | NSWindowCollectionBehaviorFullScreenAuxiliary
        | NSWindowCollectionBehaviorStationary
        | NSWindowCollectionBehaviorIgnoresCycle;

    IsoformSetupCalloutView *content = [[IsoformSetupCalloutView alloc] initWithFrame:NSMakeRect(0, 0, panelW, panelH)];
    content.wantsLayer = YES;
    panel.contentView = content;
    [content release];
    CALayer *root = content.layer;

    CGRect card = CGRectMake(0, 0, cardW, cardH);
    CGPathRef cardPath = CGPathCreateWithRoundedRect(card, 10.0, 10.0, NULL);

    // Shadow 0 18 50 .16 (a blur of 50 is a radius of 25).
    CALayer *deep = [CALayer layer];
    deep.frame = card;
    deep.backgroundColor = fill.CGColor;
    deep.cornerRadius = 10.0;
    deep.shadowColor = shadow.CGColor;
    deep.shadowOpacity = 0.16;
    deep.shadowRadius = 25.0;
    deep.shadowOffset = CGSizeMake(0, -18);
    deep.shadowPath = cardPath;
    [root addSublayer:deep];

    // Shadow 0 1 2 .06.
    CALayer *near = [CALayer layer];
    near.frame = card;
    near.backgroundColor = fill.CGColor;
    near.cornerRadius = 10.0;
    near.shadowColor = shadow.CGColor;
    near.shadowOpacity = 0.06;
    near.shadowRadius = 1.0;
    near.shadowOffset = CGSizeMake(0, -1);
    near.shadowPath = cardPath;
    [root addSublayer:near];

    // The tip: a 10 pt square rotated 45 degrees, centred just above the card's top edge.
    CALayer *tip = [CALayer layer];
    tip.bounds = CGRectMake(0, 0, 10.0, 10.0);
    tip.position = CGPointMake(tipX, cardH + 1.0);
    tip.backgroundColor = fill.CGColor;
    tip.borderColor = edge.CGColor;
    tip.borderWidth = 1.0;
    tip.transform = CATransform3DMakeRotation(-M_PI_4, 0, 0, 1);
    [root addSublayer:tip];

    CALayer *body = [CALayer layer];
    body.frame = card;
    body.backgroundColor = fill.CGColor;
    body.cornerRadius = 10.0;
    body.borderColor = edge.CGColor;
    body.borderWidth = 1.0;
    [root addSublayer:body];
    CGPathRelease(cardPath);

    NSTextField *label = [NSTextField labelWithString:text];
    label.font = font;
    label.textColor = ink;
    label.frame = NSMakeRect(13.0, 9.0, ceil(textSize.width) + 2.0, ceil(textSize.height) + 2.0);
    [content addSubview:label];

    CABasicAnimation *rise = [CABasicAnimation animationWithKeyPath:@"transform.translation.y"];
    rise.fromValue = @4.0;
    rise.toValue = @0.0;
    rise.duration = 0.3;
    rise.timingFunction = [CAMediaTimingFunction functionWithControlPoints:0.25:0.1:0.25:1.0];
    CABasicAnimation *appear = [CABasicAnimation animationWithKeyPath:@"opacity"];
    appear.fromValue = @0.0;
    appear.toValue = @1.0;
    appear.duration = 0.3;
    appear.timingFunction = [CAMediaTimingFunction functionWithControlPoints:0.25:0.1:0.25:1.0];
    [root addAnimation:rise forKey:@"isoform.callout.rise"];
    [root addAnimation:appear forKey:@"isoform.callout.appear"];

    [panel orderFrontRegardless];
    _calloutPanel = panel;
    int token = ++_calloutToken;
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(6.0 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
        if (token == _calloutToken && _calloutPanel) {
            isoformFadeOutCallout();
        }
    });
}

// ── The finish ────────────────────────────────────────────────────────

static void isoformReleasePendingCallout(void) {
    if (_pendingCallout) {
        [_pendingCallout release];
        _pendingCallout = nil;
    }
}

// Called on landing: the rings, the callout, and the signal back to Go.
static void isoformFinishLanding(int ring, double anchorX, double anchorY) {
    if (_flightPanel) {
        NSPanel *panel = _flightPanel;
        _flightPanel = nil;
        isoformRemovePanel(panel);
    }
    if (ring && _markItem) {
        isoformPlayMarkRings(_markItem, 2, 0.0);
    }
    isoformShowCallout(anchorX, anchorY, _pendingCallout ? _pendingCallout : @"");
    isoformReleasePendingCallout();
    goSetupFinishLanded();
}

static void isoformRunFinish(IsoformSetupFinish params) {
    NSWindow *window = isoformMainWindow();
    BOOL reduce = params.reduceMotion != 0 || NSWorkspace.sharedWorkspace.accessibilityDisplayShouldReduceMotion;
    if (window) {
        isoformFadeWindow(window, 0.0, reduce ? 0.2 : 0.28);
    }

    if (reduce) {
        // No flight: ring once, show the callout, and report back after the fade.
        if (params.ring && _markItem) {
            isoformPlayMarkRings(_markItem, 1, 0.0);
        }
        isoformShowCallout(params.anchorX, params.anchorY, _pendingCallout ? _pendingCallout : @"");
        isoformReleasePendingCallout();
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.2 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
            goSetupFinishLanded();
        });
        return;
    }

    NSRect bounds = isoformAllScreensFrame();
    NSPanel *panel = [[NSPanel alloc] initWithContentRect:bounds
                                                styleMask:(NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel)
                                                  backing:NSBackingStoreBuffered
                                                    defer:NO];
    panel.opaque = NO;
    panel.backgroundColor = [NSColor clearColor];
    panel.hasShadow = NO;
    panel.ignoresMouseEvents = YES;
    panel.level = NSStatusWindowLevel + 1;
    panel.hidesOnDeactivate = NO;
    panel.releasedWhenClosed = NO;
    panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces
        | NSWindowCollectionBehaviorFullScreenAuxiliary
        | NSWindowCollectionBehaviorStationary
        | NSWindowCollectionBehaviorIgnoresCycle;
    NSView *content = panel.contentView;
    content.wantsLayer = YES;
    CGFloat scale = panel.backingScaleFactor > 0 ? panel.backingScaleFactor : 2.0;

    CGFloat size = params.iconSize > 0 ? params.iconSize : 72.0;
    CALayer *icon = [CALayer layer];
    icon.bounds = CGRectMake(0, 0, size, size);
    icon.contentsScale = scale;
    icon.contentsGravity = kCAGravityResizeAspect;
    CGImageRef image = NULL;
    NSImage *appIcon = NSApp.applicationIconImage;
    NSInteger pixels = (NSInteger)llround(size * 2.0);
    if (appIcon) {
        NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithBitmapDataPlanes:NULL
                                                                        pixelsWide:pixels
                                                                        pixelsHigh:pixels
                                                                     bitsPerSample:8
                                                                   samplesPerPixel:4
                                                                          hasAlpha:YES
                                                                          isPlanar:NO
                                                                    colorSpaceName:NSCalibratedRGBColorSpace
                                                                       bytesPerRow:0
                                                                      bitsPerPixel:0];
        if (rep) {
            [NSGraphicsContext saveGraphicsState];
            [NSGraphicsContext setCurrentContext:[NSGraphicsContext graphicsContextWithBitmapImageRep:rep]];
            [appIcon drawInRect:NSMakeRect(0, 0, pixels, pixels)
                       fromRect:NSZeroRect
                      operation:NSCompositingOperationSourceOver
                       fraction:1.0];
            [NSGraphicsContext restoreGraphicsState];
            if (rep.CGImage) {
                image = CGImageRetain(rep.CGImage);
            }
            [rep release];
        }
    }
    if (image) {
        icon.contents = (__bridge id)image;
        CGImageRelease(image);
    }

    NSPoint start = window ? isoformScreenPointForWebPoint(window, params.iconX, params.iconY)
                           : NSMakePoint(NSMidX(bounds), NSMidY(bounds));
    CGPoint from = CGPointMake(start.x - NSMinX(bounds), start.y - NSMinY(bounds));
    CGPoint to = CGPointMake(params.targetX - NSMinX(bounds), params.targetY - NSMinY(bounds));
    CGFloat endScale = 18.0 / size;

    // Final model values, so nothing snaps back when the animations end.
    icon.position = to;
    icon.opacity = 0.0;
    icon.transform = CATransform3DMakeScale(endScale, endScale, 1.0);
    [content.layer addSublayer:icon];

    // Baked keyframes: the eased progress t runs along the arc, and the icon fades over
    // the last 18 % of linear time (the prototype's flight).
    const NSInteger steps = 72;
    const CFTimeInterval duration = 0.72;
    NSMutableArray *positions = [NSMutableArray arrayWithCapacity:steps + 1];
    NSMutableArray *scales = [NSMutableArray arrayWithCapacity:steps + 1];
    NSMutableArray *times = [NSMutableArray arrayWithCapacity:steps + 1];
    for (NSInteger i = 0; i <= steps; i++) {
        CGFloat p = (CGFloat)i / (CGFloat)steps;
        CGFloat t = isoformEaseInOut(p);
        CGFloat x = from.x + (to.x - from.x) * t;
        CGFloat y = from.y + (to.y - from.y) * t + sin(M_PI * t) * 70.0;
        [positions addObject:[NSValue valueWithPoint:NSMakePoint(x, y)]];
        [scales addObject:@(1.0 + (endScale - 1.0) * t)];
        [times addObject:@(p)];
    }
    CAKeyframeAnimation *move = [CAKeyframeAnimation animationWithKeyPath:@"position"];
    move.values = positions;
    move.keyTimes = times;
    move.calculationMode = kCAAnimationLinear;
    move.duration = duration;

    CAKeyframeAnimation *shrink = [CAKeyframeAnimation animationWithKeyPath:@"transform.scale"];
    shrink.values = scales;
    shrink.keyTimes = times;
    shrink.calculationMode = kCAAnimationLinear;
    shrink.duration = duration;

    CAKeyframeAnimation *vanish = [CAKeyframeAnimation animationWithKeyPath:@"opacity"];
    vanish.values = @[@1.0, @1.0, @0.0];
    vanish.keyTimes = @[@0.0, @0.82, @1.0];
    vanish.calculationMode = kCAAnimationLinear;
    vanish.duration = duration;

    _flightPanel = panel;
    [panel orderFrontRegardless];

    int ring = params.ring ? 1 : 0;
    double anchorX = params.anchorX;
    double anchorY = params.anchorY;
    [CATransaction begin];
    [CATransaction setCompletionBlock:^{
        isoformFinishLanding(ring, anchorX, anchorY);
    }];
    [icon addAnimation:move forKey:@"isoform.flight.move"];
    [icon addAnimation:shrink forKey:@"isoform.flight.scale"];
    [icon addAnimation:vanish forKey:@"isoform.flight.opacity"];
    [CATransaction commit];

    // The window stays at alpha 0 after the fade, until RevealMainWindow.
}

// Runs the finish: the window fades, the icon flies, and the landing rings the mark
// and shows the callout. Returns at once; goSetupFinishLanded signals the landing.
void isoformSetupFinishPlay(const IsoformSetupFinish *params) {
    if (!params) {
        return;
    }
    IsoformSetupFinish copy = *params;
    char *text = strdup(params->callout ? params->callout : "");
    copy.callout = NULL;
    dispatch_async(dispatch_get_main_queue(), ^{
        NSString *owned = text ? [[NSString alloc] initWithUTF8String:text] : nil;
        if (text) {
            free(text);
        }
        isoformReleasePendingCallout();
        _pendingCallout = owned ? owned : [[NSString alloc] initWithString:@""];
        isoformRunFinish(copy);
    });
}

// ── Reveal ────────────────────────────────────────────────────────────

// Fades the main window in over duration (CSS ease). Setup mode is already off.
void isoformSetupRevealMainWindow(double duration) {
    dispatch_async(dispatch_get_main_queue(), ^{
        NSWindow *window = isoformMainWindow();
        if (!window) {
            return;
        }
        if (!window.isVisible) {
            [window orderFront:nil];
        }
        isoformFadeWindow(window, 1.0, duration);
    });
}
