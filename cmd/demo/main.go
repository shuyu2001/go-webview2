package main

import (
	"log"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
)

func main() {
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:          true,
		AutoFocus:      true,
		Width:          600,
		Height:         500,
		Center:         true,
		DisableResize:  true,
		StartMaximized: true,
		Chromium:       edge.NewChromium(),
	})
	if w == nil {
		log.Fatalln("Failed to load webview.")
	}
	w.SetSize(600, 500)
	defer w.Destroy()
	w.Navigate("https://en.m.wikipedia.org/wiki/Main_Page")
	w.Run()
}
