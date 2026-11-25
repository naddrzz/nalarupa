package main

import (
	"log"
	"os"

	"github.com/egoist/mygo"
	_ "github.com/egoist/mygo/plugins/automation"
	"github.com/egoist/mygo/ui"
)

var appVersion = "0.1.0"

func main() {
	mygo.App.SetName("Nalarupa")
	if appVersion != "" {
		mygo.App.SetVersion(appVersion)
	}
	if dataDir := os.Getenv("NALARUPA_DATA"); dataDir != "" {
		mygo.App.SetPath(mygo.PathUserData, dataDir)
	}

	app := NewNalarupaApp("")
	app.Init()
	defer app.Close()

	w, h, minW, minH := InitialWindowMetrics()

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "Nalarupa",
			Width:           w,
			Height:          h,
			MinWidth:        minW,
			MinHeight:       minH,
			UseContentSize:  true,
			BackgroundColor: "#111113",
			StateKey:        "main",
			Content:         ui.View(app.View),
		})
		if pngBytes := BrandIconPNG(); len(pngBytes) > 0 {
			_ = win.SetIcon(pngBytes)
		}
		DecorateNativeWindow(win)
		app.AttachWindow(win)
	})

	mygo.App.OnQuit(func() {
		app.Close()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
