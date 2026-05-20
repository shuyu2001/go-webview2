package main

import (
	"fmt"
	"log"
	"time"

	_ "embed"

	"github.com/shuyu2001/go-webview2"
	"github.com/shuyu2001/go-webview2/pkg/edge"
)

//go:embed test.html
var html string

type Action struct {
	Action string `json:"action"`
	URL    string `json:"url"`
}

func main() {
	var host = "http://app.localhost/"
	var chrome = edge.NewChromium()
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:          true,
		AutoFocus:      true,
		Width:          600,
		Height:         500,
		Host:           host,
		Center:         true,
		DisableResize:  true,
		StartMaximized: false,
		Chromium:       chrome,
	})
	if w == nil {
		log.Fatalln("Failed to load webview.")
	}
	defer w.Destroy()

	chrome.NavigationCompletedCallback = func(sender *edge.ICoreWebView2, args *edge.ICoreWebView2NavigationCompletedEventArgs) {
		chrome.JSONMessageCallback = edge.WrapJSONCallback(func(data Action) {
			fmt.Println("message = ", data)
		})
	}

	go func() {
		for i := 0; i < 100; i++ {
			w.Dispatch(func() {
				w.Emit("myEvent", i)
				w.PostWebMessageAsJSON(map[string]int{"num": i})
			})
			time.Sleep(time.Second * 1)
		}
	}()

	w.AddHtmlContentRoute(host, html)

	w.Navigate(host)

	w.Run()
}
