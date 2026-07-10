package runtimecache

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"agent-compose/pkg/identity"
)

var (
	ErrInvalidCacheID   = errors.New("invalid runtime cache id")
	ErrAmbiguousCacheID = errors.New("ambiguous runtime cache id")
	kindSegmentRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

type ParsedCacheID struct {
	ID   string
	Hash string
}

func GenerateCacheID(item Item) (string, error) {
	domain, ok := NormalizeDomain(item.Domain)
	if !ok || domain == "" {
		return "", fmt.Errorf("%w: domain is required", ErrInvalidCacheID)
	}
	driver, ok := NormalizeDriver(item.Driver)
	if !ok {
		return "", fmt.Errorf("%w: unknown driver %q", ErrInvalidCacheID, item.Driver)
	}
	if driver == "" {
		driver = DriverAll
	}
	kind := normalizeKind(item.Kind)
	if kind == "" {
		return "", fmt.Errorf("%w: kind is required", ErrInvalidCacheID)
	}
	identityValue := cacheIDIdentity(item)
	if identityValue == "" {
		return "", fmt.Errorf("%w: identity is required", ErrInvalidCacheID)
	}
	return identity.NewID(identity.ResourceKind("cache:"+kind), string(domain), driver, identityValue), nil
}

func ParseCacheID(cacheID string) (ParsedCacheID, error) {
	hash, err := identity.Hash(cacheID)
	if err != nil {
		return ParsedCacheID{}, fmt.Errorf("%w: %v", ErrInvalidCacheID, err)
	}
	return ParsedCacheID{ID: hash, Hash: hash}, nil
}

func ShortCacheID(cacheID string) string {
	return identity.ShortID(cacheID)
}

func ValidateCacheIDReference(ref string) error {
	ref = strings.TrimSpace(ref)
	if _, err := ParseCacheID(ref); err == nil {
		return nil
	}
	if identity.IsIDPrefix(ref) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidCacheID, ref)
}

func ResolveCacheID(items []Item, ref string) (string, error) {
	ref = strings.TrimSpace(strings.ToLower(ref))
	if parsed, err := ParseCacheID(ref); err == nil {
		for _, item := range items {
			itemHash, hashErr := identity.Hash(item.CacheID)
			if hashErr == nil && itemHash == parsed.Hash {
				return item.CacheID, nil
			}
		}
		return parsed.ID, nil
	}
	if !identity.IsIDPrefix(ref) {
		return "", fmt.Errorf("%w: %s", ErrInvalidCacheID, ref)
	}
	prefix := strings.TrimPrefix(ref, identity.Prefix)
	var matched string
	for _, item := range items {
		hash := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(item.CacheID)), identity.Prefix)
		if !strings.HasPrefix(hash, prefix) {
			continue
		}
		if matched != "" && matched != item.CacheID {
			return "", fmt.Errorf("%w: %s", ErrAmbiguousCacheID, ref)
		}
		matched = item.CacheID
	}
	if matched == "" {
		return "", fmt.Errorf("%w: %s", ErrCacheNotFound, ref)
	}
	return matched, nil
}

func cacheIDIdentity(item Item) string {
	for _, candidate := range []string{
		item.Path,
		item.ImageID,
		item.ResolvedRef,
		item.ImageRef,
		item.SandboxID,
	} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, string(filepath.Separator)) {
			return filepath.Clean(candidate)
		}
		return candidate
	}
	return ""
}

func normalizeKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !kindSegmentRE.MatchString(kind) {
		return ""
	}
	return kind
}
