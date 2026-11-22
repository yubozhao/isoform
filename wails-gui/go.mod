module wails-gui

go 1.25.0

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/aymanbagabas/go-pty v0.2.2
	github.com/bwmarrin/discordgo v0.29.0
	github.com/creack/pty v1.1.24
	github.com/energye/systray v1.0.3
	github.com/go-ole/go-ole v1.3.0
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/k2-fsa/sherpa-onnx-go v1.13.0
	github.com/larksuite/oapi-sdk-go/v3 v3.9.5
	github.com/ledongthuc/pdf v0.0.0-20250511090121-5959a4027728
	github.com/mark3labs/mcp-go v0.48.0
	github.com/moutend/go-wca v0.3.0
	github.com/mozillazg/go-unidecode v0.2.0
	github.com/pdfcpu/pdfcpu v0.12.1
	github.com/pkg/errors v0.9.1
	github.com/posthog/posthog-go v1.12.5
	github.com/rivo/uniseg v0.4.7
	github.com/robfig/cron/v3 v3.0.1
	github.com/skratchdot/open-golang v0.0.0-20200116055534-eef842397966
	github.com/slack-go/slack v0.26.0
	github.com/wailsapp/wails/v2 v2.12.0
	github.com/xuri/excelize/v2 v2.10.1
	github.com/yalue/onnxruntime_go v1.26.0
	golang.org/x/crypto v0.50.0
	golang.org/x/image v0.39.0
	golang.org/x/sync v0.20.0
	golang.org/x/sys v0.43.0
	golang.org/x/text v0.36.0
	gopkg.in/yaml.v2 v2.4.0
	modernc.org/sqlite v1.44.3
	yansu-agent v0.0.0
)

replace yansu-agent => ../yansu-agent

require (
	codeberg.org/readeck/go-readability/v2 v2.1.1 // indirect
	git.sr.ht/~jackmordaunt/go-toast/v2 v2.0.3 // indirect
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/araddon/dateparse v0.0.0-20210429162001-6b43995a97de // indirect
	github.com/bep/debounce v1.2.1 // indirect
	github.com/bmatcuk/doublestar/v4 v4.6.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-shiori/dom v0.0.0-20230515143342-73569d674e1c // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/gogs/chardet v0.0.0-20211120154057-b7413eaefb8f // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/hhrutter/lzw v1.0.0 // indirect
	github.com/hhrutter/pkcs7 v0.2.2 // indirect
	github.com/hhrutter/tiff v1.0.3 // indirect
	github.com/jchv/go-winloader v0.0.0-20210711035445-715c2860da7e // indirect
	github.com/k2-fsa/sherpa-onnx-go-linux v1.13.0 // indirect
	github.com/k2-fsa/sherpa-onnx-go-macos v1.13.0 // indirect
	github.com/k2-fsa/sherpa-onnx-go-windows v1.13.0 // indirect
	github.com/labstack/echo/v4 v4.13.3 // indirect
	github.com/labstack/gommon v0.4.2 // indirect
	github.com/leaanthony/go-ansi-parser v1.6.1 // indirect
	github.com/leaanthony/gosod v1.0.4 // indirect
	github.com/leaanthony/slicer v1.6.0 // indirect
	github.com/leaanthony/u v1.1.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/richardlehane/mscfb v1.0.6 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/samber/lo v1.49.1 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/tkrajina/go-reflector v0.5.8 // indirect
	github.com/u-root/u-root v0.11.0 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	github.com/wailsapp/go-webview2 v1.0.22 // indirect
	github.com/wailsapp/mimetype v1.4.1 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.starlark.net v0.0.0-20250127224320-2fb1215d657f // indirect
	golang.org/x/exp v0.0.0-20251023183803-a4bb9ffd2546 // indirect
	golang.org/x/mod v0.34.0
	golang.org/x/net v0.52.0 // indirect
	modernc.org/libc v1.67.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	mvdan.cc/sh/v3 v3.12.0 // indirect
)
