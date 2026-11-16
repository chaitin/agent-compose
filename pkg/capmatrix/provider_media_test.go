package capmatrix

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/agentinput"
)

func TestDeclaredProvidersDeclareMediaMatrix(t *testing.T) {
	providers, err := DeclaredProviders()
	if err != nil {
		t.Fatalf("DeclaredProviders() error = %v", err)
	}
	byName := make(map[string]ProviderCapabilities, len(providers))
	for _, provider := range providers {
		byName[provider.Provider] = provider
	}
	images := agentinput.ImageMediaTypes()
	imagesAndPDF := append(agentinput.ImageMediaTypes(), agentinput.MediaTypePDF)
	cases := map[string]struct {
		mediaTypes []string
		ordering   MediaOrdering
	}{
		"codex":    {images, MediaOrderingAggregate},
		"claude":   {imagesAndPDF, MediaOrderingInterleaved},
		"opencode": {imagesAndPDF, MediaOrderingAggregate},
		"pi":       {images, MediaOrderingAggregate},
		"dsh":      {images, MediaOrderingInterleaved},
	}
	for name, want := range cases {
		provider, ok := byName[name]
		if !ok {
			t.Fatalf("provider %q is not declared", name)
		}
		if !provider.Feature(FeatureMediaInput) {
			t.Errorf("provider %q feature %q = false", name, FeatureMediaInput)
		}
		if !reflect.DeepEqual(provider.MediaTypes, want.mediaTypes) {
			t.Errorf("provider %q media types = %v, want %v", name, provider.MediaTypes, want.mediaTypes)
		}
		if provider.MediaOrdering != want.ordering {
			t.Errorf("provider %q ordering = %q, want %q", name, provider.MediaOrdering, want.ordering)
		}
		if provider.MaxAttachmentBytes != agentinput.DefaultMaxPartBytes {
			t.Errorf("provider %q max attachment bytes = %d, want %d", name, provider.MaxAttachmentBytes, agentinput.DefaultMaxPartBytes)
		}
	}
}

func TestDeclaredProvidersFromManifestWithoutMediaInputDisablesMedia(t *testing.T) {
	manifest := manifestWithRequiredProviders()
	providers, err := DeclaredProvidersFromManifest(manifest)
	if err != nil {
		t.Fatalf("DeclaredProvidersFromManifest() error = %v", err)
	}
	for _, provider := range providers {
		if provider.Feature(FeatureMediaInput) {
			t.Errorf("provider %q feature %q = true for a manifest that does not declare it", provider.Provider, FeatureMediaInput)
		}
		if len(provider.MediaTypes) != 0 {
			t.Errorf("provider %q media types = %v, want none", provider.Provider, provider.MediaTypes)
		}
		if provider.MediaOrdering != "" {
			t.Errorf("provider %q ordering = %q, want empty", provider.Provider, provider.MediaOrdering)
		}
	}
}

func TestDeclaredProvidersFromManifestWithMediaInputEnablesStaticMatrix(t *testing.T) {
	manifest := manifestWithRequiredProviders()
	for name, provider := range manifest.Providers {
		provider.Features = append(provider.Features, string(FeatureMediaInput))
		manifest.Providers[name] = provider
	}
	providers, err := DeclaredProvidersFromManifest(manifest)
	if err != nil {
		t.Fatalf("DeclaredProvidersFromManifest() error = %v", err)
	}
	for _, provider := range providers {
		if !provider.Feature(FeatureMediaInput) {
			t.Errorf("provider %q feature %q = false", provider.Provider, FeatureMediaInput)
			continue
		}
		if len(provider.MediaTypes) == 0 {
			t.Errorf("provider %q declares media input with no media types", provider.Provider)
		}
	}
}

func TestProviderCapabilitiesRejectsInconsistentMediaDeclarations(t *testing.T) {
	base := func() ProviderCapabilities {
		provider := ProviderCapabilities{
			Provider:           "codex",
			PreferredProtocols: []string{"responses"},
			MediaTypes:         agentinput.ImageMediaTypes(),
			MediaOrdering:      MediaOrderingAggregate,
			MaxAttachmentBytes: 1024,
		}
		for _, feature := range canonicalFeatures {
			provider.Features = append(provider.Features, ProviderFeature{Feature: feature, Supported: feature == FeatureMediaInput})
		}
		return provider
	}
	cases := map[string]func(ProviderCapabilities) ProviderCapabilities{
		"unknown media type": func(p ProviderCapabilities) ProviderCapabilities {
			p.MediaTypes = append(p.MediaTypes, "image/svg+xml")
			return p
		},
		"duplicate media type": func(p ProviderCapabilities) ProviderCapabilities {
			p.MediaTypes = append(p.MediaTypes, agentinput.MediaTypePNG)
			return p
		},
		"missing ordering": func(p ProviderCapabilities) ProviderCapabilities {
			p.MediaOrdering = ""
			return p
		},
		"media types without feature": func(p ProviderCapabilities) ProviderCapabilities {
			p.Features = nil
			for _, feature := range canonicalFeatures {
				p.Features = append(p.Features, ProviderFeature{Feature: feature, Supported: feature != FeatureMediaInput})
			}
			return p
		},
		"limit without media types": func(p ProviderCapabilities) ProviderCapabilities {
			p.MediaTypes = nil
			p.MediaOrdering = ""
			p.MaxAttachmentBytes = 1024
			return p
		},
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("baseline Validate() = %v, want nil", err)
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if err := mutate(base()).Validate(); !errors.Is(err, ErrInvalidCapability) {
				t.Fatalf("Validate() = %v, want ErrInvalidCapability", err)
			}
		})
	}
}

func TestMediaPolicyNarrowsPartLimit(t *testing.T) {
	provider := ProviderCapabilities{
		Provider:           "codex",
		MediaTypes:         agentinput.ImageMediaTypes(),
		MediaOrdering:      MediaOrderingAggregate,
		MaxAttachmentBytes: 1024,
	}
	policy := provider.MediaPolicy()
	if policy.Limits.MaxPartBytes != 1024 {
		t.Errorf("MaxPartBytes = %d, want the provider limit 1024", policy.Limits.MaxPartBytes)
	}
	if policy.Limits.MaxTurnBytes != agentinput.DefaultMaxTurnBytes {
		t.Errorf("MaxTurnBytes = %d, want the daemon limit", policy.Limits.MaxTurnBytes)
	}
	if !reflect.DeepEqual(policy.MediaTypes, provider.MediaTypes) {
		t.Errorf("MediaTypes = %v, want %v", policy.MediaTypes, provider.MediaTypes)
	}
}

func TestSnapshotPreservesMediaDeclaration(t *testing.T) {
	providers, err := DeclaredProviders()
	if err != nil {
		t.Fatalf("DeclaredProviders() error = %v", err)
	}
	snapshot, err := NewSnapshot(nil, providers, nil, time.Unix(1700000000, 0).UTC())
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("snapshot Validate() error = %v", err)
	}
	frozen := snapshot.Providers()
	if len(frozen) != len(providers) {
		t.Fatalf("frozen providers = %d, want %d", len(frozen), len(providers))
	}
	for index, provider := range frozen {
		if !reflect.DeepEqual(provider.MediaTypes, providers[index].MediaTypes) {
			t.Errorf("provider %q media types = %v, want %v (the snapshot must not drop media facts)",
				provider.Provider, provider.MediaTypes, providers[index].MediaTypes)
		}
		if provider.MediaOrdering != providers[index].MediaOrdering || provider.MaxAttachmentBytes != providers[index].MaxAttachmentBytes {
			t.Errorf("provider %q media ordering/limit dropped by the snapshot", provider.Provider)
		}
	}
}
