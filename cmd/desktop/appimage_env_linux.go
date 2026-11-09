//go:build linux && cgo

package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"log"
	"os"
	"unsafe"

	"github.com/skyhook-io/radar/internal/desktopenv"
)

// separateChildEnv gives processes Radar starts (shells, auth plugins,
// xdg-open, AI agents) the environment the user launched the AppImage with,
// while GTK and WebKit keep the variables that point them at the bundled
// libraries. The two cannot share one environment: GTK reads its variables
// for the life of the process, and WebKit starts its helper processes from C
// with the C environ.
//
// Go keeps its own copy of the environment, taken at startup; os.Setenv and
// os.Unsetenv write through to the C environ, C's setenv does not touch Go's
// copy. So each variable is first set to the user's value in both, then the
// image's value is put back in the C environ only. Every Go child, including
// those started by libraries (client-go exec plugins, Helm plugins), inherits
// Go's copy. A later os.Setenv of one of these variables overwrites the C
// value again; nothing in Radar does that after this runs (Wails sets
// GDK_BACKEND=x11 only on X11 sessions, where it is already x11).
func separateChildEnv() {
	changes := desktopenv.PlanChildEnv(os.LookupEnv)
	applyChildEnv(changes)
	desktopenv.RecordWebviewEnv(changes)
	if len(changes) > 0 {
		log.Printf("[desktop] AppImage: processes Radar starts get the environment and folder it was launched from (%s)", desktopenv.LaunchDir())
	}
}

func applyChildEnv(changes []desktopenv.EnvChange) {
	for _, c := range changes {
		var err error
		if c.ChildSet {
			err = os.Setenv(c.Key, c.Child)
		} else {
			err = os.Unsetenv(c.Key)
		}
		if err != nil {
			log.Printf("[desktop] could not restore %s for child processes: %v", c.Key, err)
			continue
		}
		if c.ImageSet {
			cSetenv(c.Key, c.Image)
		}
	}
}

func cSetenv(key, value string) {
	k := C.CString(key)
	v := C.CString(value)
	defer C.free(unsafe.Pointer(k))
	defer C.free(unsafe.Pointer(v))
	C.setenv(k, v, 1)
}

// cGetenv reads the C environ, which is what GTK and WebKit see.
func cGetenv(key string) (string, bool) {
	k := C.CString(key)
	defer C.free(unsafe.Pointer(k))
	v := C.getenv(k)
	if v == nil {
		return "", false
	}
	return C.GoString(v), true
}
