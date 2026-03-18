//go:build !darwin && !linux && !windows

package version

func dirBirthtime(_ string) int64 {
	return 0
}
