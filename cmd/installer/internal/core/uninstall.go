package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func (s Service) uninstall(ctx context.Context, options Options) (Result, error) {
	if err := options.Validate(OperationUninstall); err != nil {
		return Result{}, err
	}
	installDir, err := validateInstallPath(options.InstallDir)
	if err != nil {
		return Result{}, err
	}
	if !regularFile(filepath.Join(installDir, "docker-compose.yml")) || !regularFile(filepath.Join(installDir, ".env")) {
		return Result{}, fmt.Errorf("%s is not a recognized agent-compose installation", installDir)
	}
	if options.Purge {
		if err := validateRegularTarget(filepath.Join(installDir, ".env")); err != nil {
			return Result{}, err
		}
		if err := validateDirectoryTarget(filepath.Join(installDir, "data")); err != nil {
			return Result{}, err
		}
	}
	if _, err := s.checkCompose(ctx); err != nil {
		return Result{}, err
	}
	s.report(EventStep, "Stopping agent-compose")
	if err := s.compose(ctx, installDir, "down", "--remove-orphans"); err != nil {
		return Result{}, fmt.Errorf("stop deployment before uninstall: %w", err)
	}
	s.report(EventStep, "Removing installer-managed files")
	for _, name := range []string{"docker-compose.yml", "docker-compose.kvm.yml", ".installer-state.env"} {
		if err := os.Remove(filepath.Join(installDir, name)); err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("remove %s: %w", name, err)
		}
	}
	if options.Purge {
		if err := purgeInstallationData(installDir); err != nil {
			return Result{}, err
		}
	}
	installerPath := filepath.Join(installDir, "installer")
	if err := os.Remove(installerPath); err != nil && !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("remove installer executable: %w", err)
	}
	retained, err := retainedInstallationFiles(installDir, options.Purge)
	if err != nil {
		return Result{}, err
	}
	for _, name := range retained {
		s.report(EventWarning, "Retained installation path: "+name)
	}
	_ = os.Remove(installDir)
	return Result{InstallDir: installDir, RetainedFiles: retained}, nil
}

func retainedInstallationFiles(installDir string, purged bool) ([]string, error) {
	entries, err := os.ReadDir(installDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list retained installation files: %w", err)
	}
	retained := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !purged && (entry.Name() == ".env" || entry.Name() == "data") {
			continue
		}
		retained = append(retained, entry.Name())
	}
	return retained, nil
}

func purgeInstallationData(installDir string) error {
	for _, path := range []string{filepath.Join(installDir, ".env"), filepath.Join(installDir, "data")} {
		if path == filepath.Join(installDir, "data") {
			if err := validateDirectoryTarget(path); err != nil {
				return err
			}
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove data directory: %w", err)
			}
			continue
		}
		if err := validateRegularTarget(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove environment file: %w", err)
		}
	}
	return nil
}
