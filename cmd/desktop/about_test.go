package main

import (
	"strings"
	"testing"
)

func TestAboutText(t *testing.T) {
	got := aboutText("v1.20.4")
	if !strings.Contains(got, "Version 1.20.4") || !strings.HasPrefix(got, aboutTagline) {
		t.Fatalf("aboutText = %q", got)
	}
}

func TestOpenAboutScript(t *testing.T) {
	got := openAboutScript("1.20.4", "windows", "amd64")
	want := `window.dispatchEvent(new CustomEvent('radar:open-about',{detail:{"platform":"Windows · amd64","version":"1.20.4"}}))`
	if got != want {
		t.Fatalf("openAboutScript =\n%s\nwant\n%s", got, want)
	}
	if platformLabel("freebsd", "arm64") != "freebsd · arm64" {
		t.Fatal("unknown OS should fall back to its GOOS name")
	}
}
