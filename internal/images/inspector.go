package images

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	maxFileCount    = 50000           // Safety limit for file count
	maxTotalSize    = 5 << 30         // 5GB safety limit
	layerCacheTTL   = 5 * time.Minute // TTL for cached layers on disk
	maxCachedImages = 5               // Max number of images to cache on disk
	cacheSubdir     = "radar-image-cache"
)

// layerCacheMetadata stores metadata about cached image layers
type layerCacheMetadata struct {
	ImageRef   string    `json:"imageRef"`
	Digest     string    `json:"digest"`
	Platform   string    `json:"platform"`
	LayerCount int       `json:"layerCount"`
	CachedAt   time.Time `json:"cachedAt"`
}

// Inspector handles image filesystem inspection with disk-based layer caching
type Inspector struct {
	cacheDir string
	cacheMu  sync.RWMutex
}

// NewInspector creates a new image inspector
func NewInspector() *Inspector {
	cacheDir := filepath.Join(os.TempDir(), cacheSubdir)

	i := &Inspector{
		cacheDir: cacheDir,
	}

	// Clean cache directory on startup
	i.cleanCacheDir()

	// Start background cleanup goroutine
	go i.cleanupLoop()

	return i
}

// cleanCacheDir removes and recreates the cache directory
func (i *Inspector) cleanCacheDir() {
	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	if err := os.RemoveAll(i.cacheDir); err != nil {
		log.Printf("Warning: failed to remove cache directory: %v", err)
	}
	if err := os.MkdirAll(i.cacheDir, 0755); err != nil {
		log.Printf("Warning: failed to create cache directory: %v", err)
	}
}

// cleanupLoop periodically removes expired entries from the cache
func (i *Inspector) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		i.cleanupExpired()
	}
}

// cleanupExpired removes all expired entries from the disk cache
func (i *Inspector) cleanupExpired() {
	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	entries, err := os.ReadDir(i.cacheDir)
	if err != nil {
		return
	}

	now := time.Now()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadataPath := filepath.Join(i.cacheDir, entry.Name(), "metadata.json")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			// No metadata, remove the directory
			os.RemoveAll(filepath.Join(i.cacheDir, entry.Name()))
			continue
		}

		var meta layerCacheMetadata
		if err := json.Unmarshal(data, &meta); err != nil {
			os.RemoveAll(filepath.Join(i.cacheDir, entry.Name()))
			continue
		}

		if now.Sub(meta.CachedAt) >= layerCacheTTL {
			os.RemoveAll(filepath.Join(i.cacheDir, entry.Name()))
			log.Printf("Cleaned up expired layer cache for: %s", meta.ImageRef)
		}
	}
}

// getCacheKey returns a filesystem-safe cache key from an image digest
func getCacheKey(digest string) string {
	// Digest format: sha256:abc123...
	// Replace : with - for filesystem safety
	return strings.ReplaceAll(digest, ":", "-")
}

// getCachedLayers returns paths to cached layer files if available and not expired
func (i *Inspector) getCachedLayers(digest string) ([]string, *layerCacheMetadata, bool) {
	i.cacheMu.RLock()
	defer i.cacheMu.RUnlock()

	cacheKey := getCacheKey(digest)
	imageDir := filepath.Join(i.cacheDir, cacheKey)
	metadataPath := filepath.Join(imageDir, "metadata.json")

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, nil, false
	}

	var meta layerCacheMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, nil, false
	}

	// Check if expired
	if time.Since(meta.CachedAt) >= layerCacheTTL {
		return nil, nil, false
	}

	// Get layer files
	layersDir := filepath.Join(imageDir, "layers")
	var layerPaths []string
	for idx := 0; idx < meta.LayerCount; idx++ {
		layerPath := filepath.Join(layersDir, fmt.Sprintf("layer-%d.tar", idx))
		if _, err := os.Stat(layerPath); err != nil {
			return nil, nil, false
		}
		layerPaths = append(layerPaths, layerPath)
	}

	return layerPaths, &meta, true
}

// cacheLayers saves image layers to disk
func (i *Inspector) cacheLayers(ctx context.Context, img v1.Image, imageRef string) ([]string, *layerCacheMetadata, error) {
	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	// Get image digest for cache key
	digest, err := img.Digest()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get image digest: %w", err)
	}

	cacheKey := getCacheKey(digest.String())
	imageDir := filepath.Join(i.cacheDir, cacheKey)
	layersDir := filepath.Join(imageDir, "layers")

	// Check if we need to evict old entries
	if err := i.evictOldEntries(); err != nil {
		log.Printf("Warning: failed to evict old cache entries: %v", err)
	}

	// Create directories
	if err := os.MkdirAll(layersDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Get platform info
	configFile, _ := img.ConfigFile()
	platform := ""
	if configFile != nil {
		platform = fmt.Sprintf("%s/%s", configFile.OS, configFile.Architecture)
	}

	// Get layers
	layers, err := img.Layers()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get layers: %w", err)
	}

	// Save each layer to disk
	var layerPaths []string
	for idx, layer := range layers {
		select {
		case <-ctx.Done():
			// Clean up partial cache on cancellation
			os.RemoveAll(imageDir)
			return nil, nil, ctx.Err()
		default:
		}

		layerPath := filepath.Join(layersDir, fmt.Sprintf("layer-%d.tar", idx))
		if err := i.saveLayer(layer, layerPath); err != nil {
			// Clean up on error
			os.RemoveAll(imageDir)
			return nil, nil, fmt.Errorf("failed to save layer %d: %w", idx, err)
		}
		layerPaths = append(layerPaths, layerPath)
	}

	// Save metadata
	meta := layerCacheMetadata{
		ImageRef:   imageRef,
		Digest:     digest.String(),
		Platform:   platform,
		LayerCount: len(layers),
		CachedAt:   time.Now(),
	}
	metaData, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(imageDir, "metadata.json"), metaData, 0644); err != nil {
		os.RemoveAll(imageDir)
		return nil, nil, fmt.Errorf("failed to save metadata: %w", err)
	}

	log.Printf("Cached %d layers for image %s (digest: %s)", len(layers), imageRef, digest.String())
	return layerPaths, &meta, nil
}

// saveLayer downloads and saves a single layer to disk
func (i *Inspector) saveLayer(layer v1.Layer, path string) error {
	reader, err := layer.Uncompressed()
	if err != nil {
		return err
	}
	defer reader.Close()

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, reader)
	return err
}

// evictOldEntries removes oldest entries if cache exceeds maxCachedImages
func (i *Inspector) evictOldEntries() error {
	entries, err := os.ReadDir(i.cacheDir)
	if err != nil {
		return err
	}

	// Count current cached images
	var cached []struct {
		name     string
		cachedAt time.Time
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metadataPath := filepath.Join(i.cacheDir, entry.Name(), "metadata.json")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			continue
		}
		var meta layerCacheMetadata
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		cached = append(cached, struct {
			name     string
			cachedAt time.Time
		}{entry.Name(), meta.CachedAt})
	}

	// If at limit, remove oldest
	if len(cached) >= maxCachedImages {
		// Sort by cachedAt (oldest first)
		sort.Slice(cached, func(i, j int) bool {
			return cached[i].cachedAt.Before(cached[j].cachedAt)
		})
		// Remove oldest entries to make room
		toRemove := len(cached) - maxCachedImages + 1
		for idx := range toRemove {
			path := filepath.Join(i.cacheDir, cached[idx].name)
			os.RemoveAll(path)
			log.Printf("Evicted oldest cached image: %s", cached[idx].name)
		}
	}

	return nil
}

// GetMetadata retrieves lightweight metadata about an image without downloading layers
func (i *Inspector) GetMetadata(ctx context.Context, req InspectRequest) (*ImageMetadata, error) {
	// Try to fetch image reference to get digest
	img, authMethod, err := i.fetchImageBruteForce(ctx, req)
	if err != nil {
		return nil, err
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("failed to get image digest: %w", err)
	}

	// Check if layers are cached
	layerPaths, meta, cached := i.getCachedLayers(digest.String())
	if cached {
		// Build filesystem from cached layers
		fs, err := i.buildFilesystemFromCache(ctx, layerPaths, meta, req.Image)
		if err == nil {
			return &ImageMetadata{
				Image:      req.Image,
				Digest:     meta.Digest,
				Platform:   meta.Platform,
				TotalSize:  fs.TotalSize,
				LayerCount: meta.LayerCount,
				Cached:     true,
				Filesystem: fs,
				AuthMethod: "cached",
			}, nil
		}
		// Cache read failed, continue to fetch fresh
		log.Printf("Failed to read from cache, will re-download: %v", err)
	}

	// Get image config for platform info
	configFile, _ := img.ConfigFile()
	platform := ""
	if configFile != nil {
		platform = fmt.Sprintf("%s/%s", configFile.OS, configFile.Architecture)
	}

	// Get layer information (just metadata, not content)
	layers, err := img.Layers()
	if err != nil {
		return nil, fmt.Errorf("failed to get layers: %w", err)
	}

	var totalSize int64
	for _, layer := range layers {
		size, _ := layer.Size()
		totalSize += size
	}

	return &ImageMetadata{
		Image:      req.Image,
		Digest:     digest.String(),
		Platform:   platform,
		TotalSize:  totalSize,
		LayerCount: len(layers),
		Cached:     false,
		AuthMethod: authMethod,
	}, nil
}

// fetchImageBruteForce tries to fetch an image using anonymous auth first,
// then falls back to authenticated access if anonymous fails
func (i *Inspector) fetchImageBruteForce(ctx context.Context, req InspectRequest) (v1.Image, string, error) {
	ref, err := name.ParseReference(req.Image)
	if err != nil {
		return nil, "", fmt.Errorf("invalid image reference: %w", err)
	}

	// Try anonymous first
	img, err := remote.Image(ref,
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous),
	)
	if err == nil {
		log.Printf("Image %s accessible with anonymous auth", req.Image)
		return img, "anonymous", nil
	}

	// Anonymous failed, try with credentials
	log.Printf("Anonymous auth failed for %s, trying with credentials: %v", req.Image, err)

	keychain := GetAuthenticatedKeychain(req.Image, req.Namespace, req.PullSecretNames)
	img, err = remote.Image(ref,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(keychain),
	)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch image: %w", err)
	}

	registryType := DetectRegistryType(req.Image)
	log.Printf("Image %s accessible with %s credentials", req.Image, registryType)
	return img, string(registryType), nil
}

// Inspect retrieves the filesystem tree for a container image
func (i *Inspector) Inspect(ctx context.Context, req InspectRequest) (*ImageFilesystem, error) {
	// Fetch image to get digest
	img, _, err := i.fetchImageBruteForce(ctx, req)
	if err != nil {
		return nil, err
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("failed to get image digest: %w", err)
	}

	// Check if layers are cached
	layerPaths, meta, cached := i.getCachedLayers(digest.String())
	if cached {
		fs, err := i.buildFilesystemFromCache(ctx, layerPaths, meta, req.Image)
		if err == nil {
			return fs, nil
		}
		log.Printf("Failed to read from cache, will re-download: %v", err)
	}

	// Download and cache layers
	layerPaths, meta, err = i.cacheLayers(ctx, img, req.Image)
	if err != nil {
		return nil, fmt.Errorf("failed to cache layers: %w", err)
	}

	return i.buildFilesystemFromCache(ctx, layerPaths, meta, req.Image)
}

// buildFilesystemFromCache builds the filesystem tree from cached layer files
func (i *Inspector) buildFilesystemFromCache(ctx context.Context, layerPaths []string, meta *layerCacheMetadata, imageRef string) (*ImageFilesystem, error) {
	// Build layer info
	layerInfos := make([]LayerInfo, len(layerPaths))
	for idx := range layerPaths {
		layerInfos[idx] = LayerInfo{
			Digest:    fmt.Sprintf("layer-%d", idx),
			MediaType: "application/vnd.oci.image.layer.v1.tar",
		}
	}

	// Build filesystem tree from cached layers
	root, totalFiles, totalSize, err := buildFilesystemTreeFromFiles(ctx, layerPaths)
	if err != nil {
		return nil, fmt.Errorf("failed to build filesystem tree: %w", err)
	}

	return &ImageFilesystem{
		Image:      imageRef,
		Digest:     meta.Digest,
		Platform:   meta.Platform,
		Root:       root,
		TotalFiles: totalFiles,
		TotalSize:  totalSize,
		Layers:     layerInfos,
	}, nil
}

// buildFilesystemTreeFromFiles constructs the directory tree from cached layer files
func buildFilesystemTreeFromFiles(ctx context.Context, layerPaths []string) (*FileNode, int, int64, error) {
	fileMap := make(map[string]*FileNode)

	root := &FileNode{
		Name:     "/",
		Path:     "/",
		Type:     "dir",
		Children: []*FileNode{},
	}
	fileMap["/"] = root

	totalFiles := 0
	var totalSize int64

	// Process each layer file (bottom to top)
	for _, layerPath := range layerPaths {
		file, err := os.Open(layerPath)
		if err != nil {
			continue
		}

		tr := tar.NewReader(file)
		for {
			select {
			case <-ctx.Done():
				file.Close()
				return nil, 0, 0, ctx.Err()
			default:
			}

			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}

			// Safety limits
			if totalFiles >= maxFileCount {
				file.Close()
				break
			}

			path := "/" + strings.TrimPrefix(header.Name, "./")
			path = filepath.Clean(path)
			if path == "." {
				path = "/"
			}
			name := filepath.Base(path)

			// Handle whiteout files (deletions in OCI layers)
			if after, ok := strings.CutPrefix(name, ".wh."); ok {
				deletedName := after
				deletedPath := filepath.Join(filepath.Dir(path), deletedName)
				deleteFromTree(fileMap, deletedPath)
				continue
			}

			// Skip root
			if path == "/" {
				continue
			}

			node := &FileNode{
				Name:        name,
				Path:        path,
				Size:        header.Size,
				Permissions: fmt.Sprintf("%s", header.FileInfo().Mode()),
				Mode:        uint32(header.Mode),
				ModTime:     header.ModTime.Format("2006-01-02 15:04:05"),
			}

			switch header.Typeflag {
			case tar.TypeDir:
				node.Type = "dir"
				if existing, ok := fileMap[path]; ok && existing.Type == "dir" {
					node.Children = existing.Children
				} else {
					node.Children = []*FileNode{}
				}
			case tar.TypeSymlink:
				node.Type = "symlink"
				node.LinkTarget = header.Linkname
			default:
				node.Type = "file"
				totalSize += header.Size
			}

			ensureParentDirs(fileMap, path)
			fileMap[path] = node
			totalFiles++
		}
		file.Close()
	}

	// Build tree structure from flat map
	for path, node := range fileMap {
		if path == "/" {
			continue
		}
		parentPath := filepath.Dir(path)
		if parentPath == "." {
			parentPath = "/"
		}
		if parent, ok := fileMap[parentPath]; ok && parent.Type == "dir" {
			found := false
			for _, child := range parent.Children {
				if child.Path == node.Path {
					found = true
					break
				}
			}
			if !found {
				parent.Children = append(parent.Children, node)
			}
		}
	}

	sortFileTree(root)
	return root, totalFiles, totalSize, nil
}

// ensureParentDirs creates parent directory nodes if they don't exist
func ensureParentDirs(fileMap map[string]*FileNode, path string) {
	dir := filepath.Dir(path)
	if dir == "." || dir == "/" {
		return
	}

	if _, ok := fileMap[dir]; !ok {
		fileMap[dir] = &FileNode{
			Name:     filepath.Base(dir),
			Path:     dir,
			Type:     "dir",
			Children: []*FileNode{},
		}
		ensureParentDirs(fileMap, dir)
	}
}

// deleteFromTree removes a path and all its children from the file map
func deleteFromTree(fileMap map[string]*FileNode, path string) {
	delete(fileMap, path)
	prefix := path + "/"
	for p := range fileMap {
		if strings.HasPrefix(p, prefix) {
			delete(fileMap, p)
		}
	}
}

// sortFileTree recursively sorts the filesystem tree
func sortFileTree(node *FileNode) {
	if node.Children == nil {
		return
	}

	sort.Slice(node.Children, func(i, j int) bool {
		if node.Children[i].Type == "dir" && node.Children[j].Type != "dir" {
			return true
		}
		if node.Children[i].Type != "dir" && node.Children[j].Type == "dir" {
			return false
		}
		return node.Children[i].Name < node.Children[j].Name
	})

	for _, child := range node.Children {
		sortFileTree(child)
	}
}

// GetFileContent retrieves the content of a specific file from an image
func (i *Inspector) GetFileContent(ctx context.Context, req InspectRequest, filePath string) ([]byte, string, error) {
	// Fetch image to get digest
	img, _, err := i.fetchImageBruteForce(ctx, req)
	if err != nil {
		return nil, "", err
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get image digest: %w", err)
	}

	// Check if layers are cached
	layerPaths, _, cached := i.getCachedLayers(digest.String())
	if !cached {
		// Cache layers first
		layerPaths, _, err = i.cacheLayers(ctx, img, req.Image)
		if err != nil {
			return nil, "", fmt.Errorf("failed to cache layers: %w", err)
		}
	}

	// Read file from cached layers
	return readFileFromCachedLayers(ctx, layerPaths, filePath)
}

// readFileFromCachedLayers reads a specific file from cached layer files
func readFileFromCachedLayers(ctx context.Context, layerPaths []string, filePath string) ([]byte, string, error) {
	targetPath := "/" + strings.TrimPrefix(filePath, "/")
	targetPath = filepath.Clean(targetPath)

	deleted := false
	var content []byte
	var filename string

	// Process layers (bottom to top)
	for _, layerPath := range layerPaths {
		file, err := os.Open(layerPath)
		if err != nil {
			continue
		}

		tr := tar.NewReader(file)
		for {
			select {
			case <-ctx.Done():
				file.Close()
				return nil, "", ctx.Err()
			default:
			}

			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}

			path := "/" + strings.TrimPrefix(header.Name, "./")
			path = filepath.Clean(path)
			name := filepath.Base(path)

			// Check for whiteout (deletion)
			if after, ok := strings.CutPrefix(name, ".wh."); ok {
				deletedName := after
				deletedPath := filepath.Join(filepath.Dir(path), deletedName)
				if deletedPath == targetPath {
					deleted = true
					content = nil
					filename = ""
				}
				continue
			}

			// Check if this is our target file
			if path == targetPath && header.Typeflag != tar.TypeDir {
				deleted = false
				filename = filepath.Base(path)

				data, err := io.ReadAll(tr)
				if err != nil {
					file.Close()
					return nil, "", fmt.Errorf("failed to read file content: %w", err)
				}
				content = data
			}
		}
		file.Close()
	}

	if deleted || content == nil {
		return nil, "", fmt.Errorf("file not found: %s", filePath)
	}

	return content, filename, nil
}
