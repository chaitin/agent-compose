package compose

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/chaitin/agent-compose/pkg/sources"
)

func normalizeSchedulerSpec(path string, scheduler *SchedulerSpec, options NormalizeOptions) (*NormalizedSchedulerSpec, error) {
	if scheduler == nil {
		return nil, nil
	}

	enabled := true
	if scheduler.Enabled != nil {
		enabled = *scheduler.Enabled
	}
	script := strings.TrimSpace(scheduler.Script.Inline)
	scriptSource := scheduler.Script.Source.Normalized()
	if scheduler.Script.Inline != "" && scriptSource.HasContent() {
		return nil, &ValidationError{Path: path + ".script", Message: "script must use exactly one of inline content or a provider"}
	}
	if scriptSource.HasContent() {
		var err error
		scriptSource, err = normalizeSchedulerScriptSource(path+".script", scriptSource, options)
		if err != nil {
			return nil, err
		}
	}
	if (script != "" || scriptSource.HasContent()) && len(scheduler.Triggers) > 0 {
		return nil, &ValidationError{Path: path, Message: "scheduler script and triggers are mutually exclusive"}
	}
	sandboxPolicy, err := normalizeSandboxPolicy(path+".sandbox_policy", scheduler.SandboxPolicy, "new")
	if err != nil {
		return nil, err
	}
	concurrencyPolicy, err := normalizeSchedulerConcurrencyPolicy(path+".concurrency_policy", scheduler.ConcurrencyPolicy)
	if err != nil {
		return nil, err
	}
	runTimeout := strings.TrimSpace(scheduler.RunTimeout)
	if runTimeout != "" {
		if err := validatePositiveDuration(path+".run_timeout", runTimeout); err != nil {
			return nil, err
		}
	}
	normalized := &NormalizedSchedulerSpec{
		Enabled:           enabled,
		SandboxPolicy:     sandboxPolicy,
		ConcurrencyPolicy: concurrencyPolicy,
		RunTimeout:        runTimeout,
		DisplayName:       strings.TrimSpace(scheduler.DisplayName),
		Description:       strings.TrimSpace(scheduler.Description),
		Model:             strings.TrimSpace(scheduler.Model),
		Script:            script,
	}
	if scriptSource.HasContent() {
		if !options.ResolveScriptURLs {
			normalized.scriptSource = &scriptSource
		} else {
			resolver := options.ScriptSourceResolver
			if resolver == nil {
				resolver = NewDefaultScriptSourceResolver(options.Env)
			}
			ctx := options.Context
			if ctx == nil {
				ctx = context.Background()
			}
			content, err := resolver.Resolve(ctx, scriptSource)
			if err != nil {
				return nil, &ValidationError{Path: path + ".script", Message: err.Error()}
			}
			if !utf8.Valid(content) {
				return nil, &ValidationError{Path: path + ".script", Message: "script content must be valid UTF-8"}
			}
			text := strings.TrimPrefix(string(content), "\ufeff")
			normalized.Script = strings.TrimSpace(text)
			if normalized.Script == "" {
				return nil, &ValidationError{Path: path + ".script", Message: "script content is empty"}
			}
		}
	}
	seenTriggerNames := make(map[string]struct{}, len(scheduler.Triggers))
	for i, trigger := range scheduler.Triggers {
		triggerPath := fmt.Sprintf("%s.triggers[%d]", path, i)
		normalizedTrigger, err := normalizeTriggerSpec(triggerPath, trigger)
		if err != nil {
			return nil, err
		}
		if normalizedTrigger.Name != "" {
			if _, ok := seenTriggerNames[normalizedTrigger.Name]; ok {
				return nil, &ValidationError{Path: triggerPath + ".name", Message: fmt.Sprintf("duplicate scheduler trigger name %q", normalizedTrigger.Name)}
			}
			seenTriggerNames[normalizedTrigger.Name] = struct{}{}
		}
		normalized.Triggers = append(normalized.Triggers, normalizedTrigger)
	}
	return normalized, nil
}

func normalizeSchedulerScriptSource(path string, source sources.Source, options NormalizeOptions) (sources.Source, error) {
	var err error
	source, err = normalizeSourceCredentials(path, source, options)
	if err != nil {
		return sources.Source{}, err
	}
	if source.Format != "" {
		return sources.Source{}, &ValidationError{Path: path + ".format", Message: "scheduler script does not support format"}
	}
	switch source.Provider {
	case sources.ProviderFile:
		if source.Path == "" {
			return sources.Source{}, &ValidationError{Path: path + ".path", Message: "file script path is required"}
		}
		if source.URL != "" || source.Ref != "" || source.HasAuthentication() {
			return sources.Source{}, &ValidationError{Path: path, Message: "file script only supports path"}
		}
		location, err := normalizeScriptSourceURL(source.Path, options)
		if err != nil {
			return sources.Source{}, &ValidationError{Path: path + ".path", Message: err.Error()}
		}
		parsed, _ := url.Parse(location)
		if parsed.Scheme != "" && parsed.Scheme != "file" {
			return sources.Source{}, &ValidationError{Path: path + ".path", Message: "file script path must use a local path or file URL"}
		}
		source.Path = location
	case sources.ProviderHTTP:
		if source.URL == "" {
			return sources.Source{}, &ValidationError{Path: path + ".url", Message: "http script url is required"}
		}
		if source.Path != "" || source.Ref != "" {
			return sources.Source{}, &ValidationError{Path: path, Message: "http script only supports url and authentication"}
		}
		location, err := normalizeScriptSourceURL(source.URL, options)
		if err != nil {
			return sources.Source{}, &ValidationError{Path: path + ".url", Message: err.Error()}
		}
		parsed, _ := url.Parse(location)
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return sources.Source{}, &ValidationError{Path: path + ".url", Message: "http script url must use http or https"}
		}
		source.URL = location
	case sources.ProviderGit:
		if source.URL == "" {
			return sources.Source{}, &ValidationError{Path: path + ".url", Message: "git script url is required"}
		}
		if source.Path == "" {
			return sources.Source{}, &ValidationError{Path: path + ".path", Message: "git script path is required"}
		}
		clean := filepath.Clean(filepath.FromSlash(source.Path))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return sources.Source{}, &ValidationError{Path: path + ".path", Message: "git script path must stay within the repository"}
		}
		source.Path = filepath.ToSlash(clean)
	default:
		return sources.Source{}, &ValidationError{Path: path + ".provider", Message: fmt.Sprintf("scheduler script provider %q is not supported", source.Provider)}
	}
	return source, nil
}
