package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/compose"

	"github.com/joho/godotenv"
)

func resolveCLIProjectEnv(spec *compose.ProjectSpec, composePath string) (map[string]string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve current directory for project env: %w", err)
	}
	return resolveCLIProjectEnvFromDir(spec, composePath, workingDir)
}

func resolveCLIProjectEnvFromDir(spec *compose.ProjectSpec, composePath, workingDir string) (map[string]string, error) {
	envFiles, err := resolveCLIProjectEnvFiles(spec.EnvFiles, composePath, workingDir)
	if err != nil {
		return nil, err
	}

	values := make(map[string]string)
	for _, path := range envFiles {
		fileValues, err := godotenv.Read(path)
		if err != nil {
			// The parser embeds the unparsed remainder of the file in its
			// message, which for a project env file is where the real provider
			// credentials live.
			return nil, fmt.Errorf("load project env file %s: %w", path, sanitizeDotenvError(err))
		}
		for key, value := range fileValues {
			values[key] = value
		}
	}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	return values, nil
}

func resolveCLIProjectEnvFiles(configured compose.EnvFileSpec, composePath, workingDir string) ([]string, error) {
	projectDir := filepath.Dir(composePath)
	if configured != nil {
		paths := make([]string, 0, len(configured))
		for _, rawPath := range configured {
			path := strings.TrimSpace(rawPath)
			if path == "" {
				return nil, fmt.Errorf("%s: env_file path is empty", composePath)
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(projectDir, path)
			}
			paths = append(paths, filepath.Clean(path))
		}
		return paths, nil
	}

	projectEnv := filepath.Join(projectDir, ".env")
	exists, err := fileExists(projectEnv)
	if err != nil {
		return nil, fmt.Errorf("inspect project env file %s: %w", projectEnv, err)
	}
	if exists {
		return []string{projectEnv}, nil
	}

	workingEnv := filepath.Join(workingDir, ".env")
	if filepath.Clean(workingEnv) == filepath.Clean(projectEnv) {
		return nil, nil
	}
	exists, err = fileExists(workingEnv)
	if err != nil {
		return nil, fmt.Errorf("inspect current directory env file %s: %w", workingEnv, err)
	}
	if exists {
		return []string{workingEnv}, nil
	}
	return nil, nil
}
