package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var managedFiles = []string{
	"docker-compose.yml",
	"docker-compose.kvm.yml",
	"installer",
	".env",
	".installer-state.env",
}

type Service struct {
	HTTPClient *http.Client
	Runner     CommandRunner
	Reporter   Reporter
}

// uiProfile gates the frontend service in docker-compose.yml. Compose reads
// COMPOSE_PROFILES from the project .env, so persisting it there keeps a later
// manual `docker compose up -d` consistent with the installed selection.
const uiProfile = "with-ui"

type Result struct {
	InstallDir        string
	URL               string
	Username          string
	GeneratedPassword string
	DataDir           string
	ComposeFiles      string
	ComposeProfiles   string
	GuestImage        string
	RetainedFiles     []string
}

// WithUI reports whether the installation published the web frontend.
// COMPOSE_PROFILES is a comma-separated list, and an existing one is preserved
// verbatim, so the installer's own profile can sit alongside others an operator
// added by hand.
func (r Result) WithUI() bool {
	for profile := range strings.SplitSeq(r.ComposeProfiles, ",") {
		if strings.TrimSpace(profile) == uiProfile {
			return true
		}
	}
	return false
}

func (s Service) Apply(ctx context.Context, operation Operation, options Options) (Result, error) {
	if operation == OperationUninstall {
		return s.uninstall(ctx, options)
	}
	return s.installOrUpgrade(ctx, operation, options)
}

func (s Service) report(kind EventKind, message string) {
	reporter := s.Reporter
	if reporter == nil {
		reporter = discardReporter{}
	}
	reporter.Report(Event{Kind: kind, Message: message})
}

func (s Service) runner() CommandRunner {
	if s.Runner != nil {
		return s.Runner
	}
	return ExecRunner{Output: io.Discard}
}

func (s Service) checkCompose(ctx context.Context) error {
	s.report(EventStep, "Checking Docker and Docker Compose")
	if err := s.runner().Run(ctx, "", "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("docker is unavailable; install Docker Engine and retry: %w", err)
	}
	if err := s.runner().Run(ctx, "", "docker", "compose", "version", "--short"); err != nil {
		return fmt.Errorf("docker compose v2 is unavailable; install the Docker Compose plugin and retry: %w", err)
	}
	return nil
}

func randomHex(bytes int) (string, error) {
	data := make([]byte, bytes)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func randomPassword() (string, error) {
	data := make([]byte, 18)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func (s Service) installOrUpgrade(ctx context.Context, operation Operation, options Options) (result Result, resultErr error) {
	if err := options.Validate(operation); err != nil {
		return result, err
	}
	installDir, err := validateInstallPath(options.InstallDir)
	if err != nil {
		return result, err
	}
	options.InstallDir = installDir
	if err := s.checkCompose(ctx); err != nil {
		return result, err
	}
	s.report(EventStep, "Loading deployment bundle")
	loadedBundle, err := (bundleLoader{client: s.HTTPClient}).Load(ctx, options)
	if err != nil {
		return result, err
	}
	defer loadedBundle.Close()

	plan, err := prepareInstallPlan(operation, options, loadedBundle)
	if err != nil {
		return result, err
	}
	defer plan.Close()
	result = plan.result
	// The port is only published by the frontend, so an explicit port without
	// the UI would otherwise be accepted and then quietly do nothing.
	if options.PortSet && !result.WithUI() {
		s.report(EventWarning, "the requested port is unused because the web UI is not installed; pass --with-ui to publish it")
	}

	tx, err := beginInstallTransaction(options.InstallDir, plan.dataDir)
	if err != nil {
		return result, err
	}
	defer func() {
		if resultErr == nil {
			tx.Commit()
			return
		}
		if tx.upAttempted && !tx.previousInstall {
			if cleanupErr := s.compose(context.Background(), options.InstallDir, "down", "--remove-orphans"); cleanupErr != nil {
				// Keep the candidate project metadata so the operator can retry cleanup.
				tx.Commit()
				message := fmt.Sprintf("cleanup incomplete; deployment files retained in %s", options.InstallDir)
				s.report(EventWarning, message)
				resultErr = errors.Join(resultErr, fmt.Errorf("%s: %w", message, cleanupErr))
				return
			}
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("rollback installation: %w", rollbackErr))
			return
		}
		if tx.upAttempted && tx.previousInstall {
			if recoveryErr := s.compose(context.Background(), options.InstallDir, "up", "-d"); recoveryErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("restore previous deployment: %w", recoveryErr))
			}
		}
	}()

	s.report(EventStep, "Writing deployment configuration")
	if err := plan.Promote(options.InstallDir); err != nil {
		return result, err
	}
	if err := os.MkdirAll(plan.dataDir, 0o755); err != nil {
		return result, fmt.Errorf("create data directory: %w", err)
	}
	if err := s.compose(ctx, options.InstallDir, "config", "--quiet"); err != nil {
		return result, fmt.Errorf("validate Docker Compose project: %w", err)
	}
	if options.NoStart {
		s.report(EventInfo, "Deployment files prepared; startup skipped")
		return result, nil
	}
	s.report(EventStep, "Pulling images")
	if err := s.compose(ctx, options.InstallDir, "pull"); err != nil {
		return result, fmt.Errorf("pull deployment images: %w", err)
	}
	s.pullGuestImage(ctx, options, result.GuestImage)
	tx.upAttempted = true
	s.report(EventStep, "Starting agent-compose")
	if err := s.compose(ctx, options.InstallDir, "up", "-d"); err != nil {
		return result, fmt.Errorf("start deployment: %w", err)
	}
	return result, nil
}

// pullGuestImage fetches the sandbox guest image ahead of time. Compose never
// pulls it because DEFAULT_IMAGE is a daemon setting rather than a service
// image, so the first agent run would otherwise stall on a large download and
// surface registry problems far from the installation that caused them.
//
// A failure is reported but does not abort: the deployment itself is healthy,
// and rolling it back over a deferred download would cost more than it saves.
func (s Service) pullGuestImage(ctx context.Context, options Options, image string) {
	if options.SkipGuestPull || image == "" {
		return
	}
	s.report(EventStep, "Pulling guest image "+image)
	if err := s.runner().Run(ctx, options.InstallDir, "docker", "pull", image); err != nil {
		s.report(EventWarning, fmt.Sprintf("guest image %s was not pulled; the first sandbox will download it: %v", image, err))
	}
}

// compose deliberately passes no --progress flag. It was once forced to plain
// so the TUI would not have to cope with docker's cursor-driven redraws, but
// the flag only exists on newer Compose plugins and made pull fail outright on
// older ones. The log now merges per-layer progress by itself, so accepting
// whatever format Compose emits costs nothing.
func (s Service) compose(ctx context.Context, dir string, args ...string) error {
	return s.runner().Run(ctx, dir, "docker", append([]string{"compose"}, args...)...)
}

type installPlan struct {
	workDir string
	dataDir string
	files   map[string]os.FileMode
	result  Result
}

func (p *installPlan) Close() { _ = os.RemoveAll(p.workDir) }

func (p *installPlan) Promote(installDir string) error {
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create install directory: %w", err)
	}
	for name, mode := range p.files {
		if err := atomicCopy(filepath.Join(p.workDir, name), filepath.Join(installDir, name), mode); err != nil {
			return err
		}
	}
	return nil
}

func prepareInstallPlan(operation Operation, options Options, source *bundle) (*installPlan, error) {
	for _, name := range managedFiles {
		if err := validateRegularTarget(filepath.Join(options.InstallDir, name)); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"data", filepath.Join("data", "agent-compose")} {
		if err := validateDirectoryTarget(filepath.Join(options.InstallDir, name)); err != nil {
			return nil, err
		}
	}
	workDir, err := os.MkdirTemp("", "agent-compose-installer-plan-*")
	if err != nil {
		return nil, err
	}
	plan := &installPlan{workDir: workDir, files: map[string]os.FileMode{}}
	fail := func(err error) (*installPlan, error) { plan.Close(); return nil, err }
	if err := copyFile(filepath.Join(source.Dir, "docker-compose.yml"), filepath.Join(workDir, "docker-compose.yml"), 0o644); err != nil {
		return fail(err)
	}
	plan.files["docker-compose.yml"] = 0o644
	kvmBundlePath := filepath.Join(source.Dir, "docker-compose.kvm.yml")
	kvmInstalledPath := filepath.Join(options.InstallDir, "docker-compose.kvm.yml")
	if regularFile(kvmBundlePath) {
		if err := copyFile(kvmBundlePath, filepath.Join(workDir, "docker-compose.kvm.yml"), 0o644); err != nil {
			return fail(err)
		}
		plan.files["docker-compose.kvm.yml"] = 0o644
	} else if regularFile(kvmInstalledPath) {
		if err := copyFile(kvmInstalledPath, filepath.Join(workDir, "docker-compose.kvm.yml"), 0o644); err != nil {
			return fail(err)
		}
		plan.files["docker-compose.kvm.yml"] = 0o644
	}

	envPath := filepath.Join(options.InstallDir, ".env")
	envData, envExists, err := readOptionalFile(envPath)
	if err != nil {
		return fail(err)
	}
	if !envExists {
		envData, err = os.ReadFile(filepath.Join(source.Dir, ".env.example"))
		if err != nil {
			return fail(err)
		}
	}
	env := parseEnvFile(envData)
	stateData, _, err := readOptionalFile(filepath.Join(options.InstallDir, ".installer-state.env"))
	if err != nil {
		return fail(err)
	}
	state := parseEnvFile(stateData)
	password, err := ensureSecrets(env)
	if err != nil {
		return fail(err)
	}
	mode := "set-missing"
	if !envExists {
		mode = "install"
	} else if operation == OperationUpgrade {
		mode = "upgrade"
	}
	if err := applyImageReferences(env, state, source.Manifest, options, mode); err != nil {
		return fail(err)
	}
	effectivePort, err := applyHTTPPort(env, envExists, options)
	if err != nil {
		return fail(err)
	}
	dataRelative, dataDir, err := selectDataDir(options.InstallDir, env, envExists)
	if err != nil {
		return fail(err)
	}
	plan.dataDir = dataDir
	if err := env.Set("AGENT_COMPOSE_DATA_DIR", dataRelative); err != nil {
		return fail(err)
	}
	composeFiles, err := selectComposeFiles(options, env, regularFile(filepath.Join(workDir, "docker-compose.kvm.yml")))
	if err != nil {
		return fail(err)
	}
	composeProfiles, err := selectComposeProfiles(env, envExists, options)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".env"), env.Bytes(), 0o600); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".installer-state.env"), state.Bytes(), 0o600); err != nil {
		return fail(err)
	}
	plan.files[".env"] = 0o600
	plan.files[".installer-state.env"] = 0o600
	if options.InstallerPath != "" {
		if err := copyFile(options.InstallerPath, filepath.Join(workDir, "installer"), 0o755); err != nil {
			return fail(fmt.Errorf("stage installer executable: %w", err))
		}
		plan.files["installer"] = 0o755
	}
	username, ok := env.Get("AUTH_USERNAME")
	if !ok || username == "" {
		username = "admin"
	}
	guestImage, _ := env.Get("DEFAULT_IMAGE")
	plan.result = Result{
		InstallDir: options.InstallDir, Username: username, GeneratedPassword: password,
		DataDir: dataDir, ComposeFiles: composeFiles, ComposeProfiles: composeProfiles,
		GuestImage: strings.TrimSpace(guestImage),
	}
	// Without the frontend nothing listens on the published port, so reporting a
	// URL would send the operator to a dead address.
	if plan.result.WithUI() {
		plan.result.URL = httpURL(hostAddress(), effectivePort)
	}
	return plan, nil
}

func selectComposeProfiles(env *envFile, envExists bool, options Options) (string, error) {
	configured, ok := env.Get("COMPOSE_PROFILES")
	if envExists && !options.WithUISet && ok {
		return strings.TrimSpace(configured), nil
	}
	value := ""
	if options.WithUI {
		value = uiProfile
	}
	return value, env.Set("COMPOSE_PROFILES", value)
}

func applyHTTPPort(env *envFile, envExists bool, options Options) (int, error) {
	configured, _ := env.Get("AGENT_COMPOSE_HTTP_PORT")
	if envExists && !options.PortSet && strings.TrimSpace(configured) != "" {
		port, err := ParsePort(configured)
		if err != nil {
			return 0, fmt.Errorf("existing AGENT_COMPOSE_HTTP_PORT is invalid: %w", err)
		}
		return port, nil
	}
	if err := env.Set("AGENT_COMPOSE_HTTP_PORT", strconv.Itoa(options.Port)); err != nil {
		return 0, err
	}
	return options.Port, nil
}

func ensureSecrets(env *envFile) (string, error) {
	secret, _ := env.Get("AUTH_SECRET")
	if secret == "" {
		generated, err := randomHex(32)
		if err != nil {
			return "", err
		}
		if err := env.Set("AUTH_SECRET", generated); err != nil {
			return "", err
		}
	}
	password, _ := env.Get("AUTH_PASSWORD")
	if password != "" {
		return "", nil
	}
	password, err := randomPassword()
	if err != nil {
		return "", err
	}
	if err := env.Set("AUTH_PASSWORD", password); err != nil {
		return "", err
	}
	return password, nil
}

func applyImageReferences(env, state, manifest *envFile, options Options, mode string) error {
	desired := map[string]string{}
	if options.ImagePrefix != "" {
		version := options.Version
		if image, ok := manifest.Get("AGENT_COMPOSE_IMAGE"); ok {
			if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
				version = image[colon+1:]
			}
		}
		frontendVersion := options.FrontendVersion
		if frontendVersion == "" {
			frontendVersion = DefaultVersion
		}
		desired["AGENT_COMPOSE_IMAGE"] = options.ImagePrefix + "/agent-compose:" + version
		desired["AGENT_COMPOSE_FRONTEND_VERSION"] = frontendVersion
		desired["AGENT_COMPOSE_FRONTEND_IMAGE"] = options.ImagePrefix + "/agent-compose-ui:" + frontendVersion
		desired["DEFAULT_IMAGE"] = options.ImagePrefix + "/agent-compose-guest:" + version
	} else {
		for _, key := range []string{"AGENT_COMPOSE_IMAGE", "AGENT_COMPOSE_FRONTEND_VERSION", "AGENT_COMPOSE_FRONTEND_IMAGE", "DEFAULT_IMAGE"} {
			if value, ok := manifest.Get(key); ok {
				desired[key] = value
			}
		}
	}
	for key, value := range desired {
		current, currentExists := env.Get(key)
		managed, managedExists := state.Get(key)
		shouldSet := mode == "install" || !currentExists || current == ""
		if mode == "upgrade" && managedExists && current == managed {
			shouldSet = true
		}
		if !shouldSet {
			continue
		}
		if err := env.Set(key, value); err != nil {
			return err
		}
		if err := state.Set(key, value); err != nil {
			return err
		}
	}
	return state.Set("INSTALLER_PAYLOAD_VERSION", "1")
}

func selectDataDir(installDir string, env *envFile, existingEnv bool) (string, string, error) {
	if configured, ok := env.Get("AGENT_COMPOSE_DATA_DIR"); existingEnv && ok {
		switch configured {
		case "./data", "data":
			return "./data", filepath.Join(installDir, "data"), nil
		case "./data/agent-compose", "data/agent-compose":
			return "./data/agent-compose", filepath.Join(installDir, "data", "agent-compose"), nil
		default:
			return "", "", fmt.Errorf("AGENT_COMPOSE_DATA_DIR must be ./data or ./data/agent-compose when using the installer")
		}
	}
	currentDB := regularFile(filepath.Join(installDir, "data", "data.db"))
	legacyDB := regularFile(filepath.Join(installDir, "data", "agent-compose", "data.db"))
	if currentDB && legacyDB {
		return "", "", fmt.Errorf("both current and legacy data stores exist; set AGENT_COMPOSE_DATA_DIR before retrying")
	}
	if legacyDB {
		return "./data/agent-compose", filepath.Join(installDir, "data", "agent-compose"), nil
	}
	return "./data", filepath.Join(installDir, "data"), nil
}

func selectComposeFiles(options Options, env *envFile, overlayAvailable bool) (string, error) {
	if existing, ok := env.Get("COMPOSE_FILE"); ok {
		return existing, nil
	}
	if _, ok := env.Get("COMPOSE_PATH_SEPARATOR"); ok {
		return "", fmt.Errorf("existing COMPOSE_PATH_SEPARATOR requires an explicit COMPOSE_FILE")
	}
	if _, err := os.Stat(options.KVMPath); err == nil {
		if !overlayAvailable {
			return "", fmt.Errorf("KVM detected at %s but docker-compose.kvm.yml is unavailable", options.KVMPath)
		}
		value := "docker-compose.yml:docker-compose.kvm.yml"
		return value, env.Set("COMPOSE_FILE", value)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect KVM device: %w", err)
	}
	value := "docker-compose.yml"
	return value, env.Set("COMPOSE_FILE", value)
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		_ = input.Close()
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func atomicCopy(source, destination string, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".installer-tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", destination, err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	input, err := os.Open(source)
	if err != nil {
		return errors.Join(err, temporary.Close())
	}
	_, copyErr := io.Copy(temporary, input)
	closeInputErr := input.Close()
	chmodErr := temporary.Chmod(mode)
	closeErr := temporary.Close()
	if err := errors.Join(copyErr, closeInputErr, chmodErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("replace %s: %w", destination, err)
	}
	return nil
}
