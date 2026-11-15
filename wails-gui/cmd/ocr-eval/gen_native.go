package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"wails-gui/internal/ocr/eval"
)

type nativeSample struct {
	id       string
	lang     string
	category string
	html     string
	ground   string
	width    int
	height   int
}

func nativeSamples() []nativeSample {
	return []nativeSample{
		// Chinese - chat app style
		{
			id: "zh_chat_light", lang: "zh", category: "chat",
			width: 800, height: 400,
			ground: "项目进度更新\n请查看最新的设计稿并提供反馈\n好的，我下午会看一下\n另外明天的会议改到三点了",
			html: `<div style="font-family:-apple-system,PingFang SC;background:#fff;padding:24px;width:800px;height:400px">
				<div style="background:#f3f3f3;border-radius:12px;padding:12px 16px;margin:8px 0;max-width:70%;font-size:15px;color:#333">项目进度更新</div>
				<div style="background:#f3f3f3;border-radius:12px;padding:12px 16px;margin:8px 0;max-width:70%;font-size:15px;color:#333">请查看最新的设计稿并提供反馈</div>
				<div style="background:#95ec69;border-radius:12px;padding:12px 16px;margin:8px 0 8px auto;max-width:70%;font-size:15px;color:#333;text-align:right">好的，我下午会看一下</div>
				<div style="background:#f3f3f3;border-radius:12px;padding:12px 16px;margin:8px 0;max-width:70%;font-size:15px;color:#333">另外明天的会议改到三点了</div>
			</div>`,
		},
		// Chinese - small text, IDE comment style
		{
			id: "zh_ide_comment", lang: "zh", category: "ide",
			width: 900, height: 300,
			ground: "// 初始化数据库连接池\nfunc initDB(dsn string) (*sql.DB, error) {\n    // 设置最大连接数和空闲连接数\n    db.SetMaxOpenConns(25)\n    db.SetMaxIdleConns(5)",
			html: `<div style="font-family:Menlo,Monaco,monospace;background:#1e1e1e;padding:16px;width:900px;height:300px;font-size:13px;line-height:1.6">
				<div><span style="color:#6a9955">// 初始化数据库连接池</span></div>
				<div><span style="color:#569cd6">func</span> <span style="color:#dcdcaa">initDB</span><span style="color:#d4d4d4">(</span><span style="color:#9cdcfe">dsn</span> <span style="color:#4ec9b0">string</span><span style="color:#d4d4d4">) (*</span><span style="color:#4ec9b0">sql.DB</span><span style="color:#d4d4d4">,</span> <span style="color:#4ec9b0">error</span><span style="color:#d4d4d4">) {</span></div>
				<div><span style="color:#6a9955">    // 设置最大连接数和空闲连接数</span></div>
				<div><span style="color:#d4d4d4">    db.</span><span style="color:#dcdcaa">SetMaxOpenConns</span><span style="color:#d4d4d4">(25)</span></div>
				<div><span style="color:#d4d4d4">    db.</span><span style="color:#dcdcaa">SetMaxIdleConns</span><span style="color:#d4d4d4">(5)</span></div>
			</div>`,
		},
		// Chinese - small font browser content
		{
			id: "zh_browser_article", lang: "zh", category: "browser",
			width: 800, height: 400,
			ground: "深度学习在自然语言处理中的应用\n近年来，大型语言模型的发展推动了自然语言处理技术的革新。\n从BERT到GPT系列，预训练模型在文本理解和生成任务上\n取得了显著的突破。本文将探讨这些技术在实际产品中的应用。",
			html: `<div style="font-family:-apple-system,PingFang SC;background:#fff;padding:32px;width:800px;height:400px">
				<h2 style="font-size:22px;font-weight:600;color:#1a1a1a;margin:0 0 16px 0">深度学习在自然语言处理中的应用</h2>
				<p style="font-size:15px;color:#333;line-height:1.8;margin:0">近年来，大型语言模型的发展推动了自然语言处理技术的革新。从BERT到GPT系列，预训练模型在文本理解和生成任务上取得了显著的突破。本文将探讨这些技术在实际产品中的应用。</p>
			</div>`,
		},
		// English - IDE code
		{
			id: "en_ide_code", lang: "en", category: "ide",
			width: 900, height: 300,
			ground: "func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {\n    ctx := r.Context()\n    userID := middleware.GetUserID(ctx)\n    if userID == \"\" {\n        http.Error(w, \"unauthorized\", http.StatusUnauthorized)",
			html: `<div style="font-family:Menlo,Monaco,monospace;background:#1e1e1e;padding:16px;width:900px;height:300px;font-size:13px;line-height:1.6">
				<div><span style="color:#569cd6">func</span> <span style="color:#d4d4d4">(</span><span style="color:#9cdcfe">s</span> <span style="color:#d4d4d4">*</span><span style="color:#4ec9b0">Server</span><span style="color:#d4d4d4">)</span> <span style="color:#dcdcaa">handleRequest</span><span style="color:#d4d4d4">(</span><span style="color:#9cdcfe">w</span> <span style="color:#4ec9b0">http.ResponseWriter</span><span style="color:#d4d4d4">,</span> <span style="color:#9cdcfe">r</span> <span style="color:#d4d4d4">*</span><span style="color:#4ec9b0">http.Request</span><span style="color:#d4d4d4">) {</span></div>
				<div><span style="color:#d4d4d4">    ctx := r.</span><span style="color:#dcdcaa">Context</span><span style="color:#d4d4d4">()</span></div>
				<div><span style="color:#d4d4d4">    userID := middleware.</span><span style="color:#dcdcaa">GetUserID</span><span style="color:#d4d4d4">(ctx)</span></div>
				<div><span style="color:#d4d4d4">    </span><span style="color:#c586c0">if</span><span style="color:#d4d4d4"> userID == </span><span style="color:#ce9178">""</span><span style="color:#d4d4d4"> {</span></div>
				<div><span style="color:#d4d4d4">        http.</span><span style="color:#dcdcaa">Error</span><span style="color:#d4d4d4">(w, </span><span style="color:#ce9178">"unauthorized"</span><span style="color:#d4d4d4">, http.StatusUnauthorized)</span></div>
			</div>`,
		},
		// English - light browser content
		{
			id: "en_browser_docs", lang: "en", category: "browser",
			width: 800, height: 350,
			ground: "Getting Started with WebSocket\nWebSocket provides full-duplex communication channels over a single TCP connection.\nUnlike HTTP, WebSocket allows the server to push data to the client without polling.\nThis makes it ideal for real-time applications like chat and live updates.",
			html: `<div style="font-family:-apple-system,system-ui;background:#fff;padding:32px;width:800px;height:350px">
				<h2 style="font-size:22px;font-weight:600;color:#1a1a1a;margin:0 0 16px 0">Getting Started with WebSocket</h2>
				<p style="font-size:15px;color:#333;line-height:1.8;margin:0 0 12px 0">WebSocket provides full-duplex communication channels over a single TCP connection.</p>
				<p style="font-size:15px;color:#333;line-height:1.8;margin:0 0 12px 0">Unlike HTTP, WebSocket allows the server to push data to the client without polling.</p>
				<p style="font-size:15px;color:#333;line-height:1.8;margin:0">This makes it ideal for real-time applications like chat and live updates.</p>
			</div>`,
		},
		// Mixed - terminal output with Chinese error messages
		{
			id: "mixed_terminal", lang: "zh-en", category: "terminal",
			width: 900, height: 300,
			ground: "$ go test ./...\n--- FAIL: TestDatabaseConnection (0.52s)\n    db_test.go:42: 数据库连接失败: dial tcp 127.0.0.1:5432: connection refused\n    db_test.go:43: 请确认 PostgreSQL 服务已启动\nFAIL\texit status 1",
			html: `<div style="font-family:Menlo,Monaco,monospace;background:#1e1e1e;padding:16px;width:900px;height:300px;font-size:13px;line-height:1.6">
				<div style="color:#b5bd68">$ go test ./...</div>
				<div style="color:#cc6666">--- FAIL: TestDatabaseConnection (0.52s)</div>
				<div style="color:#d4d4d4">    db_test.go:42: 数据库连接失败: dial tcp 127.0.0.1:5432: connection refused</div>
				<div style="color:#d4d4d4">    db_test.go:43: 请确认 PostgreSQL 服务已启动</div>
				<div style="color:#cc6666">FAIL</div>
				<div style="color:#d4d4d4">exit status 1</div>
			</div>`,
		},
		// Mixed - Feishu/Lark style with names and Chinese
		{
			id: "mixed_feishu_chat", lang: "zh-en", category: "chat",
			width: 800, height: 400,
			ground: "Gabriel\n这个PR的CI跑过了吗\nJiarui\n跑过了，但是有两个flaky test\nGabriel\nwhich ones? can you paste the log\nJiarui\nTestWebSocket和TestSSE，我把log贴到issue里了",
			html: `<div style="font-family:-apple-system,PingFang SC;background:#fff;padding:24px;width:800px;height:400px">
				<div style="margin:8px 0"><span style="font-size:12px;color:#999">Gabriel</span><div style="background:#f3f3f3;border-radius:12px;padding:10px 14px;margin:4px 0;max-width:70%;font-size:14px;color:#333">这个PR的CI跑过了吗</div></div>
				<div style="margin:8px 0"><span style="font-size:12px;color:#999">Jiarui</span><div style="background:#f3f3f3;border-radius:12px;padding:10px 14px;margin:4px 0;max-width:70%;font-size:14px;color:#333">跑过了，但是有两个flaky test</div></div>
				<div style="margin:8px 0"><span style="font-size:12px;color:#999">Gabriel</span><div style="background:#f3f3f3;border-radius:12px;padding:10px 14px;margin:4px 0;max-width:70%;font-size:14px;color:#333">which ones? can you paste the log</div></div>
				<div style="margin:8px 0"><span style="font-size:12px;color:#999">Jiarui</span><div style="background:#f3f3f3;border-radius:12px;padding:10px 14px;margin:4px 0;max-width:70%;font-size:14px;color:#333">TestWebSocket和TestSSE，我把log贴到issue里了</div></div>
			</div>`,
		},
		// Chinese - small font in dark theme (worst case scenario)
		{
			id: "zh_small_dark_ide", lang: "zh", category: "ide",
			width: 900, height: 250,
			ground: "// 处理用户认证和权限校验\n// 如果token过期则刷新，失败则返回401\nfunc (m *authMiddleware) Verify(next http.Handler) http.Handler {",
			html: `<div style="font-family:Menlo,Monaco,monospace;background:#282c34;padding:16px;width:900px;height:250px;font-size:12px;line-height:1.5">
				<div><span style="color:#7f848e">// 处理用户认证和权限校验</span></div>
				<div><span style="color:#7f848e">// 如果token过期则刷新，失败则返回401</span></div>
				<div><span style="color:#c678dd">func</span> <span style="color:#abb2bf">(</span><span style="color:#e06c75">m</span> <span style="color:#abb2bf">*</span><span style="color:#e5c07b">authMiddleware</span><span style="color:#abb2bf">)</span> <span style="color:#61afef">Verify</span><span style="color:#abb2bf">(</span><span style="color:#e06c75">next</span> <span style="color:#e5c07b">http.Handler</span><span style="color:#abb2bf">)</span> <span style="color:#e5c07b">http.Handler</span> <span style="color:#abb2bf">{</span></div>
			</div>`,
		},
	}
}

func generateNativeDataset(outDir string) (*eval.Dataset, error) {
	imgDir := filepath.Join(outDir, "images")
	if err := os.MkdirAll(imgDir, 0755); err != nil {
		return nil, err
	}

	ds := &eval.Dataset{
		Name:        "ocr-eval-native",
		Description: "Native macOS-rendered test images simulating real app screenshots",
		Version:     "1.0",
	}

	for _, ns := range nativeSamples() {
		imgPath := filepath.Join(imgDir, ns.id+".jpg")
		if err := renderHTMLToJPEG(ns.html, imgPath, ns.width, ns.height); err != nil {
			return nil, fmt.Errorf("render %s: %w", ns.id, err)
		}

		ds.Samples = append(ds.Samples, eval.Sample{
			ID:          ns.id,
			ImagePath:   imgPath,
			Language:    ns.lang,
			Category:    ns.category,
			GroundTruth: ns.ground,
		})
	}

	return ds, nil
}

// renderHTMLToJPEG uses macOS WebKit (via a Swift helper script) to render
// HTML to a PNG, then converts to JPEG. This produces text rendered with
// CoreText/subpixel antialiasing identical to real screen captures.
func renderHTMLToJPEG(html, outPath string, width, height int) error {
	htmlFile := outPath + ".html"
	pngFile := outPath + ".png"

	if err := os.WriteFile(htmlFile, []byte(wrapHTML(html, width, height)), 0644); err != nil {
		return err
	}
	defer os.Remove(htmlFile)

	absHTML, _ := filepath.Abs(htmlFile)

	// Use macOS screencapture of a WebKit view via a small Swift script.
	// Alternative: use /usr/bin/qlmanage or wkwebview snapshot.
	// Simplest cross-version approach: use Safari's Web Archive export
	// via /usr/sbin/screencapture, or cliclick. But the most portable
	// way is to shell out to a Swift one-liner.
	script := fmt.Sprintf(`
import WebKit
import AppKit

let args = CommandLine.arguments
guard args.count == 4 else {
    fputs("usage: render <html-path> <png-path> <width>x<height>\n", stderr)
    exit(1)
}
let htmlPath = args[1]
let pngPath = args[2]
let dims = args[3].split(separator: "x").compactMap { Int($0) }
guard dims.count == 2 else { exit(1) }
let w = dims[0], h = dims[1]

let app = NSApplication.shared
let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: w, height: h),
                      styleMask: [], backing: .buffered, defer: false)
let webView = WKWebView(frame: NSRect(x: 0, y: 0, width: w, height: h))
window.contentView = webView

let url = URL(fileURLWithPath: htmlPath)
webView.loadFileURL(url, allowingReadAccessTo: url.deletingLastPathComponent())

DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
    let config = WKSnapshotConfiguration()
    config.rect = NSRect(x: 0, y: 0, width: w, height: h)
    webView.takeSnapshot(with: config) { image, error in
        guard let image = image else {
            fputs("snapshot failed: \(error?.localizedDescription ?? "unknown")\n", stderr)
            exit(1)
        }
        guard let tiff = image.tiffRepresentation,
              let bitmap = NSBitmapImageRep(data: tiff),
              let png = bitmap.representation(using: .png, properties: [:]) else {
            fputs("png conversion failed\n", stderr)
            exit(1)
        }
        try! png.write(to: URL(fileURLWithPath: pngPath))
        exit(0)
    }
}

app.run()
`)

	scriptFile := outPath + ".swift"
	if err := os.WriteFile(scriptFile, []byte(script), 0644); err != nil {
		return err
	}
	defer os.Remove(scriptFile)

	cmd := exec.Command("swift", scriptFile, absHTML, pngFile, fmt.Sprintf("%dx%d", width, height))
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("swift render: %w", err)
	}
	defer os.Remove(pngFile)

	// Convert PNG to JPEG using sips (macOS built-in)
	sips := exec.Command("sips", "-s", "format", "jpeg", "-s", "formatOptions", "90", pngFile, "--out", outPath)
	sips.Stderr = os.Stderr
	if err := sips.Run(); err != nil {
		return fmt.Errorf("sips convert: %w", err)
	}

	return nil
}

func wrapHTML(body string, width, height int) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { width: %dpx; height: %dpx; overflow: hidden; -webkit-font-smoothing: antialiased; }
</style>
</head><body>%s</body></html>`, width, height, body)
}
