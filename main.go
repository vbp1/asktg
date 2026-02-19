package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:             "asktg",
		Width:             1240,
		Height:            820,
		HideWindowOnClose: false,
		MinWidth:          900,
		MinHeight:         640,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "asktg-desktop-single-instance",
			OnSecondInstanceLaunch: func(second options.SecondInstanceData) {
				app.showMainWindow()
			},
		},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: options.NewRGB(16, 20, 24),
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
