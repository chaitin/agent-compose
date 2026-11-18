package agentinput

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Admission defaults. They are daemon-wide and can be narrowed or widened
// through AGENT_INPUT_* configuration.
const (
	DefaultMaxFileBytes = 10 << 20
	DefaultMaxTurnBytes = 32 << 20
	DefaultMaxFiles     = 20

	maxWorkspacePathBytes = 4096
)

// Limits are the daemon-wide admission limits for one turn.
type Limits struct {
	MaxFileBytes uint64
	MaxTurnBytes uint64
	// MaxFiles caps files, not text parts; a prompt or interleaved text does
	// not consume the file budget.
	MaxFiles uint32
	// MaxUploadFileBytes and MaxUploadTurnBytes bound upload-sourced files.
	// They are separate from the inline budget so a large staged file never
	// crowds out request bytes. Zero means the upload source is unavailable.
	MaxUploadFileBytes uint64
	MaxUploadTurnBytes uint64
}

// DefaultLimits returns the limits a daemon applies when no configuration
// overrides them.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes: DefaultMaxFileBytes,
		MaxTurnBytes: DefaultMaxTurnBytes,
		MaxFiles:     DefaultMaxFiles,
	}
}

// Validate admits one normalized turn. Every file type is accepted; only the
// shape, size, digest, and workspace path are checked.
func Validate(parts []Part, limits Limits) error {
	if len(parts) == 0 {
		return fmt.Errorf("%w: a turn requires at least one part", ErrInvalid)
	}
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = DefaultMaxFileBytes
	}
	if limits.MaxTurnBytes == 0 {
		limits.MaxTurnBytes = DefaultMaxTurnBytes
	}
	if limits.MaxFiles == 0 {
		limits.MaxFiles = DefaultMaxFiles
	}
	fileCount := 0
	for _, part := range parts {
		if part.File != nil {
			fileCount++
		}
	}
	if uint32(fileCount) > limits.MaxFiles {
		return fmt.Errorf("%w: turn has %d files, limit is %d", ErrInvalid, fileCount, limits.MaxFiles)
	}
	var totalBytes, uploadBytes uint64
	for index, part := range parts {
		if part.File != nil {
			if part.Text != "" {
				return fmt.Errorf("%w: part %d sets both text and file", ErrInvalid, index)
			}
			fileBytes, err := validateFile(index, *part.File, limits)
			if err != nil {
				return err
			}
			if part.File.Source == SourceUpload {
				uploadBytes += fileBytes
			} else {
				totalBytes += fileBytes
			}
			continue
		}
		if strings.TrimSpace(part.Text) == "" {
			return fmt.Errorf("%w: part %d is empty", ErrInvalid, index)
		}
	}
	if limits.MaxTurnBytes > 0 && totalBytes > limits.MaxTurnBytes {
		return fmt.Errorf("%w: turn files are %d bytes, limit is %d", ErrInvalid, totalBytes, limits.MaxTurnBytes)
	}
	if uploadBytes > limits.MaxUploadTurnBytes {
		return fmt.Errorf("%w: turn upload files are %d bytes, limit is %d", ErrInvalid, uploadBytes, limits.MaxUploadTurnBytes)
	}
	return nil
}

func validateFile(index int, file File, limits Limits) (uint64, error) {
	if strings.TrimSpace(file.Name) == "" {
		return 0, fmt.Errorf("%w: part %d file name is required", ErrInvalid, index)
	}
	if strings.ContainsRune(file.Name, 0) {
		return 0, fmt.Errorf("%w: part %d file name contains a NUL byte", ErrInvalid, index)
	}
	if file.MediaType != "" {
		if _, err := canonicalMediaType(file.MediaType); err != nil {
			return 0, fmt.Errorf("%w: part %d: %w", ErrInvalid, index, err)
		}
	}
	if file.SHA256 != "" && !IsSHA256(file.SHA256) {
		return 0, fmt.Errorf("%w: part %d sha256 must be 64 lowercase hex characters", ErrInvalid, index)
	}
	switch file.Source {
	case SourceInline:
		size := uint64(len(file.Data))
		if size == 0 {
			return 0, fmt.Errorf("%w: part %d has no bytes", ErrInvalid, index)
		}
		if size > limits.MaxFileBytes {
			return 0, fmt.Errorf("%w: part %d is %d bytes, limit is %d", ErrInvalid, index, size, limits.MaxFileBytes)
		}
		if file.SHA256 != "" {
			sum := sha256.Sum256(file.Data)
			if hex.EncodeToString(sum[:]) != file.SHA256 {
				return 0, fmt.Errorf("%w: part %d sha256 does not match its bytes", ErrInvalid, index)
			}
		}
		return size, nil
	case SourceWorkspacePath:
		if _, err := NormalizeWorkspacePath(file.WorkspacePath); err != nil {
			return 0, fmt.Errorf("%w: part %d: %w", ErrInvalid, index, err)
		}
		// The daemon cannot stat the file when it does not share the sandbox
		// filesystem. Execution enforces the limit wherever it can read it.
		return 0, nil
	case SourceUpload:
		if strings.TrimSpace(file.UploadID) == "" {
			return 0, fmt.Errorf("%w: part %d upload_id is required", ErrInvalid, index)
		}
		if limits.MaxUploadFileBytes == 0 {
			return 0, fmt.Errorf("%w: part %d: upload input is unavailable on this daemon", ErrInvalid, index)
		}
		if file.UploadPath == "" || file.UploadSize <= 0 {
			return 0, fmt.Errorf("%w: part %d upload %q was not resolved", ErrInvalid, index, file.UploadID)
		}
		size := uint64(file.UploadSize)
		if size > limits.MaxUploadFileBytes {
			return 0, fmt.Errorf("%w: part %d is %d bytes, upload limit is %d", ErrInvalid, index, size, limits.MaxUploadFileBytes)
		}
		return size, nil
	default:
		return 0, fmt.Errorf("%w: part %d file has no source", ErrInvalid, index)
	}
}

// canonicalMediaType validates an optional media type. Uppercase, parameters,
// and empty components are rejected; the value is informational and never
// gates acceptance.
func canonicalMediaType(value string) (string, error) {
	mediaType := strings.TrimSpace(value)
	if mediaType == "" {
		return "", fmt.Errorf("media type is empty")
	}
	if strings.ContainsAny(mediaType, "; ") {
		return "", fmt.Errorf("media type %q must not carry parameters", mediaType)
	}
	if mediaType != strings.ToLower(mediaType) {
		return "", fmt.Errorf("media type %q must be lowercase", mediaType)
	}
	parts := strings.Split(mediaType, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("media type %q must be type/subtype", mediaType)
	}
	return mediaType, nil
}

// NormalizeWorkspacePath validates and cleans a workspace-relative path.
//
// Absolute paths, ".." escapes, NUL bytes, and over-long paths are rejected; a
// path that cleans to the root is rejected because it names no file.
func NormalizeWorkspacePath(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("workspace path is required")
	}
	if len(value) > maxWorkspacePathBytes {
		return "", fmt.Errorf("workspace path exceeds %d bytes", maxWorkspacePathBytes)
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("workspace path contains a NUL byte")
	}
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("workspace path must be relative")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("workspace path %q escapes the workspace", value)
	}
	return cleaned, nil
}

// IsSHA256 reports whether value is 64 lowercase hex characters.
func IsSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// SafeFileName turns a display name into one safe path segment, keeping a
// useful extension. It never returns an empty string.
func SafeFileName(name, mediaType string) string {
	base := filepath.Base(strings.TrimSpace(name))
	var builder strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	sanitized := strings.Trim(builder.String(), ".")
	if sanitized == "" {
		sanitized = "file"
	}
	if len(sanitized) > 128 {
		extension := filepath.Ext(sanitized)
		sanitized = sanitized[:128-len(extension)] + extension
	}
	if filepath.Ext(sanitized) == "" {
		if extension := extensionForMediaType(mediaType); extension != "" {
			sanitized += extension
		}
	}
	return sanitized
}

func extensionForMediaType(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "application/zip":
		return ".zip"
	default:
		return ""
	}
}
