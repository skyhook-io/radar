package images

// FileNode represents a file or directory in the image filesystem
type FileNode struct {
	Name        string      `json:"name"`
	Path        string      `json:"path"`
	Type        string      `json:"type"` // "file", "dir", "symlink"
	Size        int64       `json:"size,omitempty"`
	Permissions string      `json:"permissions,omitempty"`
	Mode        uint32      `json:"mode,omitempty"`
	ModTime     string      `json:"modTime,omitempty"`
	LinkTarget  string      `json:"linkTarget,omitempty"`
	Children    []*FileNode `json:"children,omitempty"`
}

// ImageFilesystem represents the complete filesystem tree of an image
type ImageFilesystem struct {
	Image      string      `json:"image"`
	Digest     string      `json:"digest,omitempty"`
	Platform   string      `json:"platform,omitempty"`
	Root       *FileNode   `json:"root"`
	TotalFiles int         `json:"totalFiles"`
	TotalSize  int64       `json:"totalSize"`
	Layers     []LayerInfo `json:"layers,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// LayerInfo contains metadata about a single image layer
type LayerInfo struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}

// InspectRequest contains the parameters for inspecting an image
type InspectRequest struct {
	Image           string
	Namespace       string
	PodName         string   // Optional: pod name to auto-discover pull secrets
	PullSecretNames []string // Optional: explicit pull secret names
}

// ImageMetadata contains lightweight metadata about an image (without downloading layers)
type ImageMetadata struct {
	Image      string           `json:"image"`
	Digest     string           `json:"digest"`
	Platform   string           `json:"platform"`
	TotalSize  int64            `json:"totalSize"` // Total compressed size of all layers
	LayerCount int              `json:"layerCount"`
	Cached     bool             `json:"cached"`               // Whether filesystem is already cached
	Filesystem *ImageFilesystem `json:"filesystem,omitempty"` // Included if cached
	AuthMethod string           `json:"authMethod"`           // "anonymous", "credentials", etc.
}
