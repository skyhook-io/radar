package helm

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Windows readers may temporarily deny replacement of the cached index.
func replaceRepositoryCacheFile(source, destination string) error {
	for attempt := 0; ; attempt++ {
		err := os.Rename(source, destination)
		if err == nil || attempt == 39 || (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}
