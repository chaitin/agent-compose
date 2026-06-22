<script lang="ts">
  import { onDestroy, onMount } from 'svelte';

  import {
    createWorkspacePreset,
    deleteWebhookSource,
    deleteWorkspacePreset,
    getCapabilityCatalog,
    getCapabilityGatewayConfig,
    getCapabilityStatus,
    listCapabilitySets,
    listEnvItems,
    listWebhookSources,
    listWorkspaceFiles,
    listWorkspacePresets,
    saveWebhookSource,
    updateCapabilityGatewayConfig,
    updateEnvItems,
    updateWorkspacePreset,
    uploadWorkspaceArchive,
    type CapabilityMethodInfo,
    type EnvItem,
    type WebhookSource,
    type WorkspaceFileEntry,
    type WorkspacePreset,
  } from '../api/config';
  import { getAuthStatus, type AuthStatus } from '../api/auth';
  import { formatBeijingTime } from '../time';
  import { currentQueryParams, updateQueryParams } from '../url';

  let envItems: EnvItem[] = [];
  let workspaces: WorkspacePreset[] = [];
  let webhookSources: WebhookSource[] = [];
  let gatewayConfigured = false;
  let gatewayOk = false;
  let gatewayStatusText = '';
  let capabilityCount = 0;
  let capabilityRuntimeConfigured = false;
  let capabilityProxyListenConfigured = false;
  let capabilityProxyTargetConfigured = false;
  let lastSyncedAt = '';
  let gatewayAddr = '';
  let savedGatewayAddr = '';
  let gatewayToken = ''; // write-only; blank keeps the existing token
  let gatewayTokenSet = false;
  let gatewaySaving = false;

  type CapabilitySetView = {
    id: string;
    name: string;
    description: string;
    enabled: boolean;
    methods: CapabilityMethodInfo[];
  };
  let capabilitySets: CapabilitySetView[] = [];
  // System runtime image is fixed for now (managed at deploy time); shown
  // read-only. Matches AgentsPage's defaultGuestImage.
  const runtimeImage = 'agent-compose-guest:latest';
  let authStatus: AuthStatus | null = null;

  type SettingsSection = 'workspace' | 'env' | 'runtime' | 'auth' | 'gateway' | 'webhook';
  const settingsSections: Array<{ key: SettingsSection; label: string }> = [
    { key: 'gateway', label: '能力接入网关' },
    { key: 'webhook', label: 'Webhook 配置' },
    { key: 'runtime', label: '运行环境' },
    { key: 'workspace', label: '智能体工作文件' },
    { key: 'env', label: '全局环境变量' },
    { key: 'auth', label: '登录控制' },
  ];
  let activeSection: SettingsSection = 'gateway';

  function sectionFromValue(value: string | null): SettingsSection {
    return settingsSections.some((section) => section.key === value)
      ? (value as SettingsSection)
      : 'gateway';
  }

  function syncFromURL(): void {
    activeSection = sectionFromValue(currentQueryParams().get('section'));
  }

  function selectSection(key: SettingsSection): void {
    activeSection = key;
    updateQueryParams({ section: key === 'gateway' ? null : key });
  }

  // Auto-dismiss the success toast so it doesn't linger after a save.
  let messageTimer: ReturnType<typeof setTimeout> | null = null;
  let copiedWorkspaceId = '';
  let copyTimer: ReturnType<typeof setTimeout> | null = null;
  $: if (message) {
    if (messageTimer) clearTimeout(messageTimer);
    messageTimer = setTimeout(() => { message = ''; }, 2600);
  }
  onDestroy(() => {
    if (messageTimer) clearTimeout(messageTimer);
    if (copyTimer) clearTimeout(copyTimer);
  });
  let newEnvName = '';
  let newEnvValue = '';
  let newEnvSecret = false;
  let error = '';
  let message = '';
  let savingEnv = false;
  let envDirty = false;
  let workspaceDraft: WorkspacePreset | null = null;
  let workspaceName = '';
  let workspaceType = 'git';
  let workspaceConfigMode: 'form' | 'json' = 'form';
  let workspaceGitUrl = '';
  let workspaceGitBranch = 'main';
  let workspaceGitCredential = '';
  let workspaceConfigJson = '{}';
  let workspaceComment = '';
  let workspaceFiles: WorkspaceFileEntry[] = [];
  let workspaceFileLoading = false;
  let workspaceUploading = false;
  let webhookSaving = false;
  let webhookEditorOpen = false;
  let deletingWebhookSourceId = '';
  let webhookForm = {
    id: '',
    name: '',
    enabled: true,
    provider: '',
    topicPrefix: 'webhook.',
    token: '',
    bodyLimitBytes: '',
  };

  const defaultGitBranch = 'main';
  const gitConfigJsonPlaceholder = `{
  "url": "https://github.com/org/repo.git",
  "branch": "main",
  "commit": "",
  "credential": "",
  "username": "",
  "password": "",
  "path": ""
}`;

  type GitWorkspaceConfigDraft = {
    url?: unknown;
    repo_url?: unknown;
    repoUrl?: unknown;
    branch?: unknown;
    commit?: unknown;
    credential?: unknown;
    username?: unknown;
    password?: unknown;
    path?: unknown;
  };

  onMount(() => {
    syncFromURL();
    void load();
    void loadAuthStatus();
    window.addEventListener('popstate', syncFromURL);
    return () => {
      window.removeEventListener('popstate', syncFromURL);
    };
  });

  async function load(): Promise<void> {
    error = '';
    try {
      const [items, presets, sources, gateway] = await Promise.all([
        listEnvItems(),
        listWorkspacePresets(),
        listWebhookSources(),
        getCapabilityGatewayConfig(),
      ]);
      envItems = items;
      envDirty = false;
      workspaces = presets;
      webhookSources = sources;
      gatewayAddr = gateway.addr;
      savedGatewayAddr = gateway.addr;
      gatewayTokenSet = gateway.tokenSet;
      gatewayToken = '';
      await refreshGatewayStatus();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  // refreshGatewayStatus does one live sync: probe status + load capability
  // sets. It is triggered on page load, on save, and by the 测试连接 button —
  // there is no background polling, so 最近同步 reflects the last manual sync.
  async function refreshGatewayStatus(): Promise<void> {
    try {
      const status = await getCapabilityStatus();
      gatewayConfigured = status.configured;
      gatewayOk = status.ok;
      gatewayStatusText = status.error || status.status;
      capabilityCount = status.serviceCount;
      capabilityRuntimeConfigured = status.runtimeConfigured;
      capabilityProxyListenConfigured = status.proxyListenConfigured;
      capabilityProxyTargetConfigured = status.proxyTargetConfigured;
    } catch (err) {
      gatewayConfigured = false;
      gatewayOk = false;
      gatewayStatusText = err instanceof Error ? err.message : String(err);
      capabilityCount = 0;
      capabilityRuntimeConfigured = false;
      capabilityProxyListenConfigured = false;
      capabilityProxyTargetConfigured = false;
    }
    await loadCapabilitySets();
    lastSyncedAt = new Date().toISOString();
  }

  async function loadCapabilitySets(): Promise<void> {
    // Best-effort: load the capability sets and their callable methods so the
    // page shows what's reachable. Never throws.
    if (!gatewayConfigured || !gatewayOk) {
      capabilitySets = [];
      return;
    }
    try {
      const sets = await listCapabilitySets();
      capabilitySets = await Promise.all(
        sets.map(async (set) => {
          try {
            return { ...set, methods: await getCapabilityCatalog(set.id) };
          } catch {
            return { ...set, methods: [] };
          }
        }),
      );
    } catch {
      capabilitySets = [];
    }
  }

  async function saveGateway(): Promise<void> {
    gatewaySaving = true;
    error = '';
    try {
      const gateway = await updateCapabilityGatewayConfig(gatewayAddr.trim(), gatewayToken);
      gatewayAddr = gateway.addr;
      savedGatewayAddr = gateway.addr;
      gatewayTokenSet = gateway.tokenSet;
      gatewayToken = '';
      await refreshGatewayStatus();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      gatewaySaving = false;
    }
  }

  async function loadAuthStatus(): Promise<void> {
    // Read-only login status from the backend; non-fatal so it never blocks
    // the rest of settings.
    try {
      authStatus = await getAuthStatus();
    } catch {
      authStatus = null;
    }
  }

  // Derived reactively (not as function calls) so the status pill re-renders
  // when gatewayConfigured / gatewayOk change after a save or status refresh.
  $: gatewayStatusLabel = !gatewayConfigured ? '未配置' : gatewayOk ? '已连接' : '连接失败';
  $: gatewayStatusChip = !gatewayConfigured ? 'amber' : gatewayOk ? 'green' : 'red';
  $: gatewayDirty = gatewayAddr.trim() !== savedGatewayAddr || gatewayToken.trim() !== '';
  $: capabilityRuntimeWarning = gatewayConfigured && gatewayOk && !capabilityRuntimeConfigured;

  function addEnvItem(): void {
    const name = newEnvName.trim();
    if (!name) return;
    envItems = [...envItems.filter((item) => item.name !== name), { name, value: newEnvValue, secret: newEnvSecret, valueKnown: true }];
    envDirty = true;
    newEnvName = '';
    newEnvValue = '';
    newEnvSecret = false;
  }

  function updateEnvItem(index: number, patch: Partial<EnvItem>): void {
    envItems = envItems.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item);
    envDirty = true;
  }

  function deleteEnvItem(index: number): void {
    envItems = envItems.filter((_, itemIndex) => itemIndex !== index);
    envDirty = true;
  }

  async function saveEnvItems(): Promise<void> {
    const emptyKnownSecrets = envItems.filter((item) => item.secret && item.valueKnown && item.value.trim() === '');
    if (emptyKnownSecrets.length > 0) {
      error = `请填写这些 Secret 的值，或删除对应变量：${emptyKnownSecrets.map((item) => item.name || '未命名').join(', ')}`;
      return;
    }
    savingEnv = true;
    error = '';
    message = '';
    try {
      await updateEnvItems(envItems);
      envItems = await listEnvItems();
      envDirty = false;
      message = '全局环境变量已保存';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      savingEnv = false;
    }
  }

  function openCreateWorkspace(): void {
    workspaceDraft = null;
    workspaceName = '';
    workspaceType = 'git';
    resetGitWorkspaceForm();
    workspaceConfigMode = 'form';
    workspaceConfigJson = buildWorkspaceConfigJson();
    workspaceComment = '';
    workspaceFiles = [];
  }

  function openEditWorkspace(workspace: WorkspacePreset): void {
    workspaceDraft = workspace;
    workspaceName = workspace.name;
    workspaceType = workspace.type || 'git';
    workspaceConfigMode = 'form';
    workspaceConfigJson = workspace.configJson || '{}';
    hydrateWorkspaceConfigForm(workspaceConfigJson);
    workspaceComment = workspace.comment;
    workspaceFiles = [];
    if (workspace.type === 'file') {
      void loadWorkspaceFiles(workspace.id);
    }
  }

  function cancelWorkspaceEdit(): void {
    workspaceDraft = null;
    workspaceName = '';
    workspaceType = 'git';
    resetGitWorkspaceForm();
    workspaceConfigMode = 'form';
    workspaceConfigJson = buildWorkspaceConfigJson();
    workspaceComment = '';
    workspaceFiles = [];
  }

  function onWorkspaceTypeChange(): void {
    workspaceFiles = [];
    if (workspaceType === 'git') {
      if (!workspaceDraft) {
        resetGitWorkspaceForm();
      }
      workspaceConfigMode = 'form';
      workspaceConfigJson = buildWorkspaceConfigJson();
    } else {
      workspaceConfigMode = 'form';
      workspaceConfigJson = '{}';
    }
  }

  function resetGitWorkspaceForm(): void {
    workspaceGitUrl = '';
    workspaceGitBranch = defaultGitBranch;
    workspaceGitCredential = '';
  }

  function hydrateWorkspaceConfigForm(configJson: string): void {
    resetGitWorkspaceForm();
    if (workspaceType !== 'git') return;
    const trimmed = configJson.trim();
    if (!trimmed) return;
    try {
      const config = JSON.parse(trimmed) as GitWorkspaceConfigDraft;
      workspaceGitUrl = stringConfigValue(config.url) || stringConfigValue(config.repo_url) || stringConfigValue(config.repoUrl);
      workspaceGitBranch = stringConfigValue(config.branch) || defaultGitBranch;
      workspaceGitCredential = stringConfigValue(config.credential);
      if (hasAdvancedGitConfig(config)) {
        workspaceConfigMode = 'json';
      }
    } catch {
      workspaceConfigMode = 'json';
    }
  }

  function stringConfigValue(value: unknown): string {
    return typeof value === 'string' ? value : '';
  }

  function hasAdvancedGitConfig(config: GitWorkspaceConfigDraft): boolean {
    return Boolean(
      stringConfigValue(config.commit) ||
      stringConfigValue(config.path) ||
      stringConfigValue(config.username) ||
      stringConfigValue(config.password),
    );
  }

  function buildWorkspaceConfigJson(): string {
    if (workspaceType !== 'git') return '{}';
    const config: Record<string, string> = {};
    const url = workspaceGitUrl.trim();
    const branch = workspaceGitBranch.trim();
    const credential = workspaceGitCredential.trim();
    if (url) config.url = url;
    if (branch) config.branch = branch;
    if (credential) config.credential = credential;
    return JSON.stringify(config, null, 2);
  }

  function switchWorkspaceConfigMode(mode: 'form' | 'json'): void {
    if (mode === workspaceConfigMode) return;
    error = '';
    if (mode === 'json') {
      workspaceConfigJson = workspaceGitUrl.trim() ? buildWorkspaceConfigJson() : gitConfigJsonPlaceholder;
      workspaceConfigMode = 'json';
      return;
    }
    try {
      JSON.parse(workspaceConfigJson || '{}');
      workspaceConfigMode = 'form';
      hydrateWorkspaceConfigForm(workspaceConfigJson);
    } catch {
      error = '智能体工作文件配置必须是合法 JSON，修正后才能切回表单';
    }
  }

  async function loadWorkspaceFiles(id: string): Promise<void> {
    if (!id) {
      workspaceFiles = [];
      return;
    }
    workspaceFileLoading = true;
    error = '';
    try {
      workspaceFiles = await listWorkspaceFiles(id);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      workspaceFileLoading = false;
    }
  }

  async function onUploadArchive(event: Event): Promise<void> {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (!workspaceDraft || workspaceType !== 'file' || !file) {
      return;
    }
    workspaceUploading = true;
    error = '';
    message = '';
    try {
      workspaceFiles = await uploadWorkspaceArchive(workspaceDraft.id, file);
      message = 'tar 内容已导入';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      workspaceUploading = false;
      input.value = '';
    }
  }

  async function saveWorkspace(): Promise<void> {
    if (!workspaceName.trim()) {
      error = '智能体工作文件名称必填';
      return;
    }
    // file / empty 类型不绑定客户端 JSON 配置；file 的存储根由后端按 workspace id 生成。
    let configJson = '{}';
    if (workspaceType === 'git') {
      configJson = workspaceConfigMode === 'json' ? workspaceConfigJson : buildWorkspaceConfigJson();
      try {
        const parsed = JSON.parse(configJson || '{}') as GitWorkspaceConfigDraft;
        const url = stringConfigValue(parsed.url) || stringConfigValue(parsed.repo_url) || stringConfigValue(parsed.repoUrl);
        if (!url.trim()) {
          error = 'Git 仓库地址必填';
          return;
        }
      } catch {
        error = '智能体工作文件配置必须是合法 JSON';
        return;
      }
    }
    error = '';
    message = '';
    try {
      const input = { name: workspaceName, type: workspaceType, configJson, comment: workspaceComment };
      const saved = workspaceDraft
        ? await updateWorkspacePreset(workspaceDraft.id, input)
        : await createWorkspacePreset(input);
      workspaces = [saved, ...workspaces.filter((item) => item.id !== saved.id)];
      message = '智能体工作文件配置已保存';
      if (saved.type === 'file') {
        // file 型上传需要已保存的 workspace id，保留表单并载入文件列表。
        workspaceDraft = saved;
        workspaceName = saved.name;
        workspaceType = saved.type;
        workspaceConfigJson = saved.configJson || '{}';
        hydrateWorkspaceConfigForm(workspaceConfigJson);
        workspaceComment = saved.comment;
        await loadWorkspaceFiles(saved.id);
      } else {
        cancelWorkspaceEdit();
      }
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function removeWorkspace(workspace: WorkspacePreset): Promise<void> {
    error = '';
    message = '';
    try {
      await deleteWorkspacePreset(workspace.id);
      workspaces = workspaces.filter((item) => item.id !== workspace.id);
      message = '智能体工作文件配置已删除';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  function resetWebhookForm(): void {
    webhookForm = {
      id: '',
      name: '',
      enabled: true,
      provider: '',
      topicPrefix: 'webhook.',
      token: '',
      bodyLimitBytes: '',
    };
  }

  function openCreateWebhookSource(): void {
    resetWebhookForm();
    webhookEditorOpen = true;
  }

  function closeWebhookEditor(): void {
    resetWebhookForm();
    webhookEditorOpen = false;
  }

  function openEditWebhookSource(source: WebhookSource): void {
    webhookForm = {
      id: source.id,
      name: source.name,
      enabled: source.enabled,
      provider: source.provider,
      topicPrefix: source.topicPrefix,
      token: '',
      bodyLimitBytes: source.bodyLimitBytes > 0 ? String(source.bodyLimitBytes) : '',
    };
    webhookEditorOpen = true;
  }

  function existingWebhookSourceSelected(): boolean {
    return Boolean(webhookForm.id && webhookSources.some((source) => source.id === webhookForm.id));
  }

  function validateWebhookForm(): { sourceId: string; bodyLimitBytes: number } | null {
    const sourceId = webhookForm.id.trim();
    if (!sourceId) {
      error = '唯一标识必填';
      return null;
    }
    const bodyLimitBytes = webhookForm.bodyLimitBytes.trim() ? Number(webhookForm.bodyLimitBytes.trim()) : 0;
    if (!Number.isFinite(bodyLimitBytes) || bodyLimitBytes < 0) {
      error = '请求体限制必须是非负数字';
      return null;
    }
    return { sourceId, bodyLimitBytes };
  }

  async function persistWebhookSource(): Promise<void> {
    error = '';
    message = '';
    const validated = validateWebhookForm();
    if (!validated) return;
    webhookSaving = true;
    try {
      const saved = await saveWebhookSource(validated.sourceId, {
        name: webhookForm.name,
        enabled: webhookForm.enabled,
        provider: webhookForm.provider,
        topicPrefix: webhookForm.topicPrefix,
        token: webhookForm.token,
        clearToken: false,
        bodyLimitBytes: validated.bodyLimitBytes,
      });
      webhookSources = [saved, ...webhookSources.filter((source) => source.id !== saved.id)];
      closeWebhookEditor();
      message = 'Webhook 配置已保存';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      webhookSaving = false;
    }
  }

  async function removeWebhookSource(source: WebhookSource): Promise<void> {
    deletingWebhookSourceId = source.id;
    error = '';
    message = '';
    try {
      await deleteWebhookSource(source.id);
      webhookSources = webhookSources.filter((item) => item.id !== source.id);
      if (webhookForm.id === source.id) {
        closeWebhookEditor();
      }
      message = 'Webhook 配置已删除';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      deletingWebhookSourceId = '';
    }
  }

  async function copyWorkspaceId(id: string, event?: MouseEvent): Promise<void> {
    event?.stopPropagation();
    if (!id) return;
    if (copyTimer) clearTimeout(copyTimer);
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(id);
      } else {
        fallbackCopyText(id);
      }
      copiedWorkspaceId = id;
      copyTimer = setTimeout(() => { copiedWorkspaceId = ''; }, 1600);
    } catch (err) {
      error = err instanceof Error ? err.message : '复制失败';
    }
  }

  function fallbackCopyText(value: string): void {
    const textarea = document.createElement('textarea');
    textarea.value = value;
    textarea.setAttribute('readonly', 'true');
    textarea.style.position = 'fixed';
    textarea.style.left = '-9999px';
    textarea.style.top = '0';
    document.body.appendChild(textarea);
    textarea.select();
    const copied = document.execCommand('copy');
    document.body.removeChild(textarea);
    if (!copied) {
      throw new Error('复制失败');
    }
  }

  function formatDateTime(value: string): string {
    return formatBeijingTime(value);
  }

  function validExpiry(value: string): string {
    // Backend sends a zero time (0001-...) when there's no live session.
    const time = value ? new Date(value).getTime() : Number.NaN;
    return !Number.isNaN(time) && new Date(value).getFullYear() > 1970 ? formatDateTime(value) : '-';
  }
</script>

{#if error}
  <div class="alert danger">{error}</div>
{/if}
{#if message}
  <div class="alert success">{message}</div>
{/if}

<section class="run-commandbar">
  <div>
    <h2>系统配置</h2>
  </div>
  <div class="run-command-metrics five">
    <button class:active={activeSection === 'gateway'} on:click={() => selectSection('gateway')}><span>能力</span><b>{capabilityCount}</b></button>
    <button class:active={activeSection === 'webhook'} on:click={() => selectSection('webhook')}><span>Webhook</span><b>{webhookSources.length}</b></button>
    <button class:active={activeSection === 'runtime'} on:click={() => selectSection('runtime')}><span>运行环境</span><b>1</b></button>
    <button class:active={activeSection === 'workspace'} on:click={() => selectSection('workspace')}><span>工作文件</span><b>{workspaces.length}</b></button>
    <button class:active={activeSection === 'env'} on:click={() => selectSection('env')}><span>环境变量</span><b>{envItems.length}</b></button>
  </div>
</section>

<div class="settings-layout">
  <nav class="settings-nav">
    {#each settingsSections as section}
      <button class:active={activeSection === section.key} on:click={() => selectSection(section.key)}>{section.label}</button>
    {/each}
  </nav>
  <div class="settings-content">
  {#if activeSection === 'gateway'}
  <section class="config-card">
    <div class="panel-head">
      <h2>能力接入网关</h2>
      <div class="toolbar">
        <button on:click={refreshGatewayStatus} disabled={gatewaySaving || gatewayDirty}>测试已保存配置</button>
        <button on:click={saveGateway} disabled={gatewaySaving}>{gatewaySaving ? '保存中…' : '保存'}</button>
      </div>
    </div>
    <div class="description-grid compact">
      <label>OctoBus 地址<input bind:value={gatewayAddr} placeholder="http://127.0.0.1:9000"></label>
      <label>访问 Token<input type="password" bind:value={gatewayToken} placeholder={gatewayTokenSet ? '已设置（留空保持不变）' : '可选'}></label>
      <div><span>连接状态</span><b><span class="chip {gatewayStatusChip}">{gatewayStatusLabel}</span></b></div>
      <div><span>可用服务数量</span><b>{capabilityCount}</b></div>
      <div><span>最近同步</span><b>{formatDateTime(lastSyncedAt)}</b></div>
    </div>
    {#if gatewayConfigured && !gatewayOk && gatewayStatusText}
      <div class="alert danger">{gatewayStatusText}</div>
    {/if}
    {#if capabilityRuntimeWarning}
      <div class="alert warning">OctoBus 控制面已连接，但运行时 gRPC capability proxy 尚未完整配置。需要在 daemon 启动环境中设置 CAP_GRPC_LISTEN 和 CAP_GRPC_TARGET，并重启后新建会话，Agent 才能调用 gRPC capability。</div>
      <div class="settings-hint">当前状态：CAP_GRPC_LISTEN {capabilityProxyListenConfigured ? '已配置' : '未配置'}，CAP_GRPC_TARGET {capabilityProxyTargetConfigured ? '已配置' : '未配置'}。</div>
    {/if}
    {#if gatewayDirty}
      <div class="alert warning">当前输入尚未保存，连接状态仍来自已保存配置。请先保存后再测试连接。</div>
    {/if}
    {#if gatewayConfigured && gatewayOk}
      <div class="cap-sets">
        <h3>能力集</h3>
        {#if capabilitySets.length === 0}
          <p class="cap-empty">OctoBus 暂无已发布的能力集。</p>
        {:else}
          {#each capabilitySets as set}
            <div class="cap-set">
              <div class="cap-set-head">
                <b>{set.name || set.id}</b>
                <span class="chip {set.enabled ? 'green' : 'gray'}">{set.enabled ? '启用' : '停用'}</span>
                <small>{set.id}</small>
              </div>
              {#if set.description}<p class="cap-set-desc">{set.description}</p>{/if}
              {#if set.methods.length === 0}
                <p class="cap-empty">无可调用能力</p>
              {:else}
                <div class="table cap-table">
                  <div class="thead">
                    <div>可调用能力</div>
                    <div>接入源</div>
                    <div>连接实例</div>
                    <div>状态</div>
                  </div>
                  {#each set.methods as method}
                    <div class="tr">
                      <div><code>{method.methodFullName}</code></div>
                      <div>{method.serviceId}</div>
                      <div>{method.instanceId}</div>
                      <div>
                        <span class="chip {method.backendInstanceStatus === 'running' ? 'green' : 'gray'}">
                          {method.backendInstanceStatus || '-'}
                        </span>
                      </div>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
          {/each}
        {/if}
      </div>
    {/if}
  </section>
  {:else if activeSection === 'runtime'}
  <section class="config-card">
    <div class="panel-head"><h2>运行环境</h2></div>
    <div class="description-grid">
      <div><span>系统镜像</span><b>{runtimeImage}</b></div>
      <div><span>说明</span><b>运行会话与智能体时默认使用的系统镜像。</b></div>
    </div>
  </section>
  {:else if activeSection === 'auth'}
  <section class="config-card">
    <div class="panel-head"><h2>登录控制</h2></div>
    <div class="description-grid">
      <div><span>启用状态</span><b><span class="chip {authStatus?.enabled ? 'green' : 'gray'}">{authStatus?.enabled ? '已启用' : '未启用'}</span></b></div>
      <div><span>OAuth 登录</span><b>{authStatus?.oauthEnabled ? '已启用' : '未启用'}</b></div>
      {#if authStatus?.enabled}
        <div><span>登录状态</span><b><span class="chip {authStatus.loggedIn ? 'green' : 'amber'}">{authStatus.loggedIn ? '已登录' : '未登录'}</span></b></div>
        <div><span>当前用户</span><b>{authStatus.username || '-'}</b></div>
        <div><span>会话过期</span><b>{validExpiry(authStatus.expiresAt)}</b></div>
        <div><span>说明</span><b>登录由部署时的 AUTH_PASSWORD / AUTH_USERNAME 配置，此处仅展示状态。</b></div>
      {:else}
        <div style="grid-column: 1 / -1;"><span>说明</span><b>未启用登录控制，访问无需登录。设置部署时的 AUTH_PASSWORD 后开启。</b></div>
      {/if}
    </div>
  </section>
  {:else if activeSection === 'webhook'}
  <section class="config-card">
    <div class="panel-head">
      <h2>Webhook 配置</h2>
      <div class="toolbar">
        <button on:click={openCreateWebhookSource} disabled={webhookSaving}>新增配置</button>
      </div>
    </div>
    <div class="webhook-layout">
      {#if webhookEditorOpen}
      <section class="webhook-editor">
        <div class="webhook-editor-head">
          <div>
            <h3>{existingWebhookSourceSelected() ? '编辑配置' : '新建配置'}</h3>
            <p>{existingWebhookSourceSelected() ? '修改后点击保存配置。' : '填写后点击创建配置。'}</p>
          </div>
        </div>
        <div class="webhook-form-grid">
          <label class="form-item"><span>唯一标识</span><input bind:value={webhookForm.id} disabled={existingWebhookSourceSelected() || webhookSaving} placeholder="github-main"></label>
          <label class="form-item"><span>名称</span><input bind:value={webhookForm.name} placeholder="GitHub Main"></label>
          <label class="form-item"><span>平台</span><input bind:value={webhookForm.provider} placeholder="github"></label>
          <label class="form-item"><span>主题前缀</span><input bind:value={webhookForm.topicPrefix} placeholder="webhook.github."></label>
          <label class="form-item"><span>访问令牌</span><input bind:value={webhookForm.token} type="password" placeholder={existingWebhookSourceSelected() ? '不修改请留空' : '请输入访问令牌'}></label>
          <label class="form-item"><span>请求体上限</span><input bind:value={webhookForm.bodyLimitBytes} inputmode="numeric" placeholder="1048576"></label>
          <div class="webhook-switches">
            <label><input type="checkbox" bind:checked={webhookForm.enabled}> 启用</label>
          </div>
        </div>
        <div class="webhook-form-actions">
          <button on:click={persistWebhookSource} disabled={webhookSaving}>{webhookSaving ? '保存中...' : existingWebhookSourceSelected() ? '保存配置' : '创建配置'}</button>
          <button class="ghost" on:click={closeWebhookEditor} disabled={webhookSaving}>取消</button>
        </div>
      </section>
      {/if}
      <div class="config-list webhook-config-list">
        {#each webhookSources as source}
          <div class:selected={webhookEditorOpen && webhookForm.id === source.id} class="config-list-item webhook-list-item">
            <div>
              <b>{source.name || source.id}</b>
              <p>{source.topicPrefix} · {source.provider || '平台未设置'} · {formatDateTime(source.updatedAt)}</p>
              <div class="webhook-source-meta">
                <span class="chip {source.enabled ? 'green' : 'amber'}">{source.enabled ? '已启用' : '已停用'}</span>
                <span class="chip {source.hasToken ? 'blue' : 'gray'}">令牌{source.hasToken ? '已设置' : '未设置'}</span>
                <span class="chip gray">上限 {source.bodyLimitBytes || '-'}</span>
              </div>
            </div>
            <div class="toolbar">
              <button on:click={() => openEditWebhookSource(source)} disabled={webhookSaving}>编辑</button>
              <button class="danger" on:click={() => removeWebhookSource(source)} disabled={deletingWebhookSourceId === source.id}>{deletingWebhookSourceId === source.id ? '删除中...' : '删除'}</button>
            </div>
          </div>
        {/each}
        {#if webhookSources.length === 0}
          <div class="empty">暂无 Webhook 配置。点击「新增配置」开始创建。</div>
        {/if}
      </div>
    </div>
  </section>
  {:else if activeSection === 'workspace'}
  <section class="config-card">
    <div class="panel-head"><h2>智能体工作文件</h2><button on:click={openCreateWorkspace}>新增配置</button></div>
    <div class="workspace-editor">
      <select bind:value={workspaceType} on:change={onWorkspaceTypeChange}><option value="git">Git</option><option value="file">文件</option><option value="empty">空工作文件</option></select>
      <input bind:value={workspaceName} placeholder="配置名称">
      <input bind:value={workspaceComment} placeholder="备注">
      <button on:click={saveWorkspace}>{workspaceDraft ? '更新' : '添加'}</button>
    </div>
    {#if workspaceType === 'git'}
      <div class="workspace-config-panel">
        <div class="segmented-control">
          <button type="button" class:active={workspaceConfigMode === 'form'} on:click={() => switchWorkspaceConfigMode('form')}>表单</button>
          <button type="button" class:active={workspaceConfigMode === 'json'} on:click={() => switchWorkspaceConfigMode('json')}>JSON</button>
        </div>
        {#if workspaceConfigMode === 'form'}
          <div class="workspace-git-grid">
            <label class="form-item wide"><span>仓库地址</span><input bind:value={workspaceGitUrl} placeholder="https://github.com/org/repo.git"></label>
            <label class="form-item"><span>分支</span><input bind:value={workspaceGitBranch} placeholder="main"></label>
            <label class="form-item"><span>Credential</span><input bind:value={workspaceGitCredential} type="password" placeholder="可选"></label>
          </div>
        {:else}
          <label class="form-item"><span>完整配置 JSON</span><textarea rows="8" bind:value={workspaceConfigJson} placeholder={gitConfigJsonPlaceholder}></textarea></label>
        {/if}
      </div>
    {:else if workspaceType === 'empty'}
      <div class="workspace-config-panel">
        <div class="empty">空工作文件不需要额外配置。</div>
      </div>
    {/if}
    {#if workspaceType === 'file'}
      {#if workspaceDraft}
        <div class="workspace-files">
          <div class="workspace-upload-panel">
            <div class="workspace-upload-grid">
              <div class="workspace-upload-box">
                <label class="file-action-button">
                  <input type="file" accept=".tar,application/x-tar" disabled={workspaceUploading} on:change={onUploadArchive}>
                  <span>选择并导入 .tar</span>
                </label>
              </div>
            </div>
            <div class="workspace-upload-status">
              <span>{workspaceUploading ? '正在导入...' : ''}</span>
              <button on:click={() => workspaceDraft && loadWorkspaceFiles(workspaceDraft.id)} disabled={workspaceFileLoading || workspaceUploading}>{workspaceFileLoading ? '加载中...' : '刷新列表'}</button>
              <b>{workspaceFiles.length} 项</b>
            </div>
          </div>
          {#if workspaceFileLoading}
            <div class="empty">正在加载文件...</div>
          {:else if workspaceFiles.length === 0}
            <div class="empty">暂无文件。</div>
          {:else}
            <div class="config-list">
              {#each workspaceFiles as fileEntry}
                <div class="config-list-item">
                  <div>
                    <b>{fileEntry.path}</b>
                    <p>{fileEntry.dir ? '目录' : '文件'} · {fileEntry.size} B · {formatDateTime(fileEntry.updatedAt)}</p>
                  </div>
                  <span class="chip {fileEntry.dir ? 'blue' : 'gray'}">{fileEntry.dir ? '目录' : '文件'}</span>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
    {/if}
    <div class="config-list">
      {#each workspaces as workspace}
        <div class="config-list-item">
          <div>
            <b>{workspace.name}</b>
            <p>{workspace.type} · {workspace.comment || workspace.id}</p>
            <p class="workspace-id-line">
              <span>ID</span><code>{workspace.id}</code>
              <button class="workspace-id-copy" type="button" title="复制智能体工作文件 ID" on:click={(event) => copyWorkspaceId(workspace.id, event)}>
                <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 8h10v10H8z"></path><path d="M6 16H4V4h12v2"></path></svg>
              </button>
              {#if copiedWorkspaceId === workspace.id}<em>已复制</em>{/if}
            </p>
          </div>
          <div class="toolbar">
            <button on:click={() => openEditWorkspace(workspace)}>编辑</button>
            <button on:click={() => removeWorkspace(workspace)}>删除</button>
          </div>
        </div>
      {/each}
      {#if workspaces.length === 0}
        <div class="empty">暂无智能体工作文件配置。</div>
      {/if}
    </div>
  </section>
  {:else if activeSection === 'env'}
  <section class="config-card">
    <div class="panel-head"><h2>全局环境变量</h2><button on:click={saveEnvItems} disabled={savingEnv || !envDirty}>{savingEnv ? '保存中...' : '保存变量'}</button></div>
    <div class="env-editor">
      <input bind:value={newEnvName} placeholder="KEY">
      <select bind:value={newEnvSecret}><option value={false}>普通</option><option value={true}>Secret</option></select>
      <input bind:value={newEnvValue} placeholder={newEnvSecret ? '留空保持 Secret' : 'value'} type={newEnvSecret ? 'password' : 'text'}>
      <button on:click={addEnvItem}>添加</button>
    </div>
    <div class="table env-table editable">
      <div class="thead"><span>名称</span><span>类型</span><span>值</span><span>操作</span></div>
      {#each envItems as item, index}
        <div class="tr">
          <span><input value={item.name} on:input={(event) => updateEnvItem(index, { name: event.currentTarget.value })}></span>
          <span><select value={item.secret} on:change={(event) => updateEnvItem(index, { secret: event.currentTarget.value === 'true', valueKnown: !item.valueKnown ? false : true })}><option value={false}>普通</option><option value={true}>Secret</option></select></span>
          <span><input value={item.valueKnown ? item.value : ''} placeholder={item.secret && !item.valueKnown ? '未修改' : 'value'} type={item.secret ? 'password' : 'text'} on:input={(event) => updateEnvItem(index, { value: event.currentTarget.value, valueKnown: true })}></span>
          <span><button on:click={() => deleteEnvItem(index)}>删除</button></span>
        </div>
      {/each}
      {#if envItems.length === 0}
        <div class="tr"><span class="muted">暂无全局环境变量，使用上方表单添加。</span></div>
      {/if}
    </div>
  </section>
  {/if}
  </div>
</div>

<style>
  .cap-sets {
    margin-top: 12px;
    border-top: 1px solid var(--line);
    padding-top: 10px;
  }
  .cap-sets h3 {
    margin: 0 0 6px;
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-semibold);
    color: var(--muted);
  }
  .cap-set + .cap-set {
    margin-top: 10px;
  }
  .cap-set-head {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 4px;
  }
  .cap-set-head small {
    color: var(--muted);
    font-family: var(--mono);
  }
  .cap-set-desc {
    margin: 2px 0 4px;
    color: var(--muted);
    font-size: var(--font-size-sm);
  }
  .cap-empty {
    color: var(--muted);
    font-size: var(--font-size-sm);
    margin: 4px 0;
  }
</style>
