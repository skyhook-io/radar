package images

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// InspectLocal builds a filesystem without reading or populating the registry cache.
func InspectLocal(ctx context.Context, img v1.Image, imageRef string) (*ImageFilesystem, error) {
	dir, err := os.MkdirTemp("", "radar-node-layers-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	layers, err := img.Layers()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(layers))
	i := &Inspector{}
	var total int64
	for idx, layer := range layers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := filepath.Join(dir, fmt.Sprintf("%d.tar", idx))
		n, err := saveLocalLayer(ctx, layer, p, maxTotalSize-total)
		if err != nil {
			return nil, err
		}
		total += n
		paths = append(paths, p)
	}
	digest, err := img.Digest()
	if err != nil {
		return nil, err
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	return i.buildFilesystemFromCache(ctx, paths, &layerCacheMetadata{Digest: digest.String(), Platform: cfg.OS + "/" + cfg.Architecture}, imageRef)
}

func saveLocalLayer(ctx context.Context, layer v1.Layer, path string, limit int64) (int64, error) {
	r, err := layer.Uncompressed()
	if err != nil {
		return 0, err
	}
	defer r.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(localContextReader{ctx: ctx, reader: r}, limit+1))
	closeErr := f.Close()
	if err != nil {
		return n, err
	}
	if closeErr != nil {
		return n, closeErr
	}
	if n > limit {
		return n, fmt.Errorf("image layers exceed the inspection size limit")
	}
	return n, nil
}

type localContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r localContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
