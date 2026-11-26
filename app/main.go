package main

import (
	"log"
	"os"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	_ "github.com/egoist/mygo/plugins/automation"
	"github.com/egoist/mygo/ui"
)

func init() {
	// Pin the main goroutine to OS thread 0 so Win32 GUI, Direct3D 11 swapchain, and CGO calls never migrate threads.
	runtime.LockOSThread()
}

var appVersion = "0.1.0"

// shouldEnableOBSBitBltCompat reports whether legacy GDI software presentation (MYGO_GPU=0) was requested
// for environments where OBS uses legacy BitBlt Window Capture instead of Windows Graphics Capture (WGC).
func shouldEnableOBSBitBltCompat(args []string, getenv func(string) string) bool {
	if getenv("MYGO_GPU") != "" {
		return false
	}
	for _, k := range []string{"NALARUPA_OBS_COMPAT", "NALARUPA_SOFTWARE_RENDER"} {
		v := strings.ToLower(strings.TrimSpace(getenv(k)))
		if v == "1" || v == "true" || v == "yes" {
			return true
		}
	}
	for _, a := range args {
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "--obs-compat", "--software-render":
			return true
		}
	}
	return false
}

func main() {
	if shouldEnableOBSBitBltCompat(os.Args[1:], os.Getenv) {
		_ = os.Setenv("MYGO_GPU", "0")
	}

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

	mygo.App.WhenReady(func() {
		mygo.Theme.SetSource(mygo.ThemeDark)

		w, h, minW, minH := InitialWindowMetrics()
		if work := mygo.Screen.PrimaryDisplay().WorkArea; work.Width > 0 && work.Height > 0 {
			w, h, minW, minH = ComputeInitialWindowSize(work.Width, work.Height)
		}

		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "Nalarupa",
			Width:           w,
			Height:          h,
			MinWidth:        minW,
			MinHeight:       minH,
			UseContentSize:  false,
			Frameless:       false,
			BackgroundColor: "#111113",
			StateKey:        "main",
			Content:         ui.View(app.View),
		})
		if pngBytes := BrandIconPNG(); len(pngBytes) > 0 {
			_ = win.SetIcon(pngBytes)
		}
		DecorateNativeWindow(win)
		EnsureWindowInWorkArea(win)
		app.AttachWindow(win)
	})

	mygo.App.OnQuit(func() {
		app.Close()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
