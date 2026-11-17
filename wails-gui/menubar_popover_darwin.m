#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#import <WebKit/WebKit.h>
#import "menubar_popover_darwin.h"

extern void goMenuBarPopoverVisibility(_Bool open);
extern void goMenuBarCommand(char *command);
extern void goMenuBarPopoverStep(char *step);

static void logPopoverStep(NSString *step) {
    goMenuBarPopoverStep((char *)step.UTF8String);
}

// Like the toast host, this view owns only rendering and a bounded message
// bridge. All application reads and actions stay in Go, off the AppKit thread.
//
// A click never waits on Go (polish sweep 2026-10-27: a 7 s read meant the
// popover never appeared, or the native menu appeared instead). The page is
// loaded once, ahead of the first click, and kept between opens; a click
// shows it with the last model Go pushed, and showing it asks Go to refresh,
// which updates the open page in place.
//
// 1.0 (Isoform design, docs/handoff-2026-10-26/menu-bar-popover-1.0): the
// popover is our own borderless panel (no arrow, no system vibrancy; the page
// draws the border, corners and shadow), 5 pt below the menu bar and centred
// on the icon. A left click always shows it; right-click and Control-click
// show the short native menu.
@interface IsoformMenuBarPanel : NSPanel
@end
@implementation IsoformMenuBarPanel
- (BOOL)canBecomeKeyWindow { return YES; }
- (BOOL)canBecomeMainWindow { return NO; }
@end

@interface IsoformMenuBarPopover : NSObject <WKScriptMessageHandler, WKNavigationDelegate, NSWindowDelegate>
@property(retain) NSStatusItem *item;
@property(retain) NSMenu *menu;
@property(retain) NSMenu *legacyMenu;
@property(retain) NSImage *mark;
@property(retain) IsoformMenuBarPanel *panel;
@property(retain) id globalMonitor;
@property(retain) id localMonitor;
@property(retain) WKWebView *web;
@property(retain) NSString *pageHTML;
@property(retain) NSDictionary *model;
@property BOOL pageReady;
@property BOOL opening;
@property CGFloat pageHeight;
@property NSUInteger reloads;
- (void)closeWithReason:(NSString *)reason;
- (void)applyIcon;
@end

static IsoformMenuBarPopover *host;
static NSString *pendingModel;
static NSString *pendingPlayback;
static BOOL stopped;

// A page that has not answered by then is shown as it is: an empty popover
// that fills in is better than the native menu or nothing.
static const int64_t isoformMenuBarShowDeadlineMs = 400;

// The page's body pads the panel for its shadow: 18 pt at the sides, none on
// top, 34 pt below. The window is 360 + 2 * 18 wide.
static const CGFloat isoformMenuBarPageWidth = 396, isoformMenuBarPanelInset = 18;

static NSAppearance *isoformMenuBarAppearance(NSString *theme) {
    return [NSAppearance appearanceNamed:[theme isEqual:@"onedark"] ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua];
}

@implementation IsoformMenuBarPopover
- (void)applyIcon {
    NSDictionary *icon = self.model[@"icon"];
    BOOL dot = [icon[@"dot"] boolValue] && !self.panel.isVisible;
    // A trouble line is up: a small amber dot, which a template image can't
    // carry (computer-trouble v1.0.0 rule 4).
    BOOL trouble = [icon[@"trouble"] boolValue];
    CGFloat alpha = icon ? [icon[@"opacity"] doubleValue] : 1;
    // Keep native template rendering, including dark/high-contrast menu bars.
    NSImage *mark = self.mark;
    BOOL wide = dot || trouble;
    NSImage *image = [NSImage imageWithSize:NSMakeSize(wide ? 25 : 18,18) flipped:NO drawingHandler:^BOOL(NSRect rect) {
        [mark drawInRect:NSMakeRect(0,0,18,18) fromRect:NSZeroRect operation:NSCompositingOperationSourceOver fraction:alpha];
        if (trouble) {
            // Not a template: tint the mark with the menu bar's own text
            // color, then the dot in the design system's color-warning.
            [[NSColor labelColor] set];
            NSRectFillUsingOperation(NSMakeRect(0,0,18,18), NSCompositingOperationSourceAtop);
            BOOL darkBar = [[NSAppearance.currentDrawingAppearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]] isEqual:NSAppearanceNameDarkAqua];
            NSColor *amber = darkBar ? [NSColor colorWithSRGBRed:0xe8/255.0 green:0xa0/255.0 blue:0x66/255.0 alpha:1]
                                     : [NSColor colorWithSRGBRed:0x9b/255.0 green:0x4f/255.0 blue:0x1d/255.0 alpha:1];
            [amber setFill];
            [[NSBezierPath bezierPathWithOvalInRect:NSMakeRect(19,11,6,6)] fill];
        } else if (dot) { [[NSColor blackColor] setFill]; [[NSBezierPath bezierPathWithOvalInRect:NSMakeRect(19,11,6,6)] fill]; }
        return YES;
    }];
    image.template = !trouble;
    self.item.button.image = image;
    self.item.button.toolTip = self.model[@"state"];
    // "Isoform, observing, 3 waiting on you".
    if ([self.model[@"label"] isKindOfClass:NSString.class]) self.item.button.accessibilityLabel = self.model[@"label"];
    [self.item.button highlight:self.opening || self.panel.isVisible];
}
- (NSMenu *)buildMenu:(NSArray *)rows {
    NSMenu *menu = [[[NSMenu alloc] init] autorelease];
    menu.autoenablesItems = NO;
    for (NSDictionary *row in rows) {
        if ([row[@"separator"] boolValue]) { [menu addItem:NSMenuItem.separatorItem]; continue; }
        NSMenuItem *item = [[[NSMenuItem alloc] initWithTitle:row[@"title"] ?: @"" action:@selector(command:) keyEquivalent:row[@"key"] ?: @""] autorelease];
        item.target = self;
        item.representedObject = row[@"id"];
        NSEventModifierFlags flags = 0;
        for (NSString *modifier in row[@"modifiers"]) {
            if ([modifier isEqual:@"command"]) flags |= NSEventModifierFlagCommand;
            if ([modifier isEqual:@"shift"]) flags |= NSEventModifierFlagShift;
            if ([modifier isEqual:@"control"]) flags |= NSEventModifierFlagControl;
            if ([modifier isEqual:@"option"]) flags |= NSEventModifierFlagOption;
        }
        item.keyEquivalentModifierMask = flags;
        if (row[@"children"]) item.submenu = [self buildMenu:row[@"children"]];
        [menu addItem:item];
    }
    return menu;
}
- (void)command:(NSMenuItem *)sender {
    [self closeWithReason:@"menu command"];
    goMenuBarCommand((char *)[(NSString *)sender.representedObject UTF8String]);
}
- (BOOL)shown { return self.panel.isVisible; }
// Load the page once; it stays loaded between opens.
- (void)ensurePage {
    if (self.web || !self.pageHTML || stopped) return;
    WKWebViewConfiguration *config = [[[WKWebViewConfiguration alloc] init] autorelease];
    [config.userContentController addScriptMessageHandler:self name:@"menubar"];
    config.websiteDataStore = WKWebsiteDataStore.nonPersistentDataStore;
    WKWebView *web = [[[WKWebView alloc] initWithFrame:NSMakeRect(0,0,360,420) configuration:config] autorelease];
    IsoformMenuBarPanel *panel = [[[IsoformMenuBarPanel alloc] initWithContentRect:NSMakeRect(0,0,isoformMenuBarPageWidth,420)
        styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel backing:NSBackingStoreBuffered defer:YES] autorelease];
    if (!web || !panel) {
        [config.userContentController removeScriptMessageHandlerForName:@"menubar"];
        logPopoverStep(@"page creation failed");
        return;
    }
    web.navigationDelegate = self;
    // Transparent around the page's own panel and shadow.
    [web setValue:@NO forKey:@"drawsBackground"];
    if (@available(macOS 12.0, *)) web.underPageBackgroundColor = NSColor.clearColor;
    panel.opaque = NO;
    panel.backgroundColor = NSColor.clearColor;
    panel.hasShadow = NO;
    panel.level = NSPopUpMenuWindowLevel;
    panel.hidesOnDeactivate = NO;
    panel.releasedWhenClosed = NO;
    panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorTransient | NSWindowCollectionBehaviorIgnoresCycle;
    panel.delegate = self;
    panel.contentView = web;
    self.web = web;
    self.panel = panel;
    self.pageReady = NO;
    logPopoverStep(@"page loading");
    if (![web loadHTMLString:self.pageHTML baseURL:nil]) {
        logPopoverStep(@"page navigation could not start");
        [self dropPage];
    }
}
- (void)dropPage {
    self.web.navigationDelegate = nil;
    [self.web.configuration.userContentController removeScriptMessageHandlerForName:@"menubar"];
    [self.web stopLoading];
    self.web = nil;
    self.panel.delegate = nil;
    [self.panel orderOut:nil];
    self.panel = nil;
    self.pageReady = NO;
}
- (void)pushModel {
    if (!self.pageReady || !pendingModel) return;
    [self.web evaluateJavaScript:[NSString stringWithFormat:@"window.updateMenuBar(%@)", pendingModel] completionHandler:nil];
    if (pendingPlayback) [self.web evaluateJavaScript:[NSString stringWithFormat:@"window.updatePlayback(%@)", pendingPlayback] completionHandler:nil];
}
// Empty the page now, so nothing read before a privacy or source change
// stays on screen or in the hidden page.
- (void)withdrawPage {
    if (self.pageReady) [self.web evaluateJavaScript:@"window.withdrawMenuBar && window.withdrawMenuBar()" completionHandler:nil];
}
- (void)placePanel {
    NSButton *button = self.item.button;
    NSRect icon = [button.window convertRectToScreen:[button convertRect:button.bounds toView:nil]];
    NSScreen *screen = button.window.screen ?: NSScreen.mainScreen;
    NSRect visible = screen.visibleFrame;
    CGFloat height = self.pageHeight > 0 ? self.pageHeight : 420;
    // Centre the 360 pt panel on the icon, at least 8 pt from the edges.
    CGFloat panelX = NSMidX(icon) - (isoformMenuBarPageWidth - 2 * isoformMenuBarPanelInset) / 2;
    panelX = MAX(NSMinX(visible) + 8, MIN(panelX, NSMaxX(visible) - 8 - (isoformMenuBarPageWidth - 2 * isoformMenuBarPanelInset)));
    CGFloat top = NSMinY(icon) - 5;
    [self.panel setFrame:NSMakeRect(panelX - isoformMenuBarPanelInset, top - height, isoformMenuBarPageWidth, height) display:YES];
}
- (void)showNow:(NSString *)reason {
    if (!self.opening) return;
    self.opening = NO;
    if (!self.panel) {
        logPopoverStep([NSString stringWithFormat:@"show skipped reason=%@ no page", reason]);
        [self applyIcon];
        return;
    }
    self.panel.appearance = isoformMenuBarAppearance(self.model[@"theme"]);
    [self placePanel];
    [self.panel makeKeyAndOrderFront:nil];
    [self.panel makeFirstResponder:self.web];
    [self watchOutsideClicks];
    logPopoverStep([NSString stringWithFormat:@"show reason=%@ ready=%d", reason, self.pageReady]);
    // Clear the dot locally too, including a close before Go's next refresh.
    if (self.model[@"icon"]) {
        NSMutableDictionary *model = [self.model mutableCopy];
        NSMutableDictionary *icon = [model[@"icon"] mutableCopy];
        icon[@"dot"] = @NO;
        model[@"icon"] = icon;
        self.model = model;
        [icon release]; [model release];
    }
    [self applyIcon];
    logPopoverStep([NSString stringWithFormat:@"popoverDidShow frame=%@ visible=%d appearance=%@",
        NSStringFromRect(self.panel.frame), self.panel.isVisible, self.panel.appearance.name]);
    goMenuBarPopoverVisibility(YES);
}
// A click anywhere else closes it: other apps (global) and our own windows
// (local), except the icon, whose own click toggles.
- (void)watchOutsideClicks {
    [self stopWatching];
    NSEventMask mask = NSEventMaskLeftMouseDown | NSEventMaskRightMouseDown | NSEventMaskOtherMouseDown;
    self.globalMonitor = [NSEvent addGlobalMonitorForEventsMatchingMask:mask handler:^(NSEvent *event) {
        [host closeWithReason:@"click outside"];
    }];
    self.localMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:mask handler:^NSEvent *(NSEvent *event) {
        if (host && event.window != host.panel && event.window != host.item.button.window) [host closeWithReason:@"click outside"];
        return event;
    }];
}
- (void)stopWatching {
    if (self.globalMonitor) [NSEvent removeMonitor:self.globalMonitor];
    if (self.localMonitor) [NSEvent removeMonitor:self.localMonitor];
    self.globalMonitor = nil;
    self.localMonitor = nil;
}
- (void)windowDidResignKey:(NSNotification *)notification {
    // Another window took the keyboard: Isoform came forward, or the user
    // went elsewhere.
    if (notification.object == self.panel) [self closeWithReason:@"resign key"];
}
- (void)click:(id)sender {
    NSEvent *event = NSApp.currentEvent;
    BOOL rightClick = event.type == NSEventTypeRightMouseUp || (event.modifierFlags & NSEventModifierFlagControl);
    logPopoverStep([NSString stringWithFormat:@"click event=%ld button=%@ opening=%d shown=%d ready=%d",
        (long)event.type, rightClick ? @"right" : @"left", self.opening, self.shown, self.pageReady]);
    if (rightClick) { [self showMenu]; return; }
    if (self.opening || self.shown) { [self closeWithReason:@"user cancel"]; return; }
    self.opening = YES;
    [self applyIcon];
    if (!self.web) [self ensurePage];
    if (self.pageReady) { [self pushModel]; [self showNow:@"click"]; return; }
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, isoformMenuBarShowDeadlineMs * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{
        // Right after launch the page may not be handed over yet; it shows
        // on "ready" then. Give up only if it never comes.
        if (host == self && self.opening && self.panel) [self showNow:@"deadline"];
    });
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
        if (host == self && self.opening) [self showNow:@"late deadline"];
    });
}
// Right-click and Control-click: the short native menu.
- (void)showMenu {
    NSMenu *trackingMenu = [self.menu retain];
    [self closeWithReason:@"right click"];
    logPopoverStep(@"native menu reason=right click");
    [self.item.button highlight:YES];
    [trackingMenu popUpMenuPositioningItem:nil atLocation:NSMakePoint(0, self.item.button.bounds.size.height) inView:self.item.button];
    [trackingMenu release];
    [self.item.button highlight:NO];
}
- (void)closeWithReason:(NSString *)reason {
    BOOL wasShown = self.shown;
    logPopoverStep([NSString stringWithFormat:@"close reason=%@ opening=%d shown=%d", reason, self.opening, wasShown]);
    self.opening = NO;
    [self stopWatching];
    if (wasShown) {
        [self.panel orderOut:nil];
        goMenuBarPopoverVisibility(NO);
    }
    [self applyIcon];
}
// The page broke: reload it, and keep any open or opening popover. Never
// swap in the native menu.
- (void)pageFailed:(NSString *)reason {
    logPopoverStep([NSString stringWithFormat:@"page failed reason=%@ reloads=%lu", reason, (unsigned long)self.reloads]);
    BOOL wasShown = self.shown;
    if (wasShown) [self closeWithReason:reason];
    [self dropPage];
    if (self.reloads >= 3) return;
    self.reloads++;
    [self ensurePage];
    if (wasShown) {
        self.opening = YES;
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, isoformMenuBarShowDeadlineMs * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{
            if (host == self && self.opening) [self showNow:@"reload deadline"];
        });
    }
}
- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView {
    if (webView == self.web) [self pageFailed:@"content process terminated"];
}
- (void)webView:(WKWebView *)web didFailNavigation:(WKNavigation *)nav withError:(NSError *)error {
    if (web == self.web) [self pageFailed:[NSString stringWithFormat:@"navigation failed %@ %ld", error.domain, (long)error.code]];
}
- (void)webView:(WKWebView *)web didFailProvisionalNavigation:(WKNavigation *)nav withError:(NSError *)error {
    if (web == self.web) [self pageFailed:[NSString stringWithFormat:@"provisional navigation failed %@ %ld", error.domain, (long)error.code]];
}
- (void)webView:(WKWebView *)web decidePolicyForNavigationAction:(WKNavigationAction *)action decisionHandler:(void (^)(WKNavigationActionPolicy))handler {
    // The embedded page has no remote resources or navigable links.
    handler([action.request.URL.scheme isEqual:@"about"] ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
}
- (void)userContentController:(WKUserContentController *)uc didReceiveScriptMessage:(WKScriptMessage *)message {
    if (message.webView != self.web || ![message.body isKindOfClass:NSDictionary.class]) return;
    NSString *command = message.body[@"command"];
    if (![command isKindOfClass:NSString.class] || command.length > 256) return;
    if ([command isEqual:@"resize"] || [command isEqual:@"ready"]) {
        NSNumber *height = message.body[@"height"];
        if ([height isKindOfClass:NSNumber.class]) {
            self.pageHeight = MIN(540, MAX(200, height.doubleValue));
            if (self.shown) [self placePanel];
        }
        if ([command isEqual:@"ready"]) {
            self.pageReady = YES;
            self.reloads = 0;
            logPopoverStep([NSString stringWithFormat:@"page ready height=%.0f", self.pageHeight]);
            [self pushModel];
            if (self.opening) [self showNow:@"page ready"];
        }
    } else if ([command isEqual:@"close"]) [self closeWithReason:@"web close"];
    else if (self.shown) {
        // Pause, Resume, playing the brief and Make my brief now keep it
        // open; anything that brings Isoform forward closes it.
        BOOL stays = [command isEqual:@"pause15"] || [command isEqual:@"resume"] || [command hasPrefix:@"play:"] || [command isEqual:@"makeHere"];
        if (!stays) [self closeWithReason:@"web action"];
        goMenuBarCommand((char *)command.UTF8String);
    }
}
- (void)dealloc {
    [self stopWatching];
    [self dropPage]; [_item release]; [_menu release]; [_legacyMenu release]; [_mark release]; [_model release]; [_pageHTML release];
    [super dealloc];
}
@end

int isoformMenuBarAttach(void *pointer) {
    NSStatusItem *item = (NSStatusItem *)pointer;
    if (!item || !item.button || !item.menu) return 0;
    stopped = NO;
    host = [[IsoformMenuBarPopover alloc] init];
    if (!host) return 0;
    host.item = (NSStatusItem *)pointer;
    host.menu = host.item.menu;
    // Existing title/status updaters keep pointers into this original menu.
    host.legacyMenu = host.item.menu;
    host.mark = host.item.button.image;
    host.item.menu = nil;
    host.item.button.target = host;
    host.item.button.action = @selector(click:);
    [host.item.button sendActionOn:NSEventMaskLeftMouseUp | NSEventMaskRightMouseUp];
    if (host.item.button.target != host || host.item.button.action != @selector(click:)) {
        host.item.menu = host.legacyMenu;
        host.item.button.target = nil;
        host.item.button.action = nil;
        [host release]; host = nil;
        return 0;
    }
    if (pendingModel) isoformMenuBarUpdate(pendingModel.UTF8String);
    return 1;
}
// The page HTML (no personal content: the loading model only) and the model
// it starts from, so the first click after launch already has our popover,
// the right theme and the right menu.
void isoformMenuBarPreload(const char *html, const char *json) {
    NSString *page = [[NSString alloc] initWithUTF8String:html];
    if (json) isoformMenuBarUpdate(json);
    dispatch_async(dispatch_get_main_queue(), ^{
        if (host && !stopped) {
            host.pageHTML = page;
            [host ensurePage];
        }
        [page release];
    });
}
void isoformMenuBarDetach(void) {
    // Called on the same main-queue teardown as the status item.
    stopped = YES;
    [host closeWithReason:@"detach"];
    [host dropPage];
    host.item.button.target = nil;
    [host release]; host = nil;
    [pendingModel release]; pendingModel = nil;
    [pendingPlayback release]; pendingPlayback = nil;
}
void isoformMenuBarInvalidate(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [pendingModel release]; pendingModel = nil;
        [pendingPlayback release]; pendingPlayback = nil;
        if (!host) {
            logPopoverStep(@"invalidate state=idle action=clear");
            return;
        }
        // Keep the theme and menu (no personal content); drop the rest.
        NSMutableDictionary *kept = [NSMutableDictionary dictionary];
        for (NSString *key in @[@"theme", @"menu", @"icon", @"language"]) if (host.model[key]) kept[key] = host.model[key];
        host.model = kept;
        logPopoverStep([NSString stringWithFormat:@"invalidate shown=%d opening=%d action=withdraw", host.shown, host.opening]);
        [host withdrawPage];
    });
}
void isoformMenuBarUpdate(const char *json) {
    NSString *copy = [[NSString alloc] initWithUTF8String:json];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (!stopped) {
            NSData *data = [copy dataUsingEncoding:NSUTF8StringEncoding];
            NSDictionary *model = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
            if (model) {
                [pendingModel release]; pendingModel = [copy copy];
            }
            if (host && model) {
                host.model = model;
                if (host.panel) host.panel.appearance = isoformMenuBarAppearance(model[@"theme"]);
                host.menu = [host buildMenu:model[@"menu"]];
                [host applyIcon];
                [host pushModel];
            }
        }
        [copy release];
    });
}
// Today's player moved: the popover shows the same state and position.
void isoformMenuBarPlayback(const char *json) {
    NSString *copy = [[NSString alloc] initWithUTF8String:json];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (!stopped) {
            [pendingPlayback release]; pendingPlayback = [copy copy];
            if (host.pageReady) [host.web evaluateJavaScript:[NSString stringWithFormat:@"window.updatePlayback(%@)", copy] completionHandler:nil];
        }
        [copy release];
    });
}
void isoformMenuBarError(const char *message) {
    NSString *copy = [[NSString alloc] initWithUTF8String:message];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (host) {
            NSAlert *alert = [[[NSAlert alloc] init] autorelease];
            alert.messageText = copy;
            [alert runModal];
        }
        [copy release];
    });
}
