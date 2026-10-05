//go:build darwin

// macOS 菜单栏托盘的 Objective-C 实现（Go 侧入口见 tray_darwin.go）。
//
// 注意：cgo 编译的 Objective-C 默认是 MRC（手动引用计数，没开 -fobjc-arc），
// statusItemWithLength: 返回 autoreleased 对象，必须手动 retain——否则
// autorelease pool 一清，状态项连同菜单栏窗口一起被回收（表现为托盘闪一下就没）。
#import "_cgo_export.h"
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

@interface TMTarget : NSObject
- (void)onShow:(id)sender;
- (void)onSync:(id)sender;
- (void)onPanel:(id)sender;
- (void)onQuit:(id)sender;
@end

@implementation TMTarget
// 关窗口走的是 Wails 的 [NSApp hide]（应用级隐藏），只 makeKeyAndOrderFront 唤不回窗口，
// 先把整个应用 unhide 再让 Wails 显示主窗口
- (void)onShow:(id)s {
	[[NSApplication sharedApplication] unhide:nil];
	trayGoShow();
}
- (void)onSync:(id)s  { trayGoSync(); }
- (void)onPanel:(id)s { trayGoPanel(); }
- (void)onQuit:(id)s  { trayGoQuit(); }
@end

static TMTarget *target = nil;
static NSStatusItem *statusItem = nil;

// windowShouldMiniaturize: 返回 NO 阻止真正最小化，改成把窗口藏起来
// （托盘「打开 Token Monitor」可唤回），程序坞里不会出现迷你窗口
static BOOL hookShouldMiniaturize(id self, SEL _cmd, id window) {
	trayGoHide();
	return NO;
}

// 兜底：若 Wails 将来自己实现了 windowShouldMiniaturize:（class_addMethod 失败），
// 直接替换 NSWindow miniaturize: 的实现，同样不真正 miniaturize
static void hookMiniaturize(id self, SEL _cmd, id sender) {
	trayGoHide();
}

// 窗口隐藏后点程序坞图标：唤回主窗口
static BOOL hookReopen(id self, SEL _cmd, id app, BOOL visible) {
	trayGoShow();
	return YES;
}

static void trayInstall(NSData *iconData) {
	// 最小化 → 隐藏到托盘。给 Wails 的窗口代理（类名 WindowDelegate）补方法
	Class wd = objc_getClass("WindowDelegate");
	if (wd == nil || !class_addMethod(wd, @selector(windowShouldMiniaturize:),
	                                  (IMP)hookShouldMiniaturize, "B@:@")) {
		Method m = class_getInstanceMethod([NSWindow class], @selector(miniaturize:));
		method_setImplementation(m, (IMP)hookMiniaturize);
	}

	// 程序坞图标点击唤回。给 Wails 的应用代理（类名 AppDelegate）补方法
	Class ad = objc_getClass("AppDelegate");
	if (ad != nil) {
		class_addMethod(ad, @selector(applicationShouldHandleReopen:hasVisibleWindows:),
		                (IMP)hookReopen, "B@:@B");
	}

	target = [[TMTarget alloc] init];
	statusItem = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength]
	    retain]; // MRC：static 变量不会自动持有，见文件头注释
	NSImage *img = iconData ? [[NSImage alloc] initWithData:iconData] : nil;
	if (img) {
		img.size = NSMakeSize(16, 16);
		statusItem.button.image = img;
		[img release];
	} else {
		statusItem.button.title = @"TM";
	}
	statusItem.button.toolTip = @"Token Monitor — 后台采集中";

	NSMenu *menu = [[NSMenu alloc] init];
	menu.autoenablesItems = NO;
	[menu addItemWithTitle:@"打开 Token Monitor" action:@selector(onShow:) keyEquivalent:@""];
	[menu addItemWithTitle:@"立即同步" action:@selector(onSync:) keyEquivalent:@""];
	[menu addItemWithTitle:@"打开网页面板" action:@selector(onPanel:) keyEquivalent:@""];
	[menu addItem:[NSMenuItem separatorItem]];
	[menu addItemWithTitle:@"退出" action:@selector(onQuit:) keyEquivalent:@""];
	for (NSMenuItem *it in menu.itemArray) {
		if (it.action != nil) {
			it.target = target;
			[it setEnabled:YES];
		}
	}
	statusItem.menu = menu;
	[menu release];
}

// 可能在任意 goroutine 调用：UI 必须回主线程，Wails 正占着主线程跑事件循环，
// dispatch_async 到主队列会在循环空闲时执行。
void traySetup(char *iconBytes, int iconLen) {
	// initWithBytes 拷贝数据且按 MRC 规则 +1，块捕获后随块释放即可
	NSData *iconData = nil;
	if (iconBytes != nil && iconLen > 0) {
		iconData = [[NSData alloc] initWithBytes:iconBytes length:iconLen];
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		trayInstall(iconData);
		[iconData release];
	});
}

void trayTeardown() {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (statusItem != nil) {
			[[NSStatusBar systemStatusBar] removeStatusItem:statusItem];
			[statusItem release];
			statusItem = nil;
		}
	});
}
