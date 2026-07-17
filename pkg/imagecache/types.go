package imagecache

import "time"

const (
	metadataFileName = "metadata.json"
	ociLayoutDirName = "oci"
)

type Config struct {
	Root               string
	DefaultRegistry    string
	InsecureRegistries []string
}

type Cache struct {
	config Config
}

type Platform struct {
	OS           string   `json:"os,omitempty"`
	Architecture string   `json:"architecture,omitempty"`
	Variant      string   `json:"variant,omitempty"`
	OSVersion    string   `json:"os_version,omitempty"`
	OSFeatures   []string `json:"os_features,omitempty"`
	Features     []string `json:"features,omitempty"`
}

type MetadataFile struct {
	Version int             `json:"version"`
	Images  []ImageMetadata `json:"images"`
}

type ImageMetadata struct {
	CacheKey        string            `json:"cache_key"`
	RequestedRef    string            `json:"requested_ref"`
	NormalizedRef   string            `json:"normalized_ref"`
	RepoTags        []string          `json:"repo_tags,omitempty"`
	RepoDigests     []string          `json:"repo_digests,omitempty"`
	ManifestDigest  string            `json:"manifest_digest,omitempty"`
	ConfigDigest    string            `json:"config_digest,omitempty"`
	Platform        Platform          `json:"platform,omitempty"`
	MediaType       string            `json:"media_type,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Env             []string          `json:"env,omitempty"`
	SizeBytes       int64             `json:"size_bytes,omitempty"`
	CreatedAt       time.Time         `json:"created_at,omitempty"`
	PulledAt        time.Time         `json:"pulled_at,omitempty"`
	LayoutCachePath string            `json:"layout_cache_path,omitempty"`
	RootFSCachePath string            `json:"rootfs_cache_path,omitempty"`
}

type PullRequest struct {
	Reference string
	Platform  Platform
}

type PullResult struct {
	Image       ImageMetadata
	ResolvedRef string
	Progress    []ProgressEvent
}

type ProgressEvent struct {
	Message      string
	CurrentBytes int64
	TotalBytes   int64
}

type ListRequest struct {
	Query string
	All   bool
}

type ListResult struct {
	Images []ImageMetadata
}

type InspectRequest struct {
	Reference string
}

type InspectResult struct {
	Image ImageMetadata
}

type RemoveRequest struct {
	Reference     string
	Force         bool
	PruneChildren bool
}

type RemoveResult struct {
	UntaggedRefs []string
	DeletedIDs   []string
	Warnings     []string
}

type MaterializationResult struct {
	ImageID     string
	ResolvedRef string
	LayoutPath  string
	RootFSPath  string
	Env         []string
}
