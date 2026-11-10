package helm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

var errChartDigest = errors.New("chart digest validation failed")

// Legacy repositories may omit a digest. An advertised digest must be valid
// and match the exact archive consumed, before Helm loads or renders it.
func verifyChartDigest(data []byte, digest string) error {
	if digest == "" {
		return nil
	}
	digest = strings.TrimSpace(strings.ToLower(digest))
	want, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("%w: invalid SHA-256 digest in repository index", errChartDigest)
	}
	actual := sha256.Sum256(data)
	if !bytes.Equal(actual[:], want) {
		return fmt.Errorf("%w: chart digest does not match repository index", errChartDigest)
	}
	return nil
}

func loadChartWithDigest(path, digest string) (*chart.Chart, error) {
	if digest == "" {
		return loader.Load(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := verifyChartDigest(data, digest); err != nil {
		return nil, err
	}
	return loader.LoadArchive(bytes.NewReader(data))
}

// The source loader refreshes its index itself; cached-index consumers can
// recover from a republished archive by refreshing before retrying.
func withChartDigestGuidance(err error, freshIndex bool) error {
	if !errors.Is(err, errChartDigest) {
		return err
	}
	if freshIndex {
		return fmt.Errorf("%w. Radar just downloaded the repository index. Contact the repository maintainer", err)
	}
	return fmt.Errorf("%w. Refresh the repository and retry. If it still fails, contact the repository maintainer", err)
}
