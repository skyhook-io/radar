//go:build !linux

package desktopenv

// collect reports nothing off Linux. macOS (WKWebView) and Windows (WebView2)
// have no equivalent set of host render knobs to surface.
func collect() *Snapshot { return nil }

func webviewLibrary() string { return "" }
