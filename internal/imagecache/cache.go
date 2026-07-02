package imagecache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func New(config Config) (*Cache, error) {
	root := strings.TrimSpace(config.Root)
	if root == "" {
		return nil, NewError(ErrorKindInvalidReference, "initialize", "", fmt.Errorf("image cache root is empty"))
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, NewError(ErrorKindInternal, "initialize", root, err)
	}
	config.Root = abs
	cache := &Cache{config: config}
	if err := cache.Ensure(); err != nil {
		return nil, err
	}
	return cache, nil
}

func (c *Cache) Config() Config {
	return c.config
}

func (c *Cache) Root() string {
	return c.config.Root
}

func (c *Cache) MetadataPath() string {
	return filepath.Join(c.config.Root, metadataFileName)
}

func (c *Cache) OCILayoutPath() string {
	return filepath.Join(c.config.Root, ociLayoutDirName)
}

func (c *Cache) MaterializationRoot() string {
	return filepath.Join(filepath.Dir(c.config.Root), "image-cache")
}

func (c *Cache) MaterializedImageDir(imageID string) string {
	return filepath.Join(c.MaterializationRoot(), sanitizePathSegment(imageID))
}

func (c *Cache) MaterializedOCILayoutPath(imageID string) string {
	return filepath.Join(c.MaterializedImageDir(imageID), ociLayoutDirName)
}

func (c *Cache) MaterializedRootFSPath(imageID string) string {
	return filepath.Join(c.MaterializedImageDir(imageID), "rootfs")
}

func (c *Cache) Ensure() error {
	if err := ensureDir(c.config.Root); err != nil {
		return NewError(ErrorKindInternal, "initialize", c.config.Root, err)
	}
	if err := ensureDir(c.OCILayoutPath()); err != nil {
		return NewError(ErrorKindInternal, "initialize", c.OCILayoutPath(), err)
	}
	return nil
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

func sanitizePathSegment(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "sha256:"))
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
