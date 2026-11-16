package agentinput

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"strings"
)

// Canonical media types the daemon accepts. Providers declare the subset they
// consume through pkg/capmatrix.
const (
	MediaTypePNG  = "image/png"
	MediaTypeJPEG = "image/jpeg"
	MediaTypeGIF  = "image/gif"
	MediaTypeWebP = "image/webp"
	MediaTypePDF  = "application/pdf"
)

// Admission defaults. They are daemon-wide; a provider may only narrow them.
const (
	DefaultMaxPartBytes = 10 << 20
	DefaultMaxTurnBytes = 32 << 20
	DefaultMaxParts     = 20

	maxWorkspacePathBytes = 4096
)

// ImageMediaTypes returns the canonical raster image types, in stable order.
func ImageMediaTypes() []string {
	return []string{MediaTypePNG, MediaTypeJPEG, MediaTypeGIF, MediaTypeWebP}
}

// AllMediaTypes returns every canonical media type the daemon understands, in
// stable order. A provider declares the subset it consumes.
func AllMediaTypes() []string {
	return append(ImageMediaTypes(), MediaTypePDF)
}

// Limits are the daemon-wide admission limits for one turn.
type Limits struct {
	MaxPartBytes uint64
	MaxTurnBytes uint64
	MaxParts     uint32
}

// DefaultLimits returns the limits every daemon applies before a provider
// narrows them.
func DefaultLimits() Limits {
	return Limits{
		MaxPartBytes: DefaultMaxPartBytes,
		MaxTurnBytes: DefaultMaxTurnBytes,
		MaxParts:     DefaultMaxParts,
	}
}

// Policy is the admission policy of one provider: the daemon limits narrowed by
// what that provider consumes.
type Policy struct {
	Limits     Limits
	MediaTypes []string
}

// AllowsMediaType reports whether the policy accepts mediaType.
func (p Policy) AllowsMediaType(mediaType string) bool {
	for _, allowed := range p.MediaTypes {
		if allowed == mediaType {
			return true
		}
	}
	return false
}

// Validate admits one normalized turn. It checks part shape, media type
// syntax, the provider allowlist, size limits, digests, and that the declared
// media type matches the content's magic bytes for inline bytes.
func Validate(parts []Part, policy Policy) error {
	if len(parts) == 0 {
		return fmt.Errorf("%w: a turn requires at least one part", ErrInvalid)
	}
	limits := policy.Limits
	if limits.MaxPartBytes == 0 {
		limits.MaxPartBytes = DefaultMaxPartBytes
	}
	if limits.MaxTurnBytes == 0 {
		limits.MaxTurnBytes = DefaultMaxTurnBytes
	}
	if limits.MaxParts == 0 {
		limits.MaxParts = DefaultMaxParts
	}
	if uint32(len(parts)) > limits.MaxParts {
		return fmt.Errorf("%w: turn has %d parts, limit is %d", ErrInvalid, len(parts), limits.MaxParts)
	}
	var totalBytes uint64
	for index, part := range parts {
		if part.Media != nil {
			if part.Text != "" {
				return fmt.Errorf("%w: part %d sets both text and media", ErrInvalid, index)
			}
			mediaBytes, err := validateMedia(index, *part.Media, policy, limits)
			if err != nil {
				return err
			}
			totalBytes += mediaBytes
			continue
		}
		if strings.TrimSpace(part.Text) == "" {
			return fmt.Errorf("%w: part %d is empty", ErrInvalid, index)
		}
	}
	if limits.MaxTurnBytes > 0 && totalBytes > limits.MaxTurnBytes {
		return fmt.Errorf("%w: turn media is %d bytes, limit is %d", ErrInvalid, totalBytes, limits.MaxTurnBytes)
	}
	return nil
}

func validateMedia(index int, media Media, policy Policy, limits Limits) (uint64, error) {
	mediaType, err := canonicalMediaType(media.Type)
	if err != nil {
		return 0, fmt.Errorf("%w: part %d: %w", ErrInvalid, index, err)
	}
	if !policy.AllowsMediaType(mediaType) {
		return 0, fmt.Errorf("%w: part %d media type %q is not supported by this provider (supported: %s)",
			ErrInvalid, index, mediaType, strings.Join(policy.MediaTypes, ", "))
	}
	if media.SHA256 != "" && !IsSHA256(media.SHA256) {
		return 0, fmt.Errorf("%w: part %d sha256 must be 64 lowercase hex characters", ErrInvalid, index)
	}
	switch media.Source {
	case SourceInline:
		size := uint64(len(media.Data))
		if size == 0 {
			return 0, fmt.Errorf("%w: part %d has no bytes", ErrInvalid, index)
		}
		if size > limits.MaxPartBytes {
			return 0, fmt.Errorf("%w: part %d is %d bytes, limit is %d", ErrInvalid, index, size, limits.MaxPartBytes)
		}
		if detected := DetectMediaType(media.Data); detected != mediaType {
			return 0, fmt.Errorf("%w: part %d declares %q but content is %q", ErrInvalid, index, mediaType, detected)
		}
		if media.SHA256 != "" {
			sum := sha256.Sum256(media.Data)
			if hex.EncodeToString(sum[:]) != media.SHA256 {
				return 0, fmt.Errorf("%w: part %d sha256 does not match its bytes", ErrInvalid, index)
			}
		}
		return size, nil
	case SourceWorkspacePath:
		cleaned, err := NormalizeWorkspacePath(media.WorkspacePath)
		if err != nil {
			return 0, fmt.Errorf("%w: part %d: %w", ErrInvalid, index, err)
		}
		// The daemon cannot stat the file when it does not share the sandbox
		// filesystem, so size is checked by the guest runtime before the turn.
		_ = cleaned
		return 0, nil
	default:
		return 0, fmt.Errorf("%w: part %d media has no source", ErrInvalid, index)
	}
}

// canonicalMediaType accepts a lowercase slash-separated type and rejects
// parameters, uppercase, and empty components. Rejecting rather than
// normalizing keeps a caller from believing a type was honored when it was
// silently rewritten.
func canonicalMediaType(value string) (string, error) {
	mediaType := strings.TrimSpace(value)
	if mediaType == "" {
		return "", fmt.Errorf("media type is required")
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

// DetectMediaType returns the canonical media type inferred from magic bytes,
// or "application/octet-stream" when the content is not one of the admitted
// formats.
func DetectMediaType(data []byte) string {
	return strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0])
}

// NormalizeWorkspacePath validates and cleans a workspace-relative path.
//
// Only the workspace root is addressable. Absolute paths, ".." escapes, NUL
// bytes, and over-long paths are rejected; a path that cleans to the root is
// rejected because it names no file.
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
