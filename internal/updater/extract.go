package updater

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// findInExtracted searches dir (and one level of subdirectories) for an entry
// matching the predicate. Returns the full path to the first match.
func findInExtracted(dir string, match func(os.DirEntry) bool) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if match(e) {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	// Check one level deeper
	for _, e := range entries {
		if e.IsDir() {
			subEntries, err := os.ReadDir(filepath.Join(dir, e.Name()))
			if err != nil {
				log.Printf("[updater] Warning: could not read subdirectory %s: %v", e.Name(), err)
				continue
			}
			for _, se := range subEntries {
				if match(se) {
					return filepath.Join(dir, e.Name(), se.Name()), nil
				}
			}
		}
	}
	return "", fmt.Errorf("no matching file found in extracted contents")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
