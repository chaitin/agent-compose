package imagecache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const metadataVersion = 1

func (c *Cache) LoadMetadata() (MetadataFile, error) {
	path := c.MetadataPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return MetadataFile{Version: metadataVersion}, nil
	}
	if err != nil {
		return MetadataFile{}, newMetadataError("load", path, err)
	}
	if len(data) == 0 {
		return MetadataFile{}, newMetadataError("load", path, errors.New("empty metadata file"))
	}
	var metadata MetadataFile
	if err := json.Unmarshal(data, &metadata); err != nil {
		return MetadataFile{}, newMetadataError("load", path, err)
	}
	if metadata.Version == 0 {
		metadata.Version = metadataVersion
	}
	if metadata.Version != metadataVersion {
		return MetadataFile{}, newMetadataError("load", path, errors.New("unsupported metadata version"))
	}
	if metadata.Images == nil {
		metadata.Images = []ImageMetadata{}
	}
	return metadata, nil
}

func (c *Cache) SaveMetadata(metadata MetadataFile) error {
	if err := c.Ensure(); err != nil {
		return err
	}
	metadata.Version = metadataVersion
	if metadata.Images == nil {
		metadata.Images = []ImageMetadata{}
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return newMetadataError("save", c.MetadataPath(), err)
	}
	data = append(data, '\n')
	return c.writeFileAtomically(c.MetadataPath(), data, 0o644)
}

func (c *Cache) writeFileAtomically(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := ensureDir(dir); err != nil {
		return NewError(ErrorKindInternal, "write", path, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return NewError(ErrorKindInternal, "write", path, err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return NewError(ErrorKindInternal, "write", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return NewError(ErrorKindInternal, "write", path, err)
	}
	if err := tmp.Close(); err != nil {
		return NewError(ErrorKindInternal, "write", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return NewError(ErrorKindInternal, "write", path, err)
	}
	cleanup = false
	return nil
}
