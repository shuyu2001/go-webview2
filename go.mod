module github.com/shuyu2001/go-webview2 // 你的项目名

go 1.16

require (
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1
	golang.org/x/sys v0.0.0-20210218145245-beda7e5e158e
)

// 关键步骤：告诉 Go 把原作者的路径重定向到你的路径
replace github.com/jchv/go-webview2 => github.com/shuyu2001/go-webview2 v0.0.0-20260205173254-56598839c808
