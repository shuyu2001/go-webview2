package main

import (
	"log"
	"time"

	"github.com/shuyu2001/go-webview2"
	"github.com/shuyu2001/go-webview2/pkg/edge"
)

func main() {
	var host = "https://shuyuz.app/"
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:          true,
		AutoFocus:      true,
		Width:          600,
		Height:         500,
		Host:           host,
		Center:         true,
		DisableResize:  true,
		StartMaximized: false,
		Chromium:       edge.NewChromium(),
	})
	if w == nil {
		log.Fatalln("Failed to load webview.")
	}
	defer w.Destroy()
	w.AddHtmlContentRoute(host+"/index1", "<h1>Index1</h1>")
	w.AddHtmlContentRoute(host+"/index2", "<h1>Index2</h1>")

	// 1. 先在主线程（或直接初始化时）跳转到第一个页面
	w.Navigate(host + "/index1")

	// 2. 启动一个后台 Goroutine 来处理定时任务，避免阻塞主 UI 线程
	go func() {
		// 在后台静静地等待 5 秒
		time.Sleep(time.Second * 5)

		// 3. 时间到了，把安全修改 UI 的任务“派遣”回主线程执行
		w.Dispatch(func() {
			w.Navigate(host + "/index2")
		})
	}()
	w.Run()
}
