package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app, err := NewApp()
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	cfg := app.cfg.Get()

	err = wails.Run(&options.App{
		Title:            "Token Monitor",
		Width:            900,
		Height:           720,
		MinWidth:         720,
		MinHeight:        560,
		StartHidden:      cfg.StartMinimized,
		HideWindowOnClose: true, // 关窗口不退出，采集继续；托盘/Dock 再打开
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 246, G: 248, B: 250, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []any{app},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarDefault(),
			About: &mac.AboutInfo{
				Title:   "Token Monitor",
				Message: "采集本机 AI Coding Agent 的 Token 用量并上报到服务端",
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
