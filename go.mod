module github.com/daidr/Ariadne

go 1.27.4

require (
	connectrpc.com/connect v1.21.1
	github.com/antst/go-yjs v0.1.1
	github.com/chromedp/cdproto v0.157.8
	github.com/chromedp/chromedp v0.21.0
	github.com/clipperhouse/uax29/v2 v2.7.0
	github.com/coder/websocket v1.8.15
	github.com/dlclark/regexp2/v2 v2.13.0
	github.com/google/uuid v1.6.0
	github.com/yuin/goldmark v1.8.6
	github.com/yuin/gopher-lua v1.1.2
	golang.org/x/mod v0.42.0
	golang.org/x/net v0.58.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.44.0
	google.golang.org/protobuf v1.36.12
	modernc.org/sqlite v1.62.0
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20260213210345-44df1a37e875 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

tool (
	connectrpc.com/connect/cmd/protoc-gen-connect-go
	google.golang.org/protobuf/cmd/protoc-gen-go
)
