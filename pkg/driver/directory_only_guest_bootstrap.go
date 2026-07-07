package driver

import (
	appconfig "agent-compose/pkg/config"
	"fmt"
	"path/filepath"
	"strings"
)

func directoryOnlyGuestSessionBootstrapCommand(config *appconfig.Config) string {
	return directoryOnlyGuestSessionBootstrapCommandForSession(config, nil)
}

func directoryOnlyGuestSessionBootstrapCommandForSession(config *appconfig.Config, session *Session) string {
	appconfig.ApplyDefaultGuestPaths(config)
	workspaceSource := filepath.Clean(filepath.Join(directoryOnlyGuestSessionPath, "workspace"))
	workspaceTarget := filepath.Clean(config.GuestWorkspacePath)
	homeSource := filepath.Clean(filepath.Join(directoryOnlyGuestSessionPath, "home"))
	homeTarget := filepath.Clean(config.GuestHomePath)

	commands := []string{
		"test -d " + shellQuote(workspaceSource) + " || { echo \"missing directory-only workspace " + workspaceSource + "\" >&2; exit 1; }",
		"test -d " + shellQuote(homeSource) + " || { echo \"missing directory-only home " + homeSource + "\" >&2; exit 1; }",
	}
	commands = append(commands, directoryOnlySymlinkCommand(workspaceSource, workspaceTarget, false, true))
	if homeSource != homeTarget {
		commands = append(commands,
			"mkdir -p "+shellQuote(filepath.Dir(homeTarget)),
			"if mountpoint -q "+shellQuote(homeTarget)+"; then echo \"refusing to replace mounted home target "+homeTarget+"\" >&2; exit 1; fi",
			"if [ -L "+shellQuote(homeTarget)+" ]; then rm -f "+shellQuote(homeTarget)+"; mkdir -p "+shellQuote(homeTarget)+"; "+
				"elif [ -e "+shellQuote(homeTarget)+" ]; then "+
				"if [ ! -d "+shellQuote(homeTarget)+" ]; then echo \"refusing to replace non-directory "+homeTarget+"\" >&2; exit 1; fi; "+
				"else mkdir -p "+shellQuote(homeTarget)+"; fi",
			"test -d "+shellQuote(homeTarget)+" || { echo \"directory-only home target is not a directory "+homeTarget+"\" >&2; exit 1; }",
		)
	}
	for _, entry := range runtimeMountEntries(config) {
		if entry.directoryOnlyExposure != directoryOnlyExposureSymlink || !strings.HasPrefix(entry.sessionPath, "home/") {
			continue
		}
		source := filepath.Clean(filepath.Join(directoryOnlyGuestSessionPath, filepath.FromSlash(entry.sessionPath)))
		target := filepath.Clean(entry.guestPath)
		commands = append(commands, directoryOnlySymlinkCommand(source, target, entry.isFile, true))
	}
	commands = append(commands, boxliteVolumeGuestSymlinkCommands(session)...)
	return strings.Join(commands, "; ")
}

func directoryOnlySymlinkCommand(source, target string, isFile bool, replaceExisting bool) string {
	source = filepath.Clean(source)
	target = filepath.Clean(target)
	if source == target {
		return ":"
	}
	sourceQuote := shellQuote(source)
	targetQuote := shellQuote(target)
	sourceParentQuote := shellQuote(filepath.Dir(source))
	targetParentQuote := shellQuote(filepath.Dir(target))
	prepareSource := "if [ -e " + sourceQuote + " ]; then " +
		"if [ ! -d " + sourceQuote + " ]; then echo \"directory-only symlink source is not a directory " + source + "\" >&2; exit 1; fi; " +
		"else mkdir -p " + sourceQuote + "; fi"
	if isFile {
		prepareSource = "mkdir -p " + sourceParentQuote + "; " +
			"if [ -d " + sourceQuote + " ]; then echo \"directory-only symlink source is a directory " + source + "\" >&2; exit 1; fi; " +
			"if [ ! -e " + sourceQuote + " ]; then : > " + sourceQuote + "; fi; " +
			"if [ ! -f " + sourceQuote + " ]; then echo \"directory-only symlink source is not a file " + source + "\" >&2; exit 1; fi"
	}
	prepareTarget := "mkdir -p " + targetParentQuote + "; "
	if replaceExisting {
		prepareTarget += "rm -rf " + targetQuote + "; ln -s " + sourceQuote + " " + targetQuote + "; "
		return prepareSource + "; " + prepareTarget +
			"test \"$(readlink " + targetQuote + ")\" = " + sourceQuote + " || { echo \"directory-only symlink target does not match " + source + "\" >&2; exit 1; }"
	}
	return prepareSource + "; " + prepareTarget +
		"if [ -L " + targetQuote + " ]; then " +
		"if [ \"$(readlink " + targetQuote + ")\" != " + sourceQuote + " ]; then rm -f " + targetQuote + "; ln -s " + sourceQuote + " " + targetQuote + "; fi; " +
		"elif [ -e " + targetQuote + " ]; then echo \"refusing to replace existing directory-only symlink target " + target + "\" >&2; exit 1; " +
		"else ln -s " + sourceQuote + " " + targetQuote + "; fi; " +
		"test \"$(readlink " + targetQuote + ")\" = " + sourceQuote + " || { echo \"directory-only symlink target does not match " + source + "\" >&2; exit 1; }"
}

func directoryOnlyGuestSessionBootstrapExecSpec(config *appconfig.Config) ExecSpec {
	return directoryOnlyGuestSessionBootstrapExecSpecForSession(config, nil)
}

func directoryOnlyGuestSessionBootstrapExecSpecForSession(config *appconfig.Config, session *Session) ExecSpec {
	return ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", directoryOnlyGuestSessionBootstrapCommandForSession(config, session)},
		Cwd:     "/",
	}
}

func executeUserCommandAfterBootstrap(bootstrap func() error, execute func() (ExecResult, error)) (ExecResult, error) {
	if err := bootstrap(); err != nil {
		return ExecResult{}, err
	}
	return execute()
}

func formatDirectoryOnlyGuestSessionBootstrapError(driver, sessionID, runtimeID string, result ExecResult, execErr error) error {
	parts := []string{"directory-only guest bootstrap failed", "driver=" + strings.TrimSpace(driver)}
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		parts = append(parts, "session_id="+sessionID)
	}
	if runtimeID = strings.TrimSpace(runtimeID); runtimeID != "" {
		parts = append(parts, "runtime_id="+runtimeID)
	}
	if result.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit_code=%d", result.ExitCode))
	}
	if stdout := summarizeBootstrapOutput(result.Stdout); stdout != "" {
		parts = append(parts, "stdout="+stdout)
	}
	if stderr := summarizeBootstrapOutput(result.Stderr); stderr != "" {
		parts = append(parts, "stderr="+stderr)
	}
	message := strings.Join(parts, " ")
	if execErr != nil {
		return fmt.Errorf("%s: %w", message, execErr)
	}
	return fmt.Errorf("%s", message)
}

func summarizeBootstrapOutput(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	const limit = 1024
	if len(output) <= limit {
		return fmt.Sprintf("%q", output)
	}
	return fmt.Sprintf("%q", output[:limit]+"...")
}
