package sandboxstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/compose"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/volumes"
	"github.com/chaitin/agent-compose/pkg/workspaces"

	"github.com/google/uuid"
)

func cloneSandboxWorkspace(item *SandboxWorkspace) *SandboxWorkspace {
	if item == nil {
		return nil
	}
	copy := *item
	return &copy
}

type CreateSandboxOptions struct {
	JupyterEnabled       bool
	JupyterGuestPort     int
	JupyterExpose        bool
	VolumeMounts         []domain.SandboxVolumeMount
	StoppedRuntimePolicy string
	// DriverK8sContext and DriverK8sNamespace carry the agent's `driver.k8s`
	// override into the sandbox's VMState. Empty means the k8s driver falls
	// back to its daemon-wide defaults (see driverpkg k8sRuntime).
	DriverK8sContext   string
	DriverK8sNamespace string
}

//nolint:revive // exported store API with ~75 call sites across many packages (and test/e2e, outside pkg/); the private chain beneath it (sandboxCreateSpec) is already bundled, but changing this signature means updating every caller in one pass rather than incrementally.
func (s *Store) CreateSandbox(ctx context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *SandboxWorkspace, envItems []SandboxEnvVar, tags []SandboxTag) (*Sandbox, error) {
	return s.CreateSandboxWithOptions(ctx, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource, workspace, envItems, tags, CreateSandboxOptions{})
}

//nolint:revive // exported store API with ~13 direct call sites (plus CreateSandbox's own ~75); same reasoning as CreateSandbox above.
func (s *Store) CreateSandboxWithOptions(ctx context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *SandboxWorkspace, envItems []SandboxEnvVar, tags []SandboxTag, options CreateSandboxOptions) (*Sandbox, error) {
	return s.createSandboxWithCacheDependencyLock(ctx, sandboxCreateSpec{
		Title: title, BaseWorkspace: baseWorkspace, Driver: driver, GuestImage: guestImage,
		WorkspaceID: workspaceID, TriggerSource: triggerSource, Workspace: workspace,
		EnvItems: envItems, Tags: tags, Options: options,
	})
}

// sandboxCreateSpec groups the fields the sandbox-creation chain
// (createSandboxWithCacheDependencyLock -> createSandboxWithOptions) needs.
// The exported CreateSandbox/CreateSandboxWithOptions keep their existing
// positional signatures since they have many external callers; only the
// private chain beneath them is bundled here.
type sandboxCreateSpec struct {
	Title         string
	BaseWorkspace string
	Driver        string
	GuestImage    string
	WorkspaceID   string
	TriggerSource string
	Workspace     *SandboxWorkspace
	EnvItems      []SandboxEnvVar
	Tags          []SandboxTag
	Options       CreateSandboxOptions
}

func (s *Store) createSandboxWithCacheDependencyLock(ctx context.Context, spec sandboxCreateSpec) (*Sandbox, error) {
	s.cacheDependencyMu.RLock()
	locker := s.cacheDependencyLocker
	s.cacheDependencyMu.RUnlock()
	if locker == nil {
		return s.createSandboxWithOptions(ctx, spec)
	}
	var sandbox *Sandbox
	err := locker.WithLockContext(ctx, func() error {
		var err error
		sandbox, err = s.createSandboxWithOptions(ctx, spec)
		return err
	})
	return sandbox, err
}

// preparedSandboxCreate is the on-disk-allocated, VM-state-saved Sandbox
// prepareSandboxCreateSession hands back to createSandboxWithOptions, ready
// for proxy-state provisioning and persistence.
type preparedSandboxCreate struct {
	Session    *Sandbox
	SandboxDir string
	GuestImage string
}

func (s *Store) prepareSandboxCreateSession(ctx context.Context, spec sandboxCreateSpec) (preparedSandboxCreate, error) {
	title, baseWorkspace, driver, guestImage, workspaceID, triggerSource, workspace, envItems, tags, options :=
		spec.Title, spec.BaseWorkspace, spec.Driver, spec.GuestImage, spec.WorkspaceID, spec.TriggerSource, spec.Workspace, spec.EnvItems, spec.Tags, spec.Options
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(driver, s.config.RuntimeDriver)
	if err != nil {
		return preparedSandboxCreate{}, err
	}
	if err := workspaces.ValidateWorkspaceRuntimeDriver(workspace, driver); err != nil {
		return preparedSandboxCreate{}, err
	}
	localNow := s.currentTime()
	now := localNow.UTC()
	workspaceID = strings.TrimSpace(workspaceID)
	id := identity.NewRandomID(identity.ResourceSandbox)
	shortID := identity.ShortID(id)
	sandboxDir, err := s.layout.allocate(id, localNow)
	if err != nil {
		return preparedSandboxCreate{}, fmt.Errorf("allocate sandbox directory: %w", err)
	}
	workspaceDir := filepath.Join(sandboxDir, "workspace")
	proxyPath := strings.TrimRight(s.config.JupyterProxyBasePath, "/") + "/" + id + "/lab"
	guestImage = driverpkg.ResolveSandboxGuestImage(guestImage, "", driverpkg.DefaultGuestImageForDriver(s.config, driver))
	stoppedRuntimePolicy, err := compose.NormalizeStoppedRuntimePolicy(options.StoppedRuntimePolicy)
	if err != nil {
		return preparedSandboxCreate{}, fmt.Errorf("create sandbox: %w", err)
	}
	var workspaceProvisioning *domain.SandboxWorkspaceProvisioning
	if workspace != nil || workspaceID != "" {
		workspaceProvisioning = &domain.SandboxWorkspaceProvisioning{
			Version:   domain.SandboxWorkspaceProvisioningVersion,
			Status:    domain.SandboxWorkspaceProvisioningStatusPending,
			UpdatedAt: now,
		}
	}

	dirs := []string{
		sandboxDir,
		filepath.Join(sandboxDir, "context"),
		filepath.Join(sandboxDir, "home"),
		filepath.Join(sandboxDir, "runtime"),
		filepath.Join(sandboxDir, "state"),
		filepath.Join(sandboxDir, "logs"),
		filepath.Join(sandboxDir, "vm"),
		filepath.Join(sandboxDir, "proxy"),
	}
	if workspaceProvisioning == nil {
		dirs = append(dirs, workspaceDir)
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return preparedSandboxCreate{}, fmt.Errorf("create sandbox dir %s: %w", dir, err)
		}
	}

	session := &Sandbox{
		Summary: SandboxSummary{
			ID:            id,
			ShortID:       shortID,
			Title:         strings.TrimSpace(title),
			TriggerSource: sandboxes.NormalizeTriggerSource(triggerSource, tags),
			Driver:        driver,
			VMStatus:      VMStatusPending,
			GuestImage:    guestImage,
			RuntimeRef:    driverpkg.RuntimeRefPrefix(driver) + shortID,
			WorkspacePath: workspaceDir,
			ProxyPath:     proxyPath,
			CreatedAt:     now,
			UpdatedAt:     now,
			Tags:          append([]SandboxTag(nil), tags...),
		},
		BaseWorkspace:         strings.TrimSpace(baseWorkspace),
		WorkspaceID:           workspaceID,
		Workspace:             cloneSandboxWorkspace(workspace),
		WorkspaceProvisioning: workspaceProvisioning,
		StoppedRuntimePolicy:  stoppedRuntimePolicy,
		EnvItems:              append([]SandboxEnvVar(nil), envItems...),
		VolumeMounts:          volumes.NormalizeSandboxMounts(options.VolumeMounts),
	}

	if session.Summary.Title == "" {
		session.Summary.Title = "agent-compose Sandbox " + now.Format("2006-01-02 15:04")
	}

	vmState := VMState{
		Driver:       session.Summary.Driver,
		Mode:         session.Summary.Driver,
		BoxName:      session.Summary.RuntimeRef,
		Image:        guestImage,
		RuntimeHome:  driverpkg.RuntimeHomeForDriver(s.config, driver),
		K8sContext:   options.DriverK8sContext,
		K8sNamespace: options.DriverK8sNamespace,
	}
	if driver == driverpkg.RuntimeDriverBoxlite {
		vmState.Registry = s.config.ImageRegistry
	}
	if err := workspaces.ClaimTransientSnapshot(ctx, s.config, session.Workspace, session.Summary.ID); err != nil {
		return preparedSandboxCreate{}, err
	}
	if err := s.saveVMState(session.Summary.ID, vmState); err != nil {
		return preparedSandboxCreate{}, err
	}
	return preparedSandboxCreate{Session: session, SandboxDir: sandboxDir, GuestImage: guestImage}, nil
}

func (s *Store) createSandboxWithOptions(ctx context.Context, spec sandboxCreateSpec) (*Sandbox, error) {
	prepared, err := s.prepareSandboxCreateSession(ctx, spec)
	if err != nil {
		return nil, err
	}
	session, sandboxDir, guestImage := prepared.Session, prepared.SandboxDir, prepared.GuestImage
	id := session.Summary.ID
	options := spec.Options
	proxyState := ProxyState{
		ProxyPath: session.Summary.ProxyPath,
		GuestHost: "127.0.0.1",
		Enabled:   options.JupyterEnabled,
		Exposed:   options.JupyterExpose,
	}
	if proxyState.Enabled {
		guestPort := options.JupyterGuestPort
		if guestPort == 0 {
			guestPort = s.config.JupyterGuestPort
		}
		if session.Summary.Driver != driverpkg.RuntimeDriverDocker {
			hostPort, err := s.allocateHostPort()
			if err != nil {
				return nil, err
			}
			proxyState.HostPort = hostPort
		}
		proxyState.GuestPort = guestPort
		proxyState.JupyterURL = session.Summary.ProxyPath
		proxyState.Token = uuid.NewString()
	}
	if err := s.SaveProxyState(session.Summary.ID, proxyState); err != nil {
		return nil, err
	}
	if err := s.saveSandbox(session); err != nil {
		return nil, err
	}
	if err := workspaces.CloseTransientSnapshotLease(session.Workspace); err != nil {
		return nil, fmt.Errorf("close prepared workspace lease: %w", err)
	}
	if err := s.saveCells(id, nil); err != nil {
		return nil, err
	}
	if err := s.saveEvents(id, nil); err != nil {
		return nil, err
	}
	if err := sandboxes.WriteOwnershipRecord(s.config.SandboxRoot, sandboxes.OwnershipRecord{
		Version:        sandboxes.OwnershipRecordVersion,
		SandboxID:      session.Summary.ID,
		Driver:         session.Summary.Driver,
		RuntimeID:      session.Summary.RuntimeRef,
		SandboxPath:    sandboxDir,
		LifecycleState: "active",
		OwnedResources: []sandboxes.OwnedResource{
			{Kind: "runtime", Identity: session.Summary.RuntimeRef},
			{Kind: "sandbox-directory", Path: sandboxDir},
		},
		CacheDependencies: []sandboxes.CacheDependency{{Domain: "runtime-image", Identity: guestImage}},
	}); err != nil {
		return nil, fmt.Errorf("write sandbox ownership record: %w", err)
	}

	s.recordIndex(session)
	return session, nil
}

// recordIndex mirrors a committed sandbox summary into the queryable index.
// Request cancellation cannot undo committed metadata, so the cache write uses
// its own bounded context. A failure marks the index dirty for repair before
// the next list query. Callers updating an existing sandbox hold its sandbox
// lock through this call; creation uses a new ID that is not published until
// recordIndex returns. This keeps metadata load and cache upsert ordered with
// RemoveSandbox without holding the global cache repair lock during disk I/O.
