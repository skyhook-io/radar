//go:build linux

package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// applyUpdate on Linux replaces the current binary with the downloaded one.
// Falls back to saving in ~/Downloads with instructions if permission denied.
func applyUpdate(_ context.Context, assetPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve executable symlinks: %w", err)
	}

	// Extract tar.gz
	extractDir, err := os.MkdirTemp("", "radar-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	if err := extractTarGz(assetPath, extractDir); err != nil {
		return fmt.Errorf("extract tar.gz: %w", err)
	}

	// Find the binary
	newBin, err := findExtractedBinary(extractDir)
	if err != nil {
		return fmt.Errorf("find extracted binary: %w", err)
	}
	log.Printf("[updater] Found new binary: %s", newBin)

	// Try to replace the binary in-place
	oldExe := exe + ".old"
	if err := os.Remove(oldExe); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous backup %s: %w", oldExe, err)
	}

	if err := os.Rename(exe, oldExe); err != nil {
		return fmt.Errorf("rename current binary: %w (may need elevated permissions)", err)
	}

	// Copy new binary to target path
	if err := copyFile(newBin, exe); err != nil {
		// Restore old binary
		if restoreErr := os.Rename(oldExe, exe); restoreErr != nil {
			log.Printf("[updater] CRITICAL: failed to restore old binary from %s: %v", oldExe, restoreErr)
			return fmt.Errorf("copy new binary: %w (previous version may be at %s)", err, oldExe)
		}
		return fmt.Errorf("copy new binary (previous version restored): %w", err)
	}

	if err := os.Chmod(exe, 0o755); err != nil {
		// Non-executable binary is unusable — restore the old one
		log.Printf("[updater] chmod failed, attempting rollback: %v", err)
		if restoreErr := os.Rename(oldExe, exe); restoreErr != nil {
			log.Printf("[updater] CRITICAL: rollback also failed: %v", restoreErr)
		}
		return fmt.Errorf("make new binary executable: %w", err)
	}

	// Clean up
	os.Remove(oldExe)
	os.Remove(assetPath)

	return nil
}

// Relaunch re-executes the current binary and exits.
func Relaunch() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	log.Printf("[updater] Relaunching %s", exe)
	cmd := exec.Command(exe)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunch: %w", err)
	}

	runBeforeExit()
	os.Exit(0)
	return nil // unreachable
}

func findExtractedBinary(dir string) (string, error) {
	return findInExtracted(dir, func(e os.DirEntry) bool {
		return !e.IsDir() && strings.Contains(e.Name(), "radar-desktop")
	})
}

func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)
		// Guard against tar slip
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in tar: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("create directory %s: %w", header.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			// A failed close can mean a truncated write; swallowing it would let
			// the updater install a corrupt binary.
			if err := outFile.Close(); err != nil {
				return fmt.Errorf("close %s: %w", header.Name, err)
			}
		}
	}
	return nil
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

func cleanupPlatform() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[updater] Cleanup: could not determine executable path: %v", err)
		return
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		log.Printf("[updater] Cleanup: could not resolve symlinks: %v", err)
		return
	}
	oldExe := exe + ".old"
	if _, err := os.Stat(oldExe); err == nil {
		log.Printf("[updater] Cleaning up previous version: %s", oldExe)
		if err := os.Remove(oldExe); err != nil {
			log.Printf("[updater] Cleanup: failed to remove %s: %v", oldExe, err)
		}
	}
}
