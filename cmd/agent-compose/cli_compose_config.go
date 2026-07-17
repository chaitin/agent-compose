package main

import (
	"agent-compose/pkg/compose"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type composeConfigOptions struct {
	Quiet bool
}

func loadNormalizedCompose(cli cliOptions) (string, *compose.NormalizedProjectSpec, error) {
	return loadNormalizedComposeWithOptions(context.Background(), cli, false)
}

func loadNormalizedComposeWithOptions(ctx context.Context, cli cliOptions, resolveScriptURLs bool) (string, *compose.NormalizedProjectSpec, error) {
	composePath, err := resolveComposePath(cli.ComposeFile)
	if err != nil {
		return "", nil, err
	}
	spec, err := compose.ParseFile(composePath)
	if err != nil {
		return "", nil, commandExitError{Code: exitCodeUsage, Err: err}
	}
	if projectName := strings.TrimSpace(cli.ProjectName); projectName != "" {
		spec.Name = projectName
	}
	projectEnv, err := resolveCLIProjectEnv(spec, composePath)
	if err != nil {
		return "", nil, commandExitError{Code: exitCodeUsage, Err: err}
	}
	normalized, err := compose.Normalize(spec, compose.NormalizeOptions{
		ComposePath:       composePath,
		Env:               projectEnv,
		ResolveScriptURLs: resolveScriptURLs,
		Context:           ctx,
	})
	if err != nil {
		return "", nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s: %w", composePath, err)}
	}
	return composePath, normalized, nil
}

func resolveComposePath(pathFlag string) (string, error) {
	pathFlag = strings.TrimSpace(pathFlag)
	if pathFlag != "" {
		abs, err := filepath.Abs(pathFlag)
		if err != nil {
			return pathFlag, fmt.Errorf("resolve --file %q: %w", pathFlag, err)
		}
		return abs, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("find agent-compose.yml or agent-compose.yaml: %w", err)
	}
	ymlPath := filepath.Join(wd, "agent-compose.yml")
	yamlPath := filepath.Join(wd, "agent-compose.yaml")
	ymlExists, err := fileExists(ymlPath)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", ymlPath, err)
	}
	yamlExists, err := fileExists(yamlPath)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", yamlPath, err)
	}
	switch {
	case ymlExists && yamlExists:
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("both %s and %s exist; use --file to choose one", ymlPath, yamlPath)}
	case ymlExists:
		return ymlPath, nil
	case yamlExists:
		return yamlPath, nil
	default:
		return ymlPath, nil
	}
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
