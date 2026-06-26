<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import RobotOutlined from '@ant-design/icons-svg/es/asn/RobotOutlined';
  import FilterOutlined from '@ant-design/icons-svg/es/asn/FilterOutlined';

  import AntIcon from '../components/AntIcon.svelte';
  import {
    createAgentDefinition,
    createAgentDefinitionSession,
    deleteAgentDefinition,
    listAgentDefinitions,
    updateAgentDefinition,
    type AgentDefinition,
    type AgentDefinitionInput,
  } from '../api/agents';
  import { listCapabilitySets, listWorkspacePresets, type CapabilitySet, type WorkspacePreset } from '../api/config';
  import { appPath } from '../paths';
  import { formatBeijingTime } from '../time';
  import { defaultGuestImage } from '../model/runtime';
  import { currentQueryParams, updateQueryParams } from '../url';

  type WorkSource = 'empty' | 'file' | 'git';
  type AgentDraft = AgentDefinition & {
    workSource: WorkSource;
  };

  let agents: AgentDraft[] = [];
  let selectedAgent: AgentDraft | null = null;
  let error = '';
  let message = '';
  let messageTimer: ReturnType<typeof setTimeout> | null = null;
  let keyword = '';
  let showDeletedAgents = false;
  let editAgent: AgentDraft | null = null;
  let editDraft: AgentDraft | null = null;
  let deleteConfirming = false;
  let runAgent: AgentDraft | null = null;
  let runWorkspaceMode: WorkSource = 'empty';
  let runWorkspaceId = '';
  let runTask = '';
  let running = false;
  let workspaces: WorkspacePreset[] = [];
  let capsets: CapabilitySet[] = [];
  let loading = true;
  let saving = false;
  let capabilityStatus = '由全局配置默认注入';
  let capabilityStatusClass: 'green' | 'amber' | 'red' = 'amber';

  $: orderedAgents = [...agents].sort((left, right) => {
    if (isDeletedAgent(left) !== isDeletedAgent(right)) return isDeletedAgent(left) ? 1 : -1;
    return (right.updatedAt || '').localeCompare(left.updatedAt || '');
  });
  $: visibleAgents = orderedAgents.filter((agent) => showDeletedAgents || !isDeletedAgent(agent));
  $: filteredAgents = visibleAgents.filter((agent) =>
    [agent.name, agent.description, agent.provider, agent.runtimeImageId, agent.guestImage].join(' ').toLowerCase().includes(keyword.toLowerCase()),
  );
  $: activeAgent = selectedAgent && filteredAgents.some((agent) => agent.id === selectedAgent?.id)
    ? selectedAgent
    : filteredAgents[0] ?? null;
  $: if (selectedAgent && !agents.some((agent) => agent.id === selectedAgent?.id)) {
    selectedAgent = filteredAgents[0] ?? null;
  }

  onMount(() => {
    void load();
    window.addEventListener('popstate', syncFromURL);
    return () => window.removeEventListener('popstate', syncFromURL);
  });

  onDestroy(() => {
    if (messageTimer) {
      clearTimeout(messageTimer);
    }
  });

  function showMessage(text: string): void {
    message = text;
    if (messageTimer) {
      clearTimeout(messageTimer);
    }
    messageTimer = setTimeout(() => {
      message = '';
      messageTimer = null;
    }, 3000);
  }

  function syncFromURL(): void {
    const params = currentQueryParams();
    const agentId = params.get('agent');
    const mode = params.get('mode');
    const currentAgents = filteredAgentList();
    selectedAgent = currentAgents.find((item) => item.id === agentId) || currentAgents[0] || null;
    if (mode === 'create') {
      setEditDraft(null, false);
    } else if (mode === 'edit' && selectedAgent) {
      setEditDraft(selectedAgent, false);
    } else if (editDraft) {
      editAgent = null;
      editDraft = null;
    }
  }

  function selectAgent(agent: AgentDraft): void {
    selectedAgent = agent;
    updateQueryParams({
      agent: selectedAgent.id,
    });
  }

  function toggleDeletedAgents(): void {
    if (selectedAgent && isDeletedAgent(selectedAgent) && !showDeletedAgents) {
      selectedAgent = filteredAgentList()[0] ?? null;
      updateQueryParams({ agent: selectedAgent?.id || null });
    }
  }

  function clearAgentFilters(): void {
    keyword = '';
    if (visibleAgents.length === 0 && agents.some((agent) => isDeletedAgent(agent))) {
      showDeletedAgents = true;
    }
    selectedAgent = filteredAgentList()[0] ?? null;
    updateQueryParams({ agent: selectedAgent?.id || null });
  }

  function filteredAgentList(): AgentDraft[] {
    return orderedAgents
      .filter((agent) => showDeletedAgents || !isDeletedAgent(agent))
      .filter((agent) =>
        [agent.name, agent.description, agent.provider, agent.runtimeImageId, agent.guestImage].join(' ').toLowerCase().includes(keyword.toLowerCase()),
      );
  }

  async function load(): Promise<void> {
    loading = true;
    try {
      const [agentList, presets] = await Promise.all([listAgentDefinitions(), listWorkspacePresets()]);
      agents = agentList.map(toDraft);
      workspaces = presets.filter((item) => item.type === 'git' || item.type === 'file');
      // Capability sets are optional: an unreachable gateway must not block agents.
      try {
        capsets = await listCapabilitySets();
      } catch {
        capsets = [];
      }
      capabilityStatus = '由全局配置默认注入';
      capabilityStatusClass = 'amber';
      error = '';
      syncFromURL();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  function toDraft(agent: AgentDefinition): AgentDraft {
    return {
      ...agent,
      runtimeImageId: agent.runtimeImageId,
      driver: agent.driver || 'docker',
      guestImage: agent.guestImage || defaultGuestImage,
      workSource: agent.workFiles.source,
    };
  }

  function createEmptyAgent(): AgentDraft {
    return {
      id: '',
      name: '',
      description: '',
      enabled: true,
      provider: 'codex',
      model: '',
      systemPrompt: '',
      runtimeImageId: '',
      driver: 'docker',
      guestImage: defaultGuestImage,
      workspaceId: '',
      envItems: [],
      configJson: '{}',
      capsetIds: [],
      availability: '可用',
      availabilityClass: 'green',
      health: '健康',
      healthClass: 'green',
      workFiles: { source: 'empty', workspaceId: '', workspaceName: '', workspaceType: 'empty', summary: '', configJson: '' },
      currentRun: { text: '暂无运行', runningSessionCount: 0, runningLoaderRunCount: 0 },
      latestRun: null,
      createdAt: '',
      updatedAt: '',
      deletedAt: '',
      workSource: 'empty',
    };
  }

  function isDeletedAgent(agent: AgentDefinition | AgentDraft | null): boolean {
    return Boolean(agent?.deletedAt);
  }

  function openEdit(agent: AgentDraft | null): void {
    if (isDeletedAgent(agent)) return;
    setEditDraft(agent, true);
  }

  function setEditDraft(agent: AgentDraft | null, syncURL: boolean): void {
    editAgent = agent;
    const draft = cloneAgentDraft(agent || createEmptyAgent());
    // New agents default to every available capset selected.
    if (!agent) {
      draft.capsetIds = capsets.map((capset) => capset.id);
    }
    editDraft = draft;
    deleteConfirming = false;
    if (!syncURL) return;
    updateQueryParams({ mode: agent ? 'edit' : 'create', agent: agent?.id || selectedAgent?.id || null });
  }

  function closeEdit(): void {
    editAgent = null;
    editDraft = null;
    deleteConfirming = false;
    updateQueryParams({ mode: null });
  }

  function closeEditFromKey(event: KeyboardEvent): void {
    if (event.key === 'Escape' || event.key === 'Enter' || event.key === ' ') {
      closeEdit();
    }
  }

  async function saveAgent(runNow: boolean): Promise<void> {
    if (!editDraft || !editDraft.name.trim()) {
      error = '请输入智能体名称';
      return;
    }
    saving = true;
    error = '';
    message = '';
    try {
      normalizeEditDraft();
      const input = draftToInput(editDraft);
      const saved = editAgent
        ? await updateAgentDefinition(editAgent.id, input)
        : await createAgentDefinition(input);
      const savedDraft = toDraft(saved);
      agents = [savedDraft, ...agents.filter((item) => item.id !== savedDraft.id)];
      selectedAgent = savedDraft;
      showMessage(editAgent ? '智能体已更新' : '智能体已保存');
      closeEdit();
      if (runNow) {
        openRun(savedDraft);
      }
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }

  async function deleteAgent(): Promise<void> {
    if (!editAgent) return;
    if (!deleteConfirming) {
      deleteConfirming = true;
      return;
    }
    saving = true;
      error = '';
      message = '';
    try {
      await deleteAgentDefinition(editAgent.id);
      await load();
      showMessage('智能体已删除');
      closeEdit();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }

  function openRun(agent: AgentDraft): void {
    if (isDeletedAgent(agent) || running) return;
    runAgent = agent;
    runWorkspaceMode = agent.workSource === 'git' || agent.workSource === 'file' ? agent.workSource : 'empty';
    runWorkspaceId = agent.workspaceId;
    runTask = '';
    error = '';
    message = '';
  }

  function closeRun(): void {
    runAgent = null;
    runTask = '';
  }

  function closeRunFromKey(event: KeyboardEvent): void {
    if (event.key === 'Escape' || event.key === 'Enter' || event.key === ' ') {
      closeRun();
    }
  }

  async function runSelectedAgent(): Promise<void> {
    if (!runAgent) return;
    running = true;
    error = '';
    message = '';
    try {
      const task = runTask.trim();
      const title = task ? `${task} ${formatSessionTime(new Date())}` : `${runAgent.name} ${formatSessionTime(new Date())}`;
      const sessionId = await createAgentDefinitionSession({
        agentId: runAgent.id,
        title,
        workspaceId: runWorkspaceMode === 'git' || runWorkspaceMode === 'file' ? runWorkspaceId : '',
        driver: runAgent.driver || 'docker',
        guestImage: runAgent.guestImage,
        message: runTask,
        provider: runAgent.provider,
      });
      showMessage(sessionId ? `工作会话已创建：${sessionId}` : '工作会话已创建');
      closeRun();
      window.location.assign(sessionId ? appPath(`/runs?runId=${encodeURIComponent(sessionId)}`) : appPath('/runs'));
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      running = false;
    }
  }

  function workFilesText(agent: AgentDraft): string {
    if (agent.workFiles.source === 'git') {
      return agent.workFiles.summary || agent.workFiles.workspaceName || 'Git workspace';
    }
    if (agent.workFiles.source === 'file') {
      return agent.workFiles.summary || agent.workFiles.workspaceName || '文件 workspace';
    }
    return '空';
  }

  function draftToInput(draft: AgentDraft): AgentDefinitionInput {
    const workspaceId = draft.workSource === 'empty' ? '' : draft.workspaceId;
    return {
      name: draft.name,
      description: draft.description,
      enabled: draft.enabled,
      provider: draft.provider || 'codex',
      model: draft.model,
      systemPrompt: draft.systemPrompt,
      runtimeImageId: draft.runtimeImageId,
      driver: draft.driver || 'docker',
      guestImage: draft.guestImage,
      workspaceId,
      envItems: draft.envItems,
      configJson: draft.configJson || '{}',
      capsetIds: draft.capsetIds ?? [],
    };
  }

  function toggleAgentCapset(id: string, checked: boolean): void {
    if (!editDraft) return;
    const ids = checked ? [...editDraft.capsetIds, id] : editDraft.capsetIds.filter((value) => value !== id);
    editDraft = { ...editDraft, capsetIds: ids };
  }

  function cloneAgentDraft(agent: AgentDraft): AgentDraft {
    return {
      ...agent,
      workFiles: { ...agent.workFiles },
      currentRun: { ...agent.currentRun },
      latestRun: agent.latestRun ? { ...agent.latestRun } : null,
      envItems: agent.envItems.map((item) => ({ ...item })),
    };
  }

  function normalizeEditDraft(): void {
    if (!editDraft) return;
    try {
      if (editDraft.configJson.trim()) {
        JSON.parse(editDraft.configJson);
      }
    } catch {
      throw new Error('配置 JSON 格式不正确');
    }
    editDraft.envItems = editDraft.envItems
      .map((item) => ({ ...item, name: item.name.trim() }))
      .filter((item) => item.name);
  }

  function addEnvItem(): void {
    if (!editDraft) return;
    editDraft.envItems = [...editDraft.envItems, { name: '', value: '', secret: false }];
  }

  function removeEnvItem(index: number): void {
    if (!editDraft) return;
    editDraft.envItems = editDraft.envItems.filter((_, itemIndex) => itemIndex !== index);
  }

  function onDraftWorkSourceChange(): void {
    if (!editDraft) return;
    if (editDraft.workSource === 'empty') {
      editDraft.workspaceId = '';
    } else if (!editDraft.workspaceId || workspaces.find((workspace) => workspace.id === editDraft?.workspaceId)?.type !== editDraft.workSource) {
      editDraft.workspaceId = workspaces.find((workspace) => workspace.type === editDraft?.workSource)?.id ?? '';
    }
  }

  function onRunWorkspaceModeChange(): void {
    if (runWorkspaceMode === 'empty') {
      runWorkspaceId = '';
      return;
    }
    if (!runWorkspaceId || workspaces.find((workspace) => workspace.id === runWorkspaceId)?.type !== runWorkspaceMode) {
      runWorkspaceId = workspaces.find((workspace) => workspace.type === runWorkspaceMode)?.id ?? '';
    }
  }

  function runStatusText(agent: AgentDraft): string {
    return agent.currentRun.text || '暂无运行';
  }

  function latestRunText(agent: AgentDraft): string {
    if (!agent.latestRun) return '暂无运行';
    return `${agent.latestRun.status || '-'} · ${formatDateTime(agent.latestRun.at)}`;
  }

  function agentStatusLabel(agent: AgentDraft): string {
    return isDeletedAgent(agent) ? '已删除' : agent.availability;
  }

  function agentStatusClass(agent: AgentDraft): string {
    return isDeletedAgent(agent) ? 'deleted' : agent.availabilityClass;
  }

  function formatDateTime(value: string): string {
    return formatBeijingTime(value);
  }

  function formatSessionTime(date: Date): string {
    const pad = (value: number) => String(value).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
  }
</script>

{#if error}
  <div class="alert danger">{error}</div>
{/if}
{#if message}
  <div class="alert success">{message}</div>
{/if}

<section class="panel agents-panel">
  <div class="runs-toolbar agent-runs-toolbar">
    <div class="run-command-metrics compact">
      <button><span>全部</span><b>{visibleAgents.length}</b></button>
      <button><span>可用</span><b>{visibleAgents.filter((agent) => agent.availabilityClass === 'green').length}</b></button>
      <button><span>运行中</span><b>{visibleAgents.filter((agent) => agent.currentRun.runningSessionCount + agent.currentRun.runningLoaderRunCount > 0).length}</b></button>
      <button><span>异常</span><b>{visibleAgents.filter((agent) => agent.availabilityClass === 'red' || agent.healthClass === 'red').length}</b></button>
    </div>
    <div class="runs-filters agent-run-filters">
      <input class="filter-keyword" placeholder="按名称、描述、运行环境筛选" bind:value={keyword}>
      <label class="filter-checkbox agent-deleted-toggle"><input type="checkbox" bind:checked={showDeletedAgents} on:change={toggleDeletedAgents}><span>显示已删除</span></label>
      <button on:click={load}>{loading ? '刷新中...' : '刷新'}</button>
      <button class="primary" on:click={() => openEdit(null)}>创建智能体</button>
    </div>
  </div>

  {#if loading}
    <div class="empty">正在加载智能体...</div>
  {:else if filteredAgents.length === 0}
    <div class="empty-state">
      <div class="empty-state-icon">
        <AntIcon definition={agents.length === 0 ? RobotOutlined : FilterOutlined} />
      </div>
      {#if visibleAgents.length === 0 && !keyword}
        <h3>还没有智能体</h3>
        <p>智能体定义了运行环境、工作文件与默认配置。创建第一个智能体后即可在这里管理。</p>
        <div class="empty-state-actions">
          <button class="primary" on:click={() => openEdit(null)}>创建智能体</button>
        </div>
      {:else}
        <h3>没有匹配的智能体</h3>
        <p>当前共有 {visibleAgents.length} 个智能体，但都不满足筛选条件。试试调整或清除关键词。</p>
        <div class="empty-state-actions">
          <button on:click={clearAgentFilters}>清除筛选</button>
        </div>
      {/if}
    </div>
  {:else}
    <div class="agents-master-detail">
      <div class="agent-list-card">
        <div class="run-list-head">
          <b>智能体</b>
          <span>{filteredAgents.length} 个</span>
        </div>
        <div class="agent-list">
          {#each filteredAgents as agent}
            <button class="agent-list-item" class:active={activeAgent?.id === agent.id} on:click={() => selectAgent(agent)}>
              <span>
                <b>{agent.name || agent.id}</b>
                <small>{agent.provider || 'codex'} · {workFilesText(agent)}</small>
              </span>
              <em class={agentStatusClass(agent)}>{agentStatusLabel(agent)}</em>
            </button>
          {/each}
        </div>
      </div>
      <div class="agent-detail-panel">
        {#if activeAgent}
          <div class="agent-detail-head">
            <div>
              <h2>{activeAgent.name || activeAgent.id}</h2>
              <p>{activeAgent.description || '暂无描述'}</p>
            </div>
            <div class="toolbar">
              <span class="chip {agentStatusClass(activeAgent)}">{agentStatusLabel(activeAgent)}</span>
              <span class="chip {activeAgent.healthClass}">{activeAgent.health}</span>
              <button disabled={isDeletedAgent(activeAgent)} on:click={() => openEdit(activeAgent)}>编辑</button>
              <button class="primary" disabled={isDeletedAgent(activeAgent) || !activeAgent.enabled || activeAgent.availabilityClass === 'red' || running} on:click={() => { selectAgent(activeAgent); openRun(activeAgent); }}>{running ? '运行中...' : '运行'}</button>
            </div>
          </div>
          <div class="agent-detail-grid">
            <section>
              <h3>基础配置</h3>
              <div class="side-facts">
                <div><span>ID</span><b>{activeAgent.id || '-'}</b></div>
                <div><span>Provider</span><b>{activeAgent.provider || 'codex'}</b></div>
                <div><span>运行驱动</span><b>{activeAgent.driver || 'docker'}</b></div>
                <div><span>运行镜像</span><b>{activeAgent.guestImage || defaultGuestImage}</b></div>
                <div><span>创建时间</span><b>{formatDateTime(activeAgent.createdAt)}</b></div>
                <div><span>更新时间</span><b>{formatDateTime(activeAgent.updatedAt)}</b></div>
              </div>
            </section>
            <section>
              <h3>运行摘要</h3>
              <div class="side-facts">
                <div><span>当前运行</span><b>{runStatusText(activeAgent)}</b></div>
                <div><span>运行会话</span><b>{activeAgent.currentRun.runningSessionCount}</b></div>
                <div><span>自动化运行</span><b>{activeAgent.currentRun.runningLoaderRunCount}</b></div>
                <div><span>最近运行</span><b>{latestRunText(activeAgent)}</b></div>
                <div><span>工作文件</span><b>{workFilesText(activeAgent)}</b></div>
              </div>
            </section>
          </div>
          <section class="agent-prompt-panel">
            <h3>系统提示词</h3>
            <pre>{activeAgent.systemPrompt || '未配置'}</pre>
          </section>
          <div class="agent-detail-grid">
            <section>
              <h3>环境变量</h3>
              <div class="side-facts side-facts-wide">
                {#if activeAgent.envItems.length === 0}
                  <div><span>变量</span><b>未配置</b></div>
                {:else}
                  {#each activeAgent.envItems as item}
                    <div><span>{item.name}</span><b>{item.secret ? '已加密/隐藏' : item.value}</b></div>
                  {/each}
                {/if}
              </div>
            </section>
            <section>
              <h3>扩展配置</h3>
              <pre class="agent-config-preview">{activeAgent.configJson || '{}'}</pre>
            </section>
          </div>
        {/if}
      </div>
    </div>
  {/if}
</section>

{#if editDraft}
  <div class="drawer-mask" role="button" tabindex="0" aria-label="关闭智能体编辑抽屉" on:click={closeEdit} on:keydown={closeEditFromKey}></div>
  <div class="drawer wide" role="dialog" aria-modal="true" aria-labelledby="agent-edit-title">
    <div class="drawer-head">
      <h2 id="agent-edit-title">{editAgent ? '编辑智能体' : '创建智能体'}</h2>
      <div class="toolbar">
        {#if editAgent}
          <button class="danger-button" class:confirming={deleteConfirming} disabled={saving} on:click={deleteAgent}>{deleteConfirming ? '确认删除' : '删除'}</button>
        {/if}
        <button disabled={saving} on:click={closeEdit}>取消</button>
        <button disabled={saving} on:click={() => saveAgent(false)}>{saving ? '保存中...' : '保存'}</button>
        <button class="primary" disabled={saving} on:click={() => saveAgent(true)}>{saving ? '保存中...' : '保存并运行'}</button>
      </div>
    </div>
    <div class="drawer-body">
      {#if deleteConfirming}
        <div class="delete-confirm-alert">
          再次点击“确认删除”将停止关联运行会话和可识别的自动化任务，并将当前智能体标记为已删除。
        </div>
      {/if}
      <form class="drawer-form agent-form" on:submit|preventDefault={() => saveAgent(false)}>
        <label class="form-item checkbox-row form-span-2"><input type="checkbox" bind:checked={editDraft.enabled}><span>启用智能体</span></label>
        <label class="form-item form-span-2"><span>智能体名称</span><input bind:value={editDraft.name} required></label>
        <label class="form-item form-span-2 compact-textarea"><span>描述</span><textarea rows="2" bind:value={editDraft.description}></textarea></label>
        <label class="form-item">
          <span>Provider</span>
          <select bind:value={editDraft.provider}>
            <option value="codex">codex</option>
            <option value="claude">claude</option>
            <option value="gemini">gemini</option>
            <option value="opencode">opencode</option>
          </select>
        </label>
        <label class="form-item">
          <span>运行驱动</span>
          <select bind:value={editDraft.driver}>
            <option value="docker">docker</option>
            <option value="boxlite">boxlite</option>
            <option value="microsandbox">microsandbox</option>
          </select>
        </label>
        <label class="form-item"><span>运行镜像</span><input bind:value={editDraft.guestImage} placeholder={defaultGuestImage}></label>
        <div class="form-item form-span-2">
          <span>能力集（可多选，注入会话）</span>
          {#if capsets.length === 0}
            <p class="form-muted">无可用能力集</p>
          {:else}
            <div class="capset-checks">
              {#each capsets as capset}
                <label class="capset-check">
                  <input type="checkbox" checked={editDraft.capsetIds.includes(capset.id)} on:change={(event) => toggleAgentCapset(capset.id, event.currentTarget.checked)}>
                  <span>{capset.name || capset.id}</span>
                </label>
              {/each}
            </div>
          {/if}
        </div>
        <label class="form-item form-span-2 compact-textarea"><span>系统提示词</span><textarea rows="3" bind:value={editDraft.systemPrompt}></textarea></label>
        <fieldset class="form-item radio-field form-span-2">
          <legend>智能体工作文件</legend>
          <div class="radio-group">
            <label><input type="radio" bind:group={editDraft.workSource} value="empty" on:change={onDraftWorkSourceChange}> 空</label>
            <label><input type="radio" bind:group={editDraft.workSource} value="file" on:change={onDraftWorkSourceChange}> 文件 workspace</label>
            <label><input type="radio" bind:group={editDraft.workSource} value="git" on:change={onDraftWorkSourceChange}> Git workspace</label>
          </div>
        </fieldset>
        {#if editDraft.workSource === 'file' || editDraft.workSource === 'git'}
          <label class="form-item form-span-2">
            <span>{editDraft.workSource === 'git' ? 'Git workspace preset' : '文件 workspace preset'}</span>
            <select bind:value={editDraft.workspaceId}>
              <option value="">请选择</option>
              {#each workspaces.filter((workspace) => workspace.type === editDraft?.workSource) as workspace}
                <option value={workspace.id}>{workspace.name}</option>
              {/each}
            </select>
          </label>
        {/if}
        <section class="form-section form-span-2 agent-env-section">
          <div class="form-section-head">
            <h3>环境变量</h3>
            <button type="button" on:click={addEnvItem}>添加变量</button>
          </div>
          {#if editDraft.envItems.length === 0}
            <p class="form-muted">未配置环境变量。</p>
          {:else}
            <div class="agent-env-list">
              {#each editDraft.envItems as item, index}
                <div class="agent-env-row">
                  <label class="form-item"><span>名称</span><input bind:value={item.name} placeholder="ENV_NAME"></label>
                  <label class="form-item"><span>值</span><input bind:value={item.value} type={item.secret ? 'password' : 'text'} placeholder="变量值"></label>
                  <label class="form-item checkbox-row"><input type="checkbox" bind:checked={item.secret}><span>敏感</span></label>
                  <button type="button" on:click={() => removeEnvItem(index)}>删除</button>
                </div>
              {/each}
            </div>
          {/if}
        </section>
        <label class="form-item form-span-2 compact-textarea">
          <span>配置 JSON</span>
          <textarea rows="5" bind:value={editDraft.configJson} spellcheck="false"></textarea>
        </label>
      </form>
    </div>
  </div>
{/if}

{#if runAgent}
  <div class="drawer-mask" role="button" tabindex="0" aria-label="关闭智能体运行抽屉" on:click={closeRun} on:keydown={closeRunFromKey}></div>
  <div class="drawer" role="dialog" aria-modal="true" aria-labelledby="agent-run-title">
    <div class="drawer-head">
      <h2 id="agent-run-title">智能体运行</h2>
      <div class="toolbar">
        <button on:click={closeRun} disabled={running}>取消</button>
        <button class="primary" on:click={runSelectedAgent} disabled={running || (runWorkspaceMode !== 'empty' && !runWorkspaceId)}>{running ? '运行中...' : '运行'}</button>
      </div>
    </div>
    <div class="drawer-body drawer-form agent-run-form">
      <div class="run-launch-title">
        <div>
          <h3>{runAgent.name}</h3>
          <p>{runAgent.description || '暂无描述'}</p>
        </div>
        <span class="chip {runAgent.availabilityClass}">{runAgent.availability}</span>
      </div>

      <div class="run-launch-meta">
        <div><span>Provider</span><b>{runAgent.provider || 'codex'}</b></div>
        <div><span>运行驱动</span><b>{runAgent.driver || 'docker'}</b></div>
        <div><span>运行镜像</span><b>{runAgent.guestImage || defaultGuestImage}</b></div>
      </div>

      <fieldset class="form-item radio-field run-workspace-field">
        <legend>会话文件</legend>
        <div class="segmented-grid">
          <label class:active={runWorkspaceMode === 'empty'}><input type="radio" bind:group={runWorkspaceMode} value="empty" on:change={onRunWorkspaceModeChange}> 空白开始</label>
          <label class:active={runWorkspaceMode === 'git'}><input type="radio" bind:group={runWorkspaceMode} value="git" on:change={onRunWorkspaceModeChange}> Git workspace</label>
          <label class:active={runWorkspaceMode === 'file'}><input type="radio" bind:group={runWorkspaceMode} value="file" on:change={onRunWorkspaceModeChange}> 文件 workspace</label>
        </div>
      </fieldset>

      {#if runWorkspaceMode === 'git' || runWorkspaceMode === 'file'}
        <label class="form-item">
          <span>{runWorkspaceMode === 'git' ? 'Git workspace preset' : '文件 workspace preset'}</span>
          <select bind:value={runWorkspaceId}>
            <option value="">请选择</option>
            {#each workspaces.filter((workspace) => workspace.type === runWorkspaceMode) as workspace}
              <option value={workspace.id}>{workspace.name}</option>
            {/each}
          </select>
        </label>
      {/if}

      <label class="form-item run-task-input">
        <span>初始消息</span>
        <textarea rows="5" bind:value={runTask} placeholder="可选。留空时只创建工作会话。"></textarea>
      </label>

      <div class="run-launch-footer">
        <span class="chip {capabilityStatusClass}">能力接入</span>
        <b>全局默认</b>
        <small>{capabilityStatus}</small>
      </div>
    </div>
  </div>
{/if}

<style>
  .capset-checks {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 18px;
    padding: 8px 10px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: #fbfdff;
  }
  .capset-check {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: var(--font-size-sm);
    cursor: pointer;
  }
  .capset-check input {
    width: auto;
    margin: 0;
  }
</style>
