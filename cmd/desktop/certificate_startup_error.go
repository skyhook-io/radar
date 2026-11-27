package main

import (
	"fmt"
	"html"
	"log"
	"net/http"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// showCertificateStartupError keeps configuration failures visible when Desktop
// is launched without a terminal. It does not start Radar with a different policy.
func showCertificateStartupError(cause error) {
	err := wails.Run(&options.App{
		Title: "Radar - Certificate settings",
		Width: 640, Height: 320,
		AssetServer: &assetserver.Options{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Radar certificate settings</title></head><body style="font-family:system-ui;padding:24px"><h1>Check certificate settings</h1><p>%s</p><p>Set the certificate warning percent to auto or a whole number from 1 to 100 in Radar's config.json, environment variables or launch flags, then restart Radar. High is calculated automatically.</p></body></html>`, html.EscapeString(cause.Error()))
		})},
	})
	if err != nil {
		log.Printf("Could not display certificate settings error: %v", err)
	}
}
