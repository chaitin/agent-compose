package adapters

import (
	"context"
	"strings"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/llms/runtimefacade"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
)

type SandboxDriver struct {
	Config   *appconfig.Config
	Store    *sessionstore.Store
	ConfigDB *configstore.ConfigStore
	Runtimes RuntimeProvider
}

var ensureSandboxLLMFacadeConfig = runtimefacade.EnsureSessionLLMFacadeConfig

func NewSandboxDriver(config *appconfig.Config, store *sessionstore.Store, configDB *configstore.ConfigStore, runtimes RuntimeProvider) *SandboxDriver {
	return &SandboxDriver{Config: config, Store: store, ConfigDB: configDB, Runtimes: runtimes}
}

func (d *SandboxDriver) runtimeForSession(session *domain.Sandbox) (string, SandboxRuntime, error) {
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(session.Summary.Driver, d.Config.RuntimeDriver)
	if err != nil {
		return "", nil, err
	}
	runtime, err := d.Runtimes.ForDriver(driver)
	if err != nil {
		return "", nil, err
	}
	return driver, runtime, nil
}

func (d *SandboxDriver) ValidateSandboxRuntime(session *domain.Sandbox) error {
	_, _, err := d.runtimeForSession(session)
	return err
}

func (d *SandboxDriver) StartSandboxVM(ctx context.Context, session *domain.Sandbox) error {
	if session == nil || session.Summary.VMStatus == domain.VMStatusDeleting {
		return domain.ClassifyError(domain.ErrConflict, "sandbox is being deleted", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, d.Config.SandboxStartTimeout)
	defer cancel()

	driver, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return err
	}

	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	proxyState, err := d.Store.GetProxyState(session.Summary.ID)
	if err != nil {
		return err
	}
	vmState.Driver = driver
	vmState.Mode = driver
	vmState.BoxName = firstNonEmpty(vmState.BoxName, session.Summary.RuntimeRef)
	vmState.RuntimeHome = firstNonEmpty(vmState.RuntimeHome, driverpkg.RuntimeHomeForDriver(d.Config, driver))
	resuming := !vmState.StoppedAt.IsZero()
	if err := d.prepareSandboxStart(ctx, driver, session, &vmState, !resuming); err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}
	// Persist a start-attempt fence before asking the runtime to start. Runtime
	// creation can partially succeed before returning an error; retaining the
	// stop timestamp preserves resume semantics, while the newer attempt time
	// prevents destructive cleanup until another stop is confirmed.
	vmState.StartAttemptedAt = time.Now().UTC()
	if err := d.Store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return err
	}

	info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}

	return d.saveSandboxStartInfo(session, vmState, proxyState, info)
}

func (d *SandboxDriver) saveSandboxStartInfo(session *domain.Sandbox, vmState domain.VMState, proxyState domain.ProxyState, info domain.SandboxVMInfo) error {
	vmState, proxyState = sessions.ApplySessionStartInfo(vmState, proxyState, info, time.Now())
	if err := d.Store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return err
	}
	return d.Store.SaveProxyState(session.Summary.ID, proxyState)
}

func (d *SandboxDriver) StopSandboxVM(ctx context.Context, session *domain.Sandbox) error {
	driver, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, driverpkg.SandboxStopContextTimeout(driver, d.Config.SandboxStopTimeout))
	defer cancel()

	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	missing, err := runtime.StopSandbox(ctx, session, vmState)
	if err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}

	vmState.StoppedAt = time.Now().UTC()
	vmState.LastError = ""
	if missing {
		vmState.BoxID = ""
	}
	return d.Store.SaveVMState(session.Summary.ID, vmState)
}

func (d *SandboxDriver) RemoveSandboxVM(ctx context.Context, session *domain.Sandbox) error {
	driver, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, driverpkg.SandboxStopContextTimeout(driver, d.Config.SandboxStopTimeout))
	defer cancel()
	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	if err := runtime.RemoveSandbox(ctx, session, vmState); err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}
	if d.ConfigDB != nil {
		if err := d.ConfigDB.RevokeLLMFacadeTokensForSandbox(ctx, session.Summary.ID); err != nil {
			vmState.LastError = err.Error()
			_ = d.Store.SaveVMState(session.Summary.ID, vmState)
			return err
		}
	}
	return nil
}

func (d *SandboxDriver) prepareSandboxStart(ctx context.Context, driver string, session *domain.Sandbox, vmState *domain.VMState, refreshRuntimeEnv bool) error {
	prepared, err := driverpkg.PrepareSandboxStart(ctx, d.Config, driver, execution.ToDriverSandbox(session), execution.ToDriverVMState(*vmState))
	if err != nil {
		return err
	}
	*vmState = execution.FromDriverVMState(prepared)
	if !refreshRuntimeEnv {
		return nil
	}
	managedEnv := map[string]string{}
	for _, agent := range []string{"codex", "claude"} {
		agentEnv, err := ensureSandboxLLMFacadeConfig(ctx, d.Config, facadeStoreFor(d.ConfigDB), session, agent, "", "session", "")
		if err != nil {
			if agent == "claude" && runtimefacade.IsOptionalConfigError(err) {
				continue
			}
			return err
		}
		for key, value := range startupFacadeEnv(agent, agentEnv) {
			managedEnv[key] = value
		}
	}
	if len(managedEnv) > 0 {
		session.RuntimeEnvItems = llms.EnvItemsFromMap(managedEnv, false)
	}
	return nil
}

func startupFacadeEnv(agent string, env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(agent), "claude") {
		filtered := make(map[string]string, len(env))
		for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "CLAUDE_MODEL"} {
			if value := strings.TrimSpace(env[key]); value != "" {
				filtered[key] = value
			}
		}
		if len(filtered) == 0 {
			return nil
		}
		return filtered
	}
	return env
}
