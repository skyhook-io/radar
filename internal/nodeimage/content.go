// Package nodeimage implements experimental inspection of containerd content files.
package nodeimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

const maxBlobSize int64 = 5 << 30
const maxManifestSize int64 = 4 << 20

// CopyBlob reads only regular SHA-256 content files beneath root.
func CopyBlob(root, digest string, dst io.Writer) error {
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("a sha256 content digest is required")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("containerd content store is unavailable: %w", err)
	}
	defer r.Close()
	f, err := r.Open(filepath.Join("blobs", "sha256", digest[7:]))
	if err != nil {
		return fmt.Errorf("image blob is unavailable: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBlobSize {
		return fmt.Errorf("image blob is not a regular file within the size limit")
	}
	_, err = io.Copy(dst, io.LimitReader(f, maxBlobSize+1))
	return err
}

type blobReader func(context.Context, string, io.Writer) error

// loadImage copies and verifies only blobs reachable from the selected manifest.
// The caller owns dir and removes it after consuming the returned image.
func loadImage(ctx context.Context, dir, digest, platform string, read blobReader) (v1.Image, error) {
	if !digestPattern.MatchString(digest) {
		return nil, fmt.Errorf("the kubelet image ID is not a sha256 digest")
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0700); err != nil {
		return nil, err
	}
	var total int64
	copyBlob := func(d string, limit int64) (string, error) {
		if !digestPattern.MatchString(d) {
			return "", fmt.Errorf("image contains an unsupported digest")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		p := filepath.Join(dir, "blobs", "sha256", d[7:])
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(err) {
			return p, nil
		}
		if err != nil {
			return "", err
		}
		h := sha256.New()
		w := &limitedWriter{dst: io.MultiWriter(f, h), left: min(limit, maxBlobSize-total)}
		err = read(ctx, d, w)
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		total += w.written
		if hex.EncodeToString(h.Sum(nil)) != d[7:] {
			return "", fmt.Errorf("image blob digest verification failed")
		}
		return p, nil
	}
	// Resolve image indexes to the node's platform, with a bounded nesting depth.
	for depth := 0; depth < 4; depth++ {
		p, err := copyBlob(digest, maxManifestSize)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var doc struct {
			SchemaVersion int             `json:"schemaVersion"`
			MediaType     types.MediaType `json:"mediaType"`
			Manifests     []v1.Descriptor `json:"manifests"`
			Config        v1.Descriptor   `json:"config"`
			Layers        []v1.Descriptor `json:"layers"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("image ID does not identify a readable manifest: %w", err)
		}
		if doc.SchemaVersion != 2 {
			return nil, fmt.Errorf("image ID points to local runtime metadata rather than an OCI manifest; this prototype cannot inspect it")
		}
		if len(doc.Manifests) > 0 {
			selected := ""
			for _, d := range doc.Manifests {
				if d.Platform != nil && d.Platform.OS+"/"+d.Platform.Architecture == platform {
					selected = d.Digest.String()
					break
				}
			}
			if selected == "" {
				return nil, fmt.Errorf("image index has no manifest for node platform %s", platform)
			}
			digest = selected
			continue
		}
		if !doc.MediaType.IsImage() || len(doc.Layers) > 256 {
			return nil, fmt.Errorf("image ID does not identify a supported image manifest")
		}
		if _, err := copyBlob(doc.Config.Digest.String(), maxManifestSize); err != nil {
			return nil, err
		}
		for _, layer := range doc.Layers {
			if _, err := copyBlob(layer.Digest.String(), maxBlobSize); err != nil {
				return nil, err
			}
		}
		hash := v1.Hash{Algorithm: "sha256", Hex: digest[7:]}
		index, err := json.Marshal(v1.IndexManifest{SchemaVersion: 2, MediaType: types.OCIImageIndex, Manifests: []v1.Descriptor{{Digest: hash, Size: int64(len(data)), MediaType: doc.MediaType}}})
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0600); err != nil {
			return nil, err
		}
		return layout.Path(dir).Image(hash)
	}
	return nil, fmt.Errorf("image index nesting exceeds the inspection limit")
}

type limitedWriter struct {
	dst           io.Writer
	left, written int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		return 0, fmt.Errorf("image content exceeds the inspection size limit")
	}
	n, err := w.dst.Write(p)
	w.left -= int64(n)
	w.written += int64(n)
	return n, err
}
