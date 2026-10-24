//go:build !windows

package helm

import "os"

func replaceRepositoryCacheFile(source, destination string) error {
	return os.Rename(source, destination)
}
