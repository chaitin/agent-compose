package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	backendImageKey             = "AGENT_COMPOSE_IMAGE"
	frontendImageKey            = "AGENT_COMPOSE_FRONTEND_IMAGE"
	guestImageKey               = "DEFAULT_IMAGE"
	sandboxHostImageKey         = "AGENT_COMPOSE_SANDBOX_HOST_IMAGE"
	frontendVersionKey          = "AGENT_COMPOSE_FRONTEND_VERSION"
	frontendVersionsKey         = "AGENT_COMPOSE_FRONTEND_VERSIONS"
	installerRegistryKey        = "INSTALLER_REGISTRY"
	installerImagePrefixKey     = "INSTALLER_IMAGE_PREFIX"
	installerFrontendVersionKey = "INSTALLER_FRONTEND_VERSION"
)

var frontendTagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// ImageSource identifies why an image will be effective after an operation.
type ImageSource string

const (
	ImageSourceRelease   ImageSource = "release"
	ImageSourceOverride  ImageSource = "override"
	ImageSourcePreserved ImageSource = "preserved"
)

// ImageSelection is an effective image reference and the policy that chose it.
type ImageSelection struct {
	Value  string
	Source ImageSource
}

// ImagePreview contains the effective images for a proposed operation.
type ImagePreview struct {
	Backend         ImageSelection
	Frontend        ImageSelection
	Guest           ImageSelection
	Registry        string
	FrontendVersion string
}

// ImageReferences contains the three image defaults published by a release.
type ImageReferences struct {
	Backend  string
	Frontend string
	Guest    string
}

// Release owns a verified deployment bundle and its image defaults.
type Release struct {
	bundle                 *bundle
	version                string
	Images                 ImageReferences
	FrontendVersions       []string
	DefaultFrontendVersion string
}

// Close releases temporary files owned by the resolved release.
func (r *Release) Close() {
	if r == nil || r.bundle == nil {
		return
	}
	r.bundle.Close()
	r.bundle = nil
}

// ResolveRelease loads and verifies the selected release without modifying an
// installation. The caller must close the returned release.
func (s Service) ResolveRelease(ctx context.Context, options Options) (*Release, error) {
	loaded, err := (bundleLoader{client: s.HTTPClient}).Load(ctx, options)
	if err != nil {
		return nil, err
	}
	release := &Release{bundle: loaded, version: options.Version}
	frontendVersions, defaultFrontendVersion, err := supportedFrontendVersions(loaded.Manifest)
	if err != nil {
		release.Close()
		return nil, err
	}
	release.FrontendVersions = frontendVersions
	release.DefaultFrontendVersion = defaultFrontendVersion
	envData, err := os.ReadFile(filepath.Join(loaded.Dir, ".env.example"))
	if err != nil {
		release.Close()
		return nil, fmt.Errorf("read release environment defaults: %w", err)
	}
	env := parseEnvFile(envData)
	state := parseEnvFile(nil)
	defaultOptions := options
	defaultOptions.BackendImage, defaultOptions.BackendImageSet = "", false
	defaultOptions.FrontendImage, defaultOptions.FrontendImageSet = "", false
	defaultOptions.GuestImage, defaultOptions.GuestImageSet = "", false
	defaultOptions.Registry, defaultOptions.RegistrySet = "", false
	defaultOptions.ImagePrefix = ""
	defaultOptions.FrontendVersion = defaultFrontendVersion
	defaultOptions.FrontendVersionSet = true
	plan := planImageReferences(env, state, loaded.Manifest, defaultOptions, "install")
	if err := applyPlannedImageReferences(env, state, plan); err != nil {
		release.Close()
		return nil, err
	}
	release.Images = imageReferences(env)
	return release, nil
}

// PreviewImages reports the values that the selected release would make
// effective, including existing operator-owned values preserved on upgrade.
func (s Service) PreviewImages(operation Operation, options Options, release *Release) (ImagePreview, error) {
	if release == nil || release.bundle == nil {
		return ImagePreview{}, fmt.Errorf("resolved release is required")
	}
	envData, envExists, err := readOptionalFile(filepath.Join(options.InstallDir, ".env"))
	if err != nil {
		return ImagePreview{}, err
	}
	if !envExists {
		envData, err = os.ReadFile(filepath.Join(release.bundle.Dir, ".env.example"))
		if err != nil {
			return ImagePreview{}, fmt.Errorf("read release environment defaults: %w", err)
		}
	}
	stateData, _, err := readOptionalFile(filepath.Join(options.InstallDir, ".installer-state.env"))
	if err != nil {
		return ImagePreview{}, err
	}
	mode := "set-missing"
	if !envExists {
		mode = "install"
	} else if operation == OperationUpgrade {
		mode = "upgrade"
	}
	env := parseEnvFile(envData)
	state := parseEnvFile(stateData)
	effective, err := effectiveImageOptions(env, state, release.bundle.Manifest, options)
	if err != nil {
		return ImagePreview{}, err
	}
	plan := planImageReferences(env, state, release.bundle.Manifest, effective, mode)
	return ImagePreview{
		Backend:         plan.selection(backendImageKey),
		Frontend:        plan.selection(frontendImageKey),
		Guest:           plan.selection(guestImageKey),
		Registry:        strings.TrimSpace(effective.Registry),
		FrontendVersion: effective.FrontendVersion,
	}, nil
}

type plannedImage struct {
	value     string
	set       bool
	selection ImageSelection
}

type imageReferencePlan map[string]plannedImage

func (p imageReferencePlan) selection(key string) ImageSelection { return p[key].selection }

func planImageReferences(env, state, manifest *envFile, options Options, mode string) imageReferencePlan {
	desired := desiredImageReferences(manifest, options)
	explicit := map[string]bool{
		backendImageKey:    options.BackendImageSet,
		frontendVersionKey: options.FrontendVersionSet,
		frontendImageKey:   options.FrontendImageSet,
		guestImageKey:      options.GuestImageSet,
	}
	plan := imageReferencePlan{}
	for _, key := range []string{backendImageKey, frontendVersionKey, frontendImageKey, guestImageKey, sandboxHostImageKey} {
		value, desiredExists := desired[key]
		current, currentExists := env.Get(key)
		managed, managedExists := state.Get(key)
		shouldSet := explicit[key] || mode == "install" || !currentExists || current == ""
		if mode == "upgrade" && managedExists && current == managed {
			shouldSet = true
		}
		item := plannedImage{value: value, set: desiredExists && shouldSet}
		switch {
		case item.set && explicit[key]:
			item.selection = ImageSelection{Value: value, Source: ImageSourceOverride}
		case item.set:
			item.selection = ImageSelection{Value: value, Source: ImageSourceRelease}
		case mode == "install" && currentExists:
			item.selection = ImageSelection{Value: strings.TrimSpace(current), Source: ImageSourceRelease}
		case currentExists:
			item.selection = ImageSelection{Value: strings.TrimSpace(current), Source: ImageSourcePreserved}
		}
		plan[key] = item
	}
	return plan
}

func desiredImageReferences(manifest *envFile, options Options) map[string]string {
	desired := map[string]string{}
	if options.ImagePrefix != "" {
		version := options.Version
		if image, ok := manifest.Get(backendImageKey); ok {
			if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
				version = image[colon+1:]
			}
		}
		frontendVersion := options.FrontendVersion
		// The sandbox-host shares the daemon image repository but its tag is
		// advanced independently by the release manifest, so a daemon-only
		// upgrade does not recreate the host. Fall back to the daemon tag when
		// the manifest does not publish a sandbox-host image yet.
		sandboxHostVersion := version
		if image, ok := manifest.Get(sandboxHostImageKey); ok {
			if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
				sandboxHostVersion = image[colon+1:]
			}
		}
		desired[backendImageKey] = options.ImagePrefix + "/agent-compose:" + version
		desired[frontendVersionKey] = frontendVersion
		desired[frontendImageKey] = options.ImagePrefix + "/agent-compose-ui:" + frontendVersion
		desired[guestImageKey] = options.ImagePrefix + "/agent-compose-guest:" + version
		desired[sandboxHostImageKey] = options.ImagePrefix + "/agent-compose:" + sandboxHostVersion
	} else {
		for _, key := range []string{backendImageKey, frontendVersionKey, frontendImageKey, guestImageKey, sandboxHostImageKey} {
			if value, ok := manifest.Get(key); ok {
				desired[key] = value
			}
		}
		if options.RegistrySet && strings.TrimSpace(options.Registry) != "" {
			for _, key := range []string{backendImageKey, frontendImageKey, guestImageKey, sandboxHostImageKey} {
				if value, ok := desired[key]; ok {
					desired[key] = replaceImageRegistry(value, options.Registry)
				}
			}
		}
		defaultFrontendVersion, _ := manifest.Get(frontendVersionKey)
		if frontend, ok := desired[frontendImageKey]; ok && options.FrontendVersion != strings.TrimSpace(defaultFrontendVersion) {
			desired[frontendImageKey] = replaceImageTag(frontend, options.FrontendVersion)
		}
		desired[frontendVersionKey] = options.FrontendVersion
	}
	if options.BackendImageSet {
		desired[backendImageKey] = strings.TrimSpace(options.BackendImage)
	}
	if options.FrontendImageSet {
		desired[frontendImageKey] = strings.TrimSpace(options.FrontendImage)
	}
	if options.GuestImageSet {
		desired[guestImageKey] = strings.TrimSpace(options.GuestImage)
	}
	return desired
}

func applyImageReferences(env, state, manifest *envFile, options Options, mode string) error {
	effective, err := effectiveImageOptions(env, state, manifest, options)
	if err != nil {
		return err
	}
	if err := applyPlannedImageReferences(env, state, planImageReferences(env, state, manifest, effective, mode)); err != nil {
		return err
	}
	if effective.ImagePrefix != "" {
		if err := state.Set(installerImagePrefixKey, effective.ImagePrefix); err != nil {
			return err
		}
		if err := state.Set(installerRegistryKey, ""); err != nil {
			return err
		}
	} else {
		if err := state.Set(installerRegistryKey, effective.Registry); err != nil {
			return err
		}
		if err := state.Set(installerImagePrefixKey, ""); err != nil {
			return err
		}
	}
	return state.Set(installerFrontendVersionKey, effective.FrontendVersion)
}

func applyPlannedImageReferences(env, state *envFile, plan imageReferencePlan) error {
	for key, item := range plan {
		if !item.set {
			continue
		}
		if err := env.Set(key, item.value); err != nil {
			return err
		}
		if err := state.Set(key, item.value); err != nil {
			return err
		}
	}
	return state.Set("INSTALLER_PAYLOAD_VERSION", "1")
}

func imageReferences(env *envFile) ImageReferences {
	backend, _ := env.Get(backendImageKey)
	frontend, _ := env.Get(frontendImageKey)
	guest, _ := env.Get(guestImageKey)
	return ImageReferences{Backend: strings.TrimSpace(backend), Frontend: strings.TrimSpace(frontend), Guest: strings.TrimSpace(guest)}
}

func effectiveImageOptions(env, state, manifest *envFile, options Options) (Options, error) {
	if !options.RegistrySet && strings.TrimSpace(options.ImagePrefix) == "" {
		if registry, ok := state.Get(installerRegistryKey); ok && strings.TrimSpace(registry) != "" {
			options.Registry = strings.TrimSpace(registry)
			options.RegistrySet = true
		} else if prefix, ok := state.Get(installerImagePrefixKey); ok && strings.TrimSpace(prefix) != "" {
			options.ImagePrefix = strings.TrimSpace(prefix)
		} else if _, registryKnown := state.Get(installerRegistryKey); !registryKnown {
			if _, prefixKnown := state.Get(installerImagePrefixKey); !prefixKnown {
				options.ImagePrefix = inferLegacyImagePrefix(state)
			}
		}
	}
	if options.RegistrySet && strings.TrimSpace(options.Registry) != "" {
		if err := validateRegistry(options.Registry); err != nil {
			return options, err
		}
	}
	versions, defaultVersion, err := supportedFrontendVersions(manifest)
	if err != nil {
		return options, err
	}
	if !options.FrontendVersionSet {
		if selected, ok := state.Get(installerFrontendVersionKey); ok && strings.TrimSpace(selected) != "" {
			options.FrontendVersion = strings.TrimSpace(selected)
		} else if selected, ok := env.Get(frontendVersionKey); ok && strings.TrimSpace(selected) != "" {
			options.FrontendVersion = strings.TrimSpace(selected)
		} else {
			options.FrontendVersion = defaultVersion
		}
	}
	if options.FrontendImageSet {
		return options, nil
	}
	if !containsString(versions, options.FrontendVersion) {
		return options, fmt.Errorf("frontend version %q is not supported by release %q; choose one of %s", options.FrontendVersion, options.Version, strings.Join(versions, ", "))
	}
	return options, nil
}

func supportedFrontendVersions(manifest *envFile) ([]string, string, error) {
	defaultVersion, ok := manifest.Get(frontendVersionKey)
	defaultVersion = strings.TrimSpace(defaultVersion)
	if !ok || defaultVersion == "" || !frontendTagPattern.MatchString(defaultVersion) {
		return nil, "", fmt.Errorf("release manifest contains an invalid default frontend version %q", defaultVersion)
	}
	configured, ok := manifest.Get(frontendVersionsKey)
	if !ok || strings.TrimSpace(configured) == "" {
		return []string{defaultVersion}, defaultVersion, nil
	}
	versions := make([]string, 0)
	seen := map[string]bool{}
	for item := range strings.SplitSeq(configured, ",") {
		version := strings.TrimSpace(item)
		if !frontendTagPattern.MatchString(version) {
			return nil, "", fmt.Errorf("release manifest contains an invalid frontend version %q", version)
		}
		if seen[version] {
			return nil, "", fmt.Errorf("release manifest contains duplicate frontend version %q", version)
		}
		seen[version] = true
		versions = append(versions, version)
	}
	if !seen[defaultVersion] {
		return nil, "", fmt.Errorf("release manifest default frontend version %q is not in the supported list", defaultVersion)
	}
	return versions, defaultVersion, nil
}

func replaceImageRegistry(reference, registry string) string {
	first, remainder, found := strings.Cut(reference, "/")
	if !found {
		return strings.TrimSpace(registry) + "/" + reference
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" || strings.HasPrefix(first, "[") {
		return strings.TrimSpace(registry) + "/" + remainder
	}
	return strings.TrimSpace(registry) + "/" + reference
}

func replaceImageTag(reference, version string) string {
	if at := strings.IndexByte(reference, '@'); at >= 0 {
		reference = reference[:at]
	}
	lastSlash := strings.LastIndexByte(reference, '/')
	if colon := strings.LastIndexByte(reference, ':'); colon > lastSlash {
		reference = reference[:colon]
	}
	return reference + ":" + version
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func inferLegacyImagePrefix(state *envFile) string {
	var common string
	for key, basename := range map[string]string{
		backendImageKey:  "agent-compose",
		frontendImageKey: "agent-compose-ui",
		guestImageKey:    "agent-compose-guest",
	} {
		reference, ok := state.Get(key)
		if !ok {
			return ""
		}
		repository := imageRepository(strings.TrimSpace(reference))
		suffix := "/" + basename
		if !strings.HasSuffix(repository, suffix) {
			return ""
		}
		prefix := strings.TrimSuffix(repository, suffix)
		if prefix == "" || (common != "" && prefix != common) {
			return ""
		}
		common = prefix
	}
	return common
}

func imageRepository(reference string) string {
	if at := strings.IndexByte(reference, '@'); at >= 0 {
		return reference[:at]
	}
	lastSlash := strings.LastIndexByte(reference, '/')
	if colon := strings.LastIndexByte(reference, ':'); colon > lastSlash {
		return reference[:colon]
	}
	return reference
}
