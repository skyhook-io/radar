//go:build windows

package main

import (
	"log"

	"golang.org/x/sys/windows"
)

// userDownloadsDir asks Windows where Downloads is: people move it to another
// drive from its Properties, and OneDrive can redirect it.
func userDownloadsDir() string {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DEFAULT)
	if err != nil {
		log.Printf("Downloads folder lookup failed, using the home folder's: %v", err)
		return ""
	}
	return dir
}
