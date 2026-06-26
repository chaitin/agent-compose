<script lang="ts">
  import CopyOutlined from '@ant-design/icons-svg/es/asn/CopyOutlined';
  import InboxOutlined from '@ant-design/icons-svg/es/asn/InboxOutlined';
  import FilterOutlined from '@ant-design/icons-svg/es/asn/FilterOutlined';
  import ClearOutlined from '@ant-design/icons-svg/es/asn/ClearOutlined';
  import { createEventDispatcher, onDestroy, onMount, tick } from 'svelte';
  import { Terminal } from 'xterm';
  import { FitAddon } from '@xterm/addon-fit';

  import AntIcon from '../components/AntIcon.svelte';
  import { getAutomationRun, listAutomationEvents, listAutomationTasks, listRecentAutomationRuns, runAutomationTaskNow, type AutomationTask } from '../api/loaders';
  import { createAgentDefinitionSession, listAgentDefinitions, type AgentDefinition } from '../api/agents';
  import { listWorkspacePresets, type WorkspacePreset } from '../api/config';
  import {
    getWorkSessionStatus,
    listWorkSessionCells,
    listWorkSessionEvents,
    listWorkSessions,
    resumeWorkSession,
    sendWorkSessionMessageStream,
    stopWorkSession,
    watchWorkSession,
  } from '../api/sessions';
  import { automationRunToRun, sessionToRun, type ProductRun } from '../model/runs';
  import { CellType } from '../gen/proto/agentcompose/v1/agentcompose_pb.js';
  import { defaultGuestImage } from '../model/runtime';
  import { appPath } from '../paths';
  import { formatBeijingTime } from '../time';
  import { currentQueryParams, updateQueryParams } from '../url';

  const dispatch = createEventDispatcher<{ debug: string }>();
  type TerminalActionParams = { id?: string; text: string; running: boolean };
  type RunView = 'run' | 'agent' | 'task' | 'agent_task';
  type GroupTab = 'runs' | 'timeline' | 'artifacts' | 'automation';
  type WorkbenchMode = 'chat' | 'tasks' | 'timeline';
  type TimelineFilter = 'all' | 'manual' | 'task' | 'io' | 'input' | 'output' | 'error' | 'artifact' | 'system';
  type ConversationTaskKind = 'overview' | 'manual_conversation' | 'automation_task';
  type AgentStatusFilter = 'all' | 'running' | 'failed' | 'active' | 'empty';
  type AgentSort = 'recent' | 'failed' | 'name';
  type RunSourceFilter = 'all' | 'manual' | 'task';
  type TimeFilter = 'all' | 'today' | '7d' | '30d';
  type AuxiliaryPanelKind = 'none' | 'run' | 'task' | 'artifact' | 'system';
  type AgentObservation = {
    key: string;
    agentId: string;
    name: string;
    description: string;
    deleted: boolean;
    enabled: boolean;
    status: string;
    runs: ProductRun[];
    runningCount: number;
    totalCount: number;
    latestStatus: string;
    latestAt: string;
    failedCount: number;
    taskCount: number;
  };
  type RunGroup = {
    key: string;
    view: Exclude<RunView, 'run'>;
    title: string;
    subtitle: string;
    agentId: string;
    agent: string;
    automationId: string;
    automation: string;
    runs: ProductRun[];
    sessionCount: number;
    automationRunCount: number;
    runningCount: number;
    attentionCount: number;
    artifactCount: number;
    latestAt: string;
    earliestAt: string;
    errorSummary: string;
  };
  type GroupTimelineItem = {
    id: string;
    kind: 'run' | 'input' | 'output' | 'error' | 'artifact' | 'system' | 'end';
    level: string;
    title: string;
    message: string;
    at: string;
    runId: string;
    runTitle: string;
    runType: ProductRun['type'];
    source: string;
    detailTitle?: string;
    detail?: string;
    order?: number;
  };
  type ConversationTaskItem = {
    key: string;
    kind: ConversationTaskKind;
    title: string;
    subtitle: string;
    taskId: string;
    taskName: string;
    runs: ProductRun[];
    latestStatus: string;
    latestAt: string;
    todayCount: number;
    failedCount: number;
    artifactCount: number;
    recentArtifactCount: number;
    nextTriggerAt: string;
    deletedTask: boolean;
  };
  type TaskExecutionTimelineItem = {
    id: string;
    run: ProductRun;
    at: string;
    status: string;
    title: string;
    input: string;
    output: string;
    artifactSummary: string;
    artifactCount: number;
    error: string;
    systemCount: number;
    nodes: GroupTimelineItem[];
  };
  type TimelineRunCard = {
    id: string;
    run: ProductRun;
    kind: 'manual' | 'task';
    title: string;
    status: string;
    startedAt: string;
    endedAt: string;
    at: string;
    input: string;
    output: string;
    error: string;
    artifactCount: number;
    systemCount: number;
    errorEventCount: number;
    details: GroupTimelineItem[];
    searchText: string;
    matchTypes: TimelineFilter[];
  };
  type TimelineFilterCriteria = {
    taskId: string;
    timeRange: TimeFilter;
    status: string;
    query: string;
    filter: TimelineFilter;
    version?: number;
  };

  let loading = true;
  let error = '';
  let activeView: RunView = 'agent';
  let activeTab: 'all' | 'work_session' | 'automation_run' = 'all';
  let statusFilter = 'all';
  let agentFilter = '';
  let triggerFilter = '';
  let taskFilter = '';
  let keyword = '';
  let runs: ProductRun[] = [];
  let agentDefinitions: AgentDefinition[] = [];
  let automationTasks: AutomationTask[] = [];
  let selectedAgentId = '';
  let workbenchMode: WorkbenchMode = 'chat';
  let selectedConversationId = '';
  let selectedTaskId = '';
  let timelineQuery = '';
  let timelineTimeRange: TimeFilter = 'all';
  let timelineStatus = 'all';
  let selectedRunId = '';
  let activeMode: WorkbenchMode = 'timeline';
  let conversationId = '';
  let sidePanelOpen = false;
  let auxiliaryPanelKind: AuxiliaryPanelKind = 'none';
  let selectedGroupKey = '';
  let selectedContextKey = 'overview';
  let agentKeyword = '';
  let agentStatusFilter: AgentStatusFilter = 'all';
  let agentSort: AgentSort = 'recent';
  let runSourceFilter: RunSourceFilter = 'all';
  let runTimeFilter: TimeFilter = 'all';
  let messageText = '';
  let activeDetailTab: 'result' | 'input' | 'events' | 'artifacts' = 'result';
  let activeGroupTab: GroupTab = 'runs';
  let timelineFilter: TimelineFilter = 'all';
  let timelineVisibleLimit = 100;
  let timelineSearchCursor = 0;
  let timelineControlVersion = 0;
  let pendingLegacyRunIdMode = false;
  let showAllTaskTimeline = false;
  let sendingMessage = false;
  let detailLoading = false;
  let groupDetailLoading = false;
  let sessionAction: { runId: string; type: 'stop' | 'resume' } | null = null;
  let automationActionRunId = '';
  let runAgent: AgentDefinition | null = null;
  let runWorkspaceMode: 'empty' | 'file' | 'git' = 'empty';
  let runWorkspaceId = '';
  let runTask = '';
  let running = false;
  let workspaces: WorkspacePreset[] = [];
  let message = '';
  let messageTimer: ReturnType<typeof setTimeout> | null = null;
  let copiedId = '';
  let copyNotice: { text: string; ok: boolean } | null = null;
  let watchAbort: AbortController | null = null;
  let messageAbort: AbortController | null = null;
  let copiedTimer: ReturnType<typeof setTimeout> | null = null;
  let pendingCellChunks = new Map<string, string>();
  let messageScroll: HTMLDivElement | null = null;
  let messageScrollFrame = 0;
  let terminalVisibleIds = new Set<string>();
  const terminalHeights = new Map<string, number>();
  const terminalNodeIds = new Map<Element, string>();
  let terminalObserver: IntersectionObserver | null = null;
  let terminalObserverRoot: HTMLDivElement | null = null;
  let loadedGroupKeys = new Set<string>();
  let groupLoadToken = 0;
  let manualConversationVisible = 10;
  let sessionOffset = 0;
  let sessionHasMore = false;
  const terminalTheme = {
    background: '#07111a',
    foreground: '#d8e2ec',
    cursor: '#ffbf69',
    selectionBackground: 'rgba(255, 191, 105, 0.28)',
  };
  $: visibleRuns = filterRuns(runs, {
    tab: activeTab,
    status: statusFilter,
    agent: agentFilter,
    trigger: triggerFilter,
    task: taskFilter,
    keyword,
  });
  $: agentObservations = buildAgentObservations(runs);
  $: filteredAgentObservations = filterAgentObservations(agentObservations);
  $: selectedAgentObservation = filteredAgentObservations.find((agent) => agent.key === selectedAgentId) || filteredAgentObservations[0] || null;
  $: currentAgentRuns = selectedAgentObservation ? filterAgentRuns(selectedAgentObservation.runs) : [];
  $: currentAgentBaseRuns = selectedAgentObservation ? filterAgentRuns(selectedAgentObservation.runs, { ignoreTimeline: true }) : [];
  $: currentAgentTasks = selectedAgentObservation ? automationTasks.filter((task) => task.agentId === selectedAgentObservation.agentId) : [];
  $: conversationTaskItems = selectedAgentObservation ? buildConversationTaskItems(selectedAgentObservation, currentAgentBaseRuns, currentAgentTasks) : [];
  $: selectedConversationTaskItem = conversationTaskItems.find((item) => item.key === selectedContextKey) || conversationTaskItems[0] || null;
  $: manualConversationItems = conversationTaskItems.filter((item) => item.kind === 'manual_conversation');
  $: automationTaskItems = conversationTaskItems.filter((item) => item.kind === 'automation_task');
  $: selectedRun = selectedRunId ? currentAgentBaseRuns.find((run) => run.id === selectedRunId) || selectedAgentObservation?.runs.find((run) => run.id === selectedRunId) || null : null;
  $: selectedChatRun = resolveSelectedChatRun(workbenchMode, selectedConversationId, selectedRunId, selectedAgentObservation, currentAgentRuns, currentAgentBaseRuns);
  $: timelineCriteria = {
    taskId: selectedTaskId,
    timeRange: timelineTimeRange,
    status: timelineStatus,
    query: timelineQuery,
    filter: timelineFilter,
    version: timelineControlVersion,
  };
  $: timelineFiltersActive = Boolean(timelineQuery.trim() || timelineStatus !== 'all' || timelineTimeRange !== 'all' || timelineFilter !== 'all' || selectedTaskId);
  $: baseTimelineCards = buildTimelineRunCards(currentAgentBaseRuns);
  $: filteredTimelineRunCards = filterTimelineRunCards(baseTimelineCards, timelineCriteria);
  $: visibleTimelineRunCards = filteredTimelineRunCards.slice(0, timelineVisibleLimit);
  $: timelineRunDateGroups = groupTimelineRunCardsByDate(visibleTimelineRunCards);
  $: timelineSearchMatches = timelineSearchMatchCards(filteredTimelineRunCards, timelineQuery);
  $: recentManualConversationRuns = currentAgentBaseRuns.filter((run) => !run.automationId).slice(0, manualConversationVisible);
  $: taskModeRunCards = filterTimelineRunCards(baseTimelineCards, { ...timelineCriteria, filter: 'task' });
  $: visibleTaskModeRunCards = taskModeRunCards.slice(0, timelineVisibleLimit);
  $: taskModeDateGroups = groupTimelineRunCardsByDate(visibleTaskModeRunCards);
  $: selectedAutomationTaskItem = selectedTaskId ? automationTaskItems.find((item) => item.taskId === selectedTaskId) || null : null;
  $: auxiliaryRun = workbenchMode === 'chat' ? selectedChatRun : selectedRun;
  $: currentTimelineItems = selectedConversationTaskItem ? buildConversationItemTimeline(selectedConversationTaskItem) : [];
  $: visibleTimelineItems = filterTimelineItems(currentTimelineItems, timelineFilter);
  $: timelineDateGroups = groupTimelineByDate(visibleTimelineItems);
  $: taskExecutionTimelineItems = selectedConversationTaskItem?.kind === 'automation_task' ? buildTaskExecutionTimeline(selectedConversationTaskItem, timelineFilter) : [];
  $: visibleTaskExecutionTimelineItems = showAllTaskTimeline ? taskExecutionTimelineItems : taskExecutionTimelineItems.slice(0, 30);
  $: currentArtifactItems = currentTimelineItems.filter((item) => item.kind === 'artifact');
  $: selectedGroup = selectedAgentObservation ? agentObservationToGroup(selectedAgentObservation, currentAgentRuns) : null;
  $: groupTimelineItems = selectedGroup ? buildGroupTimeline(selectedGroup) : [];
  $: groupArtifactItems = selectedGroup ? buildGroupArtifacts(selectedGroup) : [];
  $: agentOptions = buildAgentOptions(agentDefinitions, runs);
  $: syncTerminalObserverRoot(messageScroll);
  onMount(() => {
    syncFromURL();
    void load();
    const handleVisible = () => {
      if (document.visibilityState === 'visible') {
        resumeVisibleWatch();
        if (!loading) {
          void load();
        }
      } else {
        stopWatching();
      }
    };
    const refreshOnFocus = () => {
      if (document.visibilityState === 'visible' && !loading) {
        resumeVisibleWatch();
        void load();
      }
    };
    window.addEventListener('popstate', syncFromURL);
    window.addEventListener('focus', refreshOnFocus);
    document.addEventListener('visibilitychange', handleVisible);
    return () => {
      window.removeEventListener('popstate', syncFromURL);
      window.removeEventListener('focus', refreshOnFocus);
      document.removeEventListener('visibilitychange', handleVisible);
    };
  });

  onDestroy(() => {
    stopWatching();
    stopSendingMessage();
    if (copiedTimer) {
      clearTimeout(copiedTimer);
    }
    if (messageTimer) {
      clearTimeout(messageTimer);
    }
    if (messageScrollFrame) {
      cancelAnimationFrame(messageScrollFrame);
    }
    terminalObserver?.disconnect();
    terminalObserver = null;
  });

  function tabFromValue(value: string | null): 'all' | 'work_session' | 'automation_run' {
    if (value === 'work_session' || value === 'automation_run') {
      return value;
    }
    return 'all';
  }

  function viewFromValue(value: string | null): RunView {
    if (value === 'agent' || value === 'task' || value === 'agent_task') {
      return value;
    }
    return 'run';
  }

  function detailTabFromValue(value: string | null): typeof activeDetailTab {
    if (value === 'input' || value === 'events' || value === 'artifacts') {
      return value;
    }
    return 'result';
  }

  function groupTabFromValue(value: string | null): GroupTab {
    if (value === 'timeline' || value === 'artifacts' || value === 'automation') {
      return value;
    }
    return 'runs';
  }

  function workbenchModeFromValue(value: string | null): WorkbenchMode {
    if (value === 'chat' || value === 'tasks' || value === 'timeline') {
      return value;
    }
    if (value === 'artifacts') return 'timeline';
    return 'chat';
  }

  function timelineFilterFromValue(value: string | null): TimelineFilter {
    if (value === 'input' || value === 'output') return 'io';
    if (value === 'system') return 'all';
    if (value === 'manual' || value === 'task' || value === 'io' || value === 'error' || value === 'artifact') {
      return value;
    }
    return 'all';
  }

  function timeFilterFromValue(value: string | null): TimeFilter {
    if (value === 'today' || value === '7d' || value === '30d') return value;
    return 'all';
  }

  function agentStatusFilterFromValue(value: string | null): AgentStatusFilter {
    if (value === 'running' || value === 'failed' || value === 'active' || value === 'empty') return value;
    return 'all';
  }

  function agentSortFromValue(value: string | null): AgentSort {
    if (value === 'failed' || value === 'name') return value;
    return 'recent';
  }

  function syncFromURL(): void {
    const params = currentQueryParams();
    activeView = 'agent';
    activeTab = tabFromValue(params.get('type'));
    pendingLegacyRunIdMode = false;
    timelineStatus = normalizeStatusFilter(activeTab, params.get('status') || 'all');
    statusFilter = timelineStatus;
    const legacyView = params.get('view') || '';
    const rawAgentId = params.get('agentId') || params.get('groupKey') || params.get('agent') || '';
    agentFilter = rawAgentId;
    triggerFilter = normalizeTriggerFilter(params.get('trigger') || '');
    selectedTaskId = params.get('taskId') || '';
    taskFilter = selectedTaskId;
    timelineQuery = params.get('q') || '';
    keyword = timelineQuery;
    const requestedMode = params.get('mode');
    workbenchMode = workbenchModeFromValue(requestedMode);
    activeMode = workbenchMode;
    selectedConversationId = params.get('conversationId') || '';
    conversationId = selectedConversationId;
    const requestedSessionId = params.get('sessionId') || '';
    const requestedRunId = params.get('runId') || '';
    selectedRunId = workbenchMode === 'chat'
      ? (selectedConversationId || requestedSessionId || requestedRunId)
      : (requestedRunId || requestedSessionId || selectedConversationId);
    selectedAgentId = rawAgentId;
    selectedGroupKey = selectedAgentId;
    selectedContextKey = selectedTaskId ? `task:${selectedTaskId}` : (selectedRunId ? '' : 'overview');
    activeDetailTab = detailTabFromValue(params.get('detailTab'));
    const rawGroupTab = params.get('groupTab') || params.get('tab');
    activeGroupTab = 'runs';
    timelineFilter = rawGroupTab === 'events' ? 'system' : timelineFilterFromValue(params.get('timelineFilter') || params.get('eventFilter') || params.get('source'));
    if (requestedMode === 'artifacts') {
      timelineFilter = 'artifact';
    }
    runSourceFilter = 'all';
    timelineTimeRange = timeFilterFromValue(params.get('time'));
    runTimeFilter = timelineTimeRange;
    agentKeyword = params.get('agentQ') || '';
    agentStatusFilter = agentStatusFilterFromValue(params.get('agentStatus'));
    agentSort = agentSortFromValue(params.get('agentSort'));
    if (legacyView === 'task' && !selectedTaskId) {
      selectedTaskId = params.get('groupKey') || '';
      taskFilter = selectedTaskId;
      selectedAgentId = '';
      selectedGroupKey = '';
    }
    if (legacyView === 'agent_task') {
      const groupKey = params.get('groupKey') || '';
      const [agentKey, taskKey] = groupKey.split(':');
      selectedAgentId = agentKey || selectedAgentId;
      selectedGroupKey = selectedAgentId;
      selectedTaskId = selectedTaskId || (taskKey === 'manual' ? '' : taskKey);
      taskFilter = selectedTaskId;
    }
    if (!params.has('mode') && !selectedConversationId && selectedRunId && !selectedTaskId) {
      pendingLegacyRunIdMode = true;
    }
    if (workbenchMode === 'chat' && selectedRunId) {
      selectedConversationId = selectedRunId;
      conversationId = selectedConversationId;
    }
    if (legacyView || params.has('groupKey') || params.has('agent') || params.has('mock') || params.has('type') || params.has('source') || params.has('eventFilter')) {
      updateURL(true);
    }
  }

  function updateURL(replace = true): void {
    updateQueryParams({
      view: null,
      type: activeTab === 'all' ? null : activeTab,
      status: timelineStatus === 'all' ? null : timelineStatus,
      agent: null,
      agentId: selectedAgentId || null,
      mode: workbenchMode,
      conversationId: workbenchMode === 'chat' ? (selectedConversationId || selectedRunId || null) : null,
      trigger: triggerFilter,
      taskId: selectedTaskParam(),
      source: null,
      time: timelineTimeRange === 'all' ? null : timelineTimeRange,
      agentQ: agentKeyword,
      agentStatus: agentStatusFilter === 'all' ? null : agentStatusFilter,
      agentSort: agentSort === 'recent' ? null : agentSort,
      q: timelineQuery.trim() || null,
      mock: null,
      runId: selectedRunParam(),
      sessionId: selectedSessionParam(),
      detailTab: null,
      groupKey: null,
      groupTab: null,
      timelineFilter: workbenchMode === 'timeline' && timelineFilter !== 'all' ? timelineFilter : null,
      tab: null,
      eventFilter: null,
    }, replace);
  }

  function applyFilters(replace = true): void {
    timelineVisibleLimit = 100;
    timelineSearchCursor = 0;
    timelineControlVersion += 1;
    mirrorTimelineState();
    updateURL(replace);
  }

  function selectedTaskParam(): string | null {
    if (workbenchMode === 'chat') return null;
    return selectedTaskId || null;
  }

  function selectedRunParam(): string | null {
    if (workbenchMode === 'chat') return null;
    if (!selectedRunId) return null;
    return selectedRunForURL()?.type === 'automation_run' ? selectedRunId : null;
  }

  function selectedSessionParam(): string | null {
    if (!selectedRunId) return null;
    if (workbenchMode === 'chat') return selectedConversationId || selectedRunId;
    return selectedRunForURL()?.type === 'work_session' ? selectedRunId : null;
  }

  function selectedRunForURL(): ProductRun | null {
    if (!selectedRunId) return null;
    return runs.find((run) => run.id === selectedRunId) || selectedRun || selectedChatRun || null;
  }

  function mirrorTimelineState(): void {
    activeMode = workbenchMode;
    conversationId = selectedConversationId;
    selectedGroupKey = selectedAgentId;
    taskFilter = selectedTaskId;
    keyword = timelineQuery;
    runTimeFilter = timelineTimeRange;
    statusFilter = timelineStatus;
  }

  function syncSelectionWithFilters(): void {
    syncAgentSelectionWithFilters();
  }

  function applyAgentFilters(replace = false): void {
    syncAgentSelectionWithFilters();
    updateURL(replace);
  }

  function syncAgentSelectionWithFilters(): void {
    const agents = filterAgentObservations(buildAgentObservations(runs));
    const currentAgent = agents.find((agent) => agent.key === selectedAgentId) || agents[0] || null;
    selectedAgentId = currentAgent?.key || '';
    selectedGroupKey = selectedAgentId;
    if (!currentAgent) {
      selectedContextKey = 'overview';
      selectedRunId = '';
      selectedConversationId = '';
      conversationId = '';
      return;
    }
    const items = buildConversationTaskItems(currentAgent, filterAgentRuns(currentAgent.runs, { ignoreTimeline: true }), tasksForAgent(currentAgent));
    const currentItem = items.find((item) => item.key === selectedContextKey) || null;
    if (!currentItem && selectedTaskId) {
      selectedContextKey = `task:${selectedTaskId}`;
      return;
    }
    if (!currentItem) {
      selectedContextKey = 'overview';
      if (workbenchMode !== 'chat') {
        selectedRunId = '';
      }
      return;
    }
    selectedContextKey = currentItem?.key || 'overview';
    if (!currentItem || currentItem.kind === 'overview') {
      if (workbenchMode !== 'chat') {
        selectedRunId = '';
      }
      selectedTaskId = '';
      taskFilter = '';
      return;
    }
    if (currentItem.kind === 'manual_conversation') {
      selectedRunId = currentItem.runs[0]?.id || '';
      selectedConversationId = selectedRunId;
      conversationId = selectedConversationId;
      selectedTaskId = '';
      taskFilter = '';
    } else {
      selectedTaskId = currentItem.taskId;
      taskFilter = selectedTaskId;
      if (selectedRunId && !currentItem.runs.some((run) => run.id === selectedRunId)) {
        selectedRunId = '';
      }
    }
    const currentRun = selectedRunId ? currentItem.runs.find((run) => run.id === selectedRunId) : null;
    if (currentRun) void loadRunDetail(currentRun);
  }

  function clearFilters(): void {
    activeTab = 'all';
    agentFilter = '';
    triggerFilter = '';
    selectedTaskId = '';
    taskFilter = '';
    timelineQuery = '';
    keyword = '';
    selectedContextKey = 'overview';
    selectedRunId = '';
    runSourceFilter = 'all';
    timelineTimeRange = 'all';
    runTimeFilter = 'all';
    timelineStatus = 'all';
    statusFilter = 'all';
    agentKeyword = '';
    agentStatusFilter = 'all';
    agentSort = 'recent';
    timelineFilter = 'all';
    timelineVisibleLimit = 100;
    applyFilters();
  }

  function buildAgentObservations(sourceRuns: ProductRun[]): AgentObservation[] {
    const agentById = new Map(agentDefinitions.map((agent) => [agent.id, agent]));
    const agentIdByName = new Map<string, string>();
    for (const agent of agentDefinitions) {
      if (agent.name) agentIdByName.set(agent.name, agent.id);
    }
    const observations = new Map<string, AgentObservation>();
    for (const agent of agentDefinitions) {
      observations.set(agent.id, {
        key: agent.id,
        agentId: agent.id,
        name: agent.name || agent.id,
        description: agent.description,
        deleted: Boolean(agent.deletedAt),
        enabled: agent.enabled,
        status: agent.deletedAt ? '已删除' : agent.enabled ? agent.availability : '已停用',
        runs: [],
        runningCount: 0,
        totalCount: 0,
        latestStatus: '无运行',
        latestAt: '',
        failedCount: 0,
        taskCount: automationTasks.filter((task) => task.agentId === agent.id).length,
      });
    }
    for (const run of sourceRuns) {
      let key = runAgentGroupKey(run);
      let knownAgent = run.agentId ? agentById.get(run.agentId) : null;
      if (!knownAgent && run.agent && run.agent !== '-') {
        const matchedId = agentIdByName.get(run.agent);
        if (matchedId) {
          key = matchedId;
          knownAgent = agentById.get(matchedId) || null;
        }
      }
      const current = observations.get(key) || {
        key,
        agentId: run.agentId,
        name: knownAgent?.name || run.agent || run.agentId || '已删除智能体',
        description: knownAgent?.description || '',
        deleted: !knownAgent && Boolean(run.agentId),
        enabled: knownAgent?.enabled ?? false,
        status: knownAgent ? (knownAgent.enabled ? knownAgent.availability : '已停用') : '已删除',
        runs: [],
        runningCount: 0,
        totalCount: 0,
        latestStatus: '无运行',
        latestAt: '',
        failedCount: 0,
        taskCount: run.agentId ? automationTasks.filter((task) => task.agentId === run.agentId).length : 0,
      };
      current.runs = [...current.runs, run].sort(compareRunTimeDesc);
      observations.set(key, current);
    }
    return Array.from(observations.values()).map((agent) => {
      const latest = agent.runs[0] || null;
      return {
        ...agent,
        totalCount: agent.runs.length,
        runningCount: agent.runs.filter((run) => ['等待中', '运行中'].includes(run.status)).length,
        latestStatus: latest?.status || '无运行',
        latestAt: latest?.completedAt || latest?.startedAt || '',
        failedCount: agent.runs.filter((run) => ['启动失败', '失败', '跳过', '已取消'].includes(run.status)).length,
      };
    }).sort(compareAgentObservation);
  }

  function compareAgentObservation(left: AgentObservation, right: AgentObservation): number {
    if (agentSort === 'failed') {
      const failed = right.failedCount - left.failedCount;
      if (failed !== 0) return failed;
      return compareDateDesc(left.latestAt, right.latestAt);
    }
    if (agentSort === 'name') {
      return left.name.localeCompare(right.name, 'zh-Hans-CN');
    }
    return compareDateDesc(left.latestAt, right.latestAt);
  }

  function filterAgentObservations(source: AgentObservation[]): AgentObservation[] {
    const query = agentKeyword.trim().toLowerCase();
    return source.filter((agent) => {
      const keywordMatch = !query || `${agent.name} ${agent.description} ${agent.agentId}`.toLowerCase().includes(query);
      const statusMatch =
        agentStatusFilter === 'all' ||
        (agentStatusFilter === 'running' && agent.runningCount > 0) ||
        (agentStatusFilter === 'failed' && agent.failedCount > 0) ||
        (agentStatusFilter === 'active' && isRecent(agent.latestAt, 7)) ||
        (agentStatusFilter === 'empty' && agent.totalCount === 0);
      return keywordMatch && statusMatch;
    });
  }

  function filterAgentRuns(source: ProductRun[], options: { ignoreTimeline?: boolean } = {}): ProductRun[] {
    return source.filter((run) => {
      if (options.ignoreTimeline) return true;
      const sourceMatch =
        runSourceFilter === 'all' ||
        (runSourceFilter === 'manual' && !run.automationId) ||
        (runSourceFilter === 'task' && Boolean(run.automationId));
      const statusMatch = matchesRunStatusFilter(run, timelineStatus);
      const triggerMatch = !triggerFilter || runTriggerKey(run) === triggerFilter;
      const taskMatch = !selectedTaskId || run.automationId === selectedTaskId;
      const timeMatch = matchesTimeFilter(run, timelineTimeRange);
      const query = timelineQuery.trim().toLowerCase();
      const keywordMatch = !query || runSearchText(run).includes(query);
      return sourceMatch && statusMatch && triggerMatch && taskMatch && timeMatch && keywordMatch;
    }).sort(compareRunTimeDesc);
  }

  function matchesTimeFilter(run: ProductRun, filter: TimeFilter): boolean {
    if (filter === 'all') return true;
    const at = new Date(run.startedAt || run.completedAt || 0).getTime();
    if (!at) return false;
    const now = new Date();
    if (filter === 'today') {
      const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
      return at >= start;
    }
    const days = filter === '7d' ? 7 : 30;
    return at >= Date.now() - days * 24 * 60 * 60 * 1000;
  }

  function isRecent(value: string, days: number): boolean {
    const at = new Date(value || 0).getTime();
    return Boolean(at && at >= Date.now() - days * 24 * 60 * 60 * 1000);
  }

  function agentObservationToGroup(agent: AgentObservation, sourceRuns: ProductRun[]): RunGroup {
    const latestAt = latestRunTime(sourceRuns);
    return {
      key: agent.key,
      view: 'agent',
      title: agent.name,
      subtitle: agent.agentId ? `智能体 ${agent.agentId}` : '已删除智能体',
      agentId: agent.agentId,
      agent: agent.name,
      automationId: '',
      automation: '',
      runs: sourceRuns,
      sessionCount: sourceRuns.filter((run) => run.type === 'work_session').length,
      automationRunCount: sourceRuns.filter((run) => run.type === 'automation_run').length,
      runningCount: sourceRuns.filter((run) => ['等待中', '运行中'].includes(run.status)).length,
      attentionCount: sourceRuns.filter((run) => ['启动失败', '失败', '跳过', '已取消'].includes(run.status)).length,
      artifactCount: sourceRuns.reduce((total, run) => total + run.artifacts.length, 0),
      latestAt,
      earliestAt: earliestRunTime(sourceRuns),
      errorSummary: sourceRuns.find((run) => run.errorSummary)?.errorSummary || '',
    };
  }

  function buildAgentTimeline(agent: AgentObservation, sourceRuns: ProductRun[]): GroupTimelineItem[] {
    return buildGroupTimeline(agentObservationToGroup(agent, sourceRuns));
  }

  function tasksForAgent(agent: AgentObservation): AutomationTask[] {
    return automationTasks.filter((task) => task.agentId === agent.agentId);
  }

  function buildConversationTaskItems(agent: AgentObservation, sourceRuns: ProductRun[], tasks: AutomationTask[]): ConversationTaskItem[] {
    const sortedRuns = [...sourceRuns].sort(compareRunTimeDesc);
    const latest = sortedRuns[0] || null;
    const items: ConversationTaskItem[] = [{
      key: 'overview',
      kind: 'overview',
      title: '全部运行记录',
      subtitle: `${manualRunCount(sortedRuns)} 个手动对话 · ${automationTaskRunCount(sortedRuns)} 次任务触发运行`,
      taskId: '',
      taskName: '',
      runs: sortedRuns,
      latestStatus: latest?.status || '无运行',
      latestAt: latest?.completedAt || latest?.startedAt || '',
      todayCount: sortedRuns.filter((run) => isToday(run.startedAt || run.completedAt)).length,
      failedCount: failedRunCount(sortedRuns),
      artifactCount: sortedRuns.reduce((total, run) => total + run.artifacts.length, 0),
      recentArtifactCount: latest?.artifacts.length || 0,
      nextTriggerAt: '',
      deletedTask: false,
    }];

    if (runSourceFilter !== 'task') {
      for (const run of sortedRuns.filter((item) => !item.automationId)) {
        items.push({
          key: `manual:${run.id}`,
          kind: 'manual_conversation',
          title: manualConversationTitle(run),
          subtitle: `${run.status} · ${formatTime(run.startedAt)} · #${shortRunId(run.id)}`,
          taskId: '',
          taskName: '',
          runs: [run],
          latestStatus: run.status,
          latestAt: run.completedAt || run.startedAt,
          todayCount: isToday(run.startedAt || run.completedAt) ? 1 : 0,
          failedCount: failedRunCount([run]),
          artifactCount: run.artifacts.length,
          recentArtifactCount: run.artifacts.length,
          nextTriggerAt: '',
          deletedTask: false,
        });
      }
    }

    if (runSourceFilter !== 'manual') {
      const taskRuns = new Map<string, ProductRun[]>();
      for (const run of sortedRuns.filter((item) => item.automationId)) {
        taskRuns.set(run.automationId, [...(taskRuns.get(run.automationId) || []), run]);
      }
      const taskIds = new Set<string>([
        ...tasks.map((task) => task.id),
        ...Array.from(taskRuns.keys()),
      ]);
      for (const taskId of taskIds) {
        const task = tasks.find((item) => item.id === taskId) || null;
        const runsForTask = [...(taskRuns.get(taskId) || [])].sort(compareRunTimeDesc);
        const latestRun = runsForTask[0] || null;
        const name = task?.name || latestRun?.automation || taskId;
        items.push({
          key: `task:${taskId}`,
          kind: 'automation_task',
          title: name,
          subtitle: automationTaskSubtitle(runsForTask, task),
          taskId,
          taskName: name,
          runs: runsForTask,
          latestStatus: latestRun?.status || (task?.enabled ? '等待触发' : '停用调度'),
          latestAt: latestRun?.completedAt || latestRun?.startedAt || task?.latestRunAt || '',
          todayCount: runsForTask.filter((run) => isToday(run.startedAt || run.completedAt)).length,
          failedCount: failedRunCount(runsForTask),
          artifactCount: runsForTask.reduce((total, run) => total + run.artifacts.length, 0),
          recentArtifactCount: latestRun?.artifacts.length || 0,
          nextTriggerAt: taskNextTriggerAt(task),
          deletedTask: !task && Boolean(taskId),
        });
      }
    }

    const overview = items[0];
    if (!overview) return [];
    return [
      overview,
      ...items.slice(1).sort((left, right) => {
        if (left.kind !== right.kind) return left.kind === 'manual_conversation' ? -1 : 1;
        return compareDateDesc(left.latestAt, right.latestAt);
      }),
    ];
  }

  function manualRunCount(sourceRuns: ProductRun[]): number {
    return sourceRuns.filter((run) => !run.automationId).length;
  }

  function automationTaskRunCount(sourceRuns: ProductRun[]): number {
    return sourceRuns.filter((run) => Boolean(run.automationId)).length;
  }

  function failedRunCount(sourceRuns: ProductRun[]): number {
    return sourceRuns.filter((run) => ['启动失败', '失败', '跳过', '已取消'].includes(run.status)).length;
  }

  function isToday(value: string): boolean {
    const date = new Date(value || 0);
    if (Number.isNaN(date.getTime())) return false;
    const now = new Date();
    return date.getFullYear() === now.getFullYear() && date.getMonth() === now.getMonth() && date.getDate() === now.getDate();
  }

  function manualConversationTitle(run: ProductRun): string {
    // Priority: first user message from loaded chat, then hydrated input,
    // then backend title (which for initial-task runs already contains the
    // initial message text), and finally a fallback label.
    const userMessage = run.messages.find((m) => m.role === 'user');
    const firstMsg = userMessage?.source || userMessage?.content || '';
    const sanitized = sanitizeTimelineSummary(firstMsg || run.input || '', 240);
    if (sanitized) return compactText(sanitized, 42);
    // No user message found — strip trailing "YYYY-MM-DD HH:mm" timestamp from
    // the backend title; what remains is either the agent name or the initial
    // task message (both are valid titles depending on the situation).
    const bareTitle = (run.title || '').replace(/\s+\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}$/, '');
    if (bareTitle && bareTitle !== run.id) return compactText(bareTitle, 42);
    return `手动对话 #${shortRunId(run.id)}`;
  }

  function manualConversationId(run: ProductRun): string {
    return run.type === 'work_session' && !run.automationId ? run.id : '';
  }

  function canContinueManualConversation(run: ProductRun): boolean {
    return Boolean(manualConversationId(run));
  }

  function automationTaskSubtitle(sourceRuns: ProductRun[], task: AutomationTask | null): string {
    const latest = sourceRuns[0] || null;
    const state = latest?.status || (task?.enabled ? '等待触发' : '停用调度');
    const latestAt = latest?.completedAt || latest?.startedAt || task?.latestRunAt || '';
    return `${state} · 今日 ${sourceRuns.filter((run) => isToday(run.startedAt || run.completedAt)).length} 次 · 最近 ${formatTime(latestAt)}`;
  }

  function taskNextTriggerAt(task: AutomationTask | null): string {
    if (!task) return '';
    const candidate = (task as AutomationTask & { nextRunAt?: string; nextTriggerAt?: string }).nextRunAt || (task as AutomationTask & { nextRunAt?: string; nextTriggerAt?: string }).nextTriggerAt || '';
    return candidate;
  }

  function buildConversationItemTimeline(item: ConversationTaskItem): GroupTimelineItem[] {
    if (item.kind === 'automation_task') return [];
    return buildRunsTimeline(item.runs);
  }

  function buildTaskExecutionTimeline(item: ConversationTaskItem, filter: TimelineFilter): TaskExecutionTimelineItem[] {
    if (item.kind !== 'automation_task') return [];
    return item.runs
      .map((run) => {
        const allNodes = buildRunsTimeline([run]);
        const visibleNodes = filterTimelineItems(allNodes, filter);
        if (filter !== 'all' && visibleNodes.length === 0) return null;
        return {
          id: run.id,
          run,
          at: run.completedAt || run.startedAt,
          status: run.status,
          title: executionTitle(run),
          input: runInputSummary(run),
          output: runOutputSummary(run),
          artifactSummary: artifactOutputSummary(run),
          artifactCount: runArtifactCount(run),
          error: sanitizeRunText(run.errorSummary, 220),
          systemCount: run.events.length,
          nodes: filter === 'all' ? allNodes : visibleNodes,
        };
      })
      .filter((item): item is TaskExecutionTimelineItem => Boolean(item))
      .sort((left, right) => compareDateDesc(left.at, right.at));
  }

  function buildTimelineRunCards(sourceRuns: ProductRun[]): TimelineRunCard[] {
    return sourceRuns
      .map((run) => {
        const details = buildRunsTimeline([run]);
        const input = runInputSummary(run);
        const output = runOutputSummary(run);
        const errorEventCount = run.events.filter((event) => {
          const tone = eventLevelTone(event.level);
          return tone === 'error' || tone === 'warning';
        }).length;
        const matchTypes: TimelineFilter[] = [run.automationId ? 'task' : 'manual'];
        if (input) matchTypes.push('input');
        if (output) matchTypes.push('output');
        if (runHasError(run)) matchTypes.push('error');
        if (runArtifactCount(run) > 0) matchTypes.push('artifact');
        if (runSystemEventCount(run) > 0) matchTypes.push('system');
        const title = run.automationId ? (taskNameForRun(run) || run.automation || run.automationId) : manualConversationTitle(run);
        const endedAt = runEndedAt(run);
        const searchText = runSearchText(run, { title, input, output });
        return {
          id: run.id,
          run,
          kind: run.automationId ? 'task' : 'manual',
          title,
          status: run.status,
          startedAt: run.startedAt,
          endedAt,
          at: endedAt || run.completedAt || run.startedAt,
          input,
          output,
          error: sanitizeRunText(run.errorSummary, 220),
          artifactCount: runArtifactCount(run),
          systemCount: runSystemEventCount(run),
          errorEventCount,
          details,
          searchText,
          matchTypes,
        };
      })
      .sort((left, right) => compareDateDesc(left.at, right.at));
  }

  function filterTimelineRunCards(cards: TimelineRunCard[], criteria: TimelineFilterCriteria): TimelineRunCard[] {
    const query = criteria.query.trim().toLowerCase();
    void criteria.version;
    return cards.filter((card) => {
      const taskMatch = !criteria.taskId || card.run.automationId === criteria.taskId;
      const timeMatch = matchesTimeFilter(card.run, criteria.timeRange);
      const statusMatch = matchesRunStatusFilter(card.run, criteria.status);
      const queryMatch = !query || card.searchText.includes(query);
      let typeMatch = true;
      if (criteria.filter === 'error') {
        typeMatch = card.matchTypes.includes('error');
      } else if (criteria.filter === 'io') {
        typeMatch = card.matchTypes.includes('input') || card.matchTypes.includes('output');
      } else if (criteria.filter !== 'all') {
        typeMatch = card.matchTypes.includes(criteria.filter);
      }
      return taskMatch && timeMatch && statusMatch && queryMatch && typeMatch;
    });
  }

  function groupTimelineRunCardsByDate(cards: TimelineRunCard[]): Array<{ key: string; label: string; items: TimelineRunCard[] }> {
    const groups = new Map<string, TimelineRunCard[]>();
    for (const card of cards) {
      const key = timelineDateKey(card.startedAt || card.at);
      groups.set(key, [...(groups.get(key) || []), card]);
    }
    return Array.from(groups.entries())
      .sort(([left], [right]) => right.localeCompare(left))
      .map(([key, groupItems]) => ({
        key,
        label: timelineDateLabel(key),
        items: groupItems.sort((left, right) => compareDateDesc(left.at, right.at)),
      }));
  }

  function timelineSearchMatchCards(cards: TimelineRunCard[], rawQuery: string): TimelineRunCard[] {
    const query = rawQuery.trim().toLowerCase();
    if (!query) return [];
    return cards.filter((card) => card.searchText.includes(query));
  }

  function runSearchText(run: ProductRun, extra: { title?: string; input?: string; output?: string } = {}): string {
    const artifactNames = run.artifacts.map((artifact) => readableArtifactName(artifact.name)).join(' ');
    const events = run.events.map((event) => `${event.type} ${event.level} ${event.message}`).join(' ');
    const messages = run.messages.map((message) => `${message.role} ${sanitizeRunText(message.source || '', 220)} ${sanitizeRunText(messageSummaryText(message), 220)}`).join(' ');
    return [
      run.id,
      shortRunId(run.id),
      run.title,
      run.agent,
      run.agentId,
      run.automation,
      run.automationId,
      taskNameForRun(run),
      sanitizeRunText(run.errorSummary, 220),
      sanitizeRunText(run.input, 220),
      sanitizeRunText(run.output, 220),
      extra.title,
      extra.input,
      extra.output,
      artifactNames,
      events,
      messages,
    ].filter(Boolean).join(' ').toLowerCase();
  }

  function runOutputTextSummary(run: ProductRun): string {
    const agentMessages = run.messages.filter((message) => message.role === 'agent');
    const lastAgent = agentMessages[agentMessages.length - 1];
    const text = run.output || (lastAgent ? messageSummaryText(lastAgent) : '');
    return sanitizeRunText(text, 260);
  }

  function runHasError(run: ProductRun): boolean {
    if (['启动失败', '失败', '跳过', '已取消'].includes(run.status) || Boolean(run.errorSummary)) return true;
    return run.events.some((event) => {
      const tone = eventLevelTone(event.level);
      return tone === 'error' || tone === 'warning';
    });
  }

  function runSystemEventCount(run: ProductRun): number {
    return run.eventCount || run.events.length;
  }

  function runEndedAt(run: ProductRun): string {
    if (['等待中', '运行中', '停止中...', '恢复中...'].includes(run.status)) return '';
    return run.completedAt;
  }

  function timelineCardTypeText(card: TimelineRunCard): string {
    return card.kind === 'manual' ? '手动对话' : '任务触发运行';
  }

  function timelineCardTimeRange(card: TimelineRunCard): string {
    const start = timelineTime(card.startedAt);
    const end = card.endedAt && card.endedAt !== card.startedAt ? timelineTime(card.endedAt) : '';
    return end ? `${start} - ${end}` : `${start} - 进行中`;
  }

  function timelineCardSummaryLine(card: TimelineRunCard): string {
    return `${timelineCardTypeText(card)}${card.status}  #${shortRunId(card.id)}`;
  }

  function detailRunForCard(card: TimelineRunCard): ProductRun {
    if (selectedRun?.id === card.id) return selectedRun;
    return currentAgentBaseRuns.find((run) => run.id === card.id) || runs.find((run) => run.id === card.id) || card.run;
  }

  function sortedRunEvents(run: ProductRun): ProductRun['events'] {
    return [...run.events].sort((left, right) => compareDateDesc(left.createdAt, right.createdAt));
  }

  function timelineTaskFilterOptions(): ConversationTaskItem[] {
    return automationTaskItems.filter((item) => item.kind === 'automation_task');
  }

  function executionTitle(run: ProductRun): string {
    if (['失败', '启动失败', '跳过', '已取消'].includes(run.status)) return `执行异常  #${shortRunId(run.id)}`;
    if (['等待中', '运行中', '停止中...', '恢复中...'].includes(run.status)) return `执行中  #${shortRunId(run.id)}`;
    return `执行成功  #${shortRunId(run.id)}`;
  }

  function artifactOutputSummary(run: ProductRun): string {
    if (run.artifacts.length === 0) return '';
    const names = run.artifacts
      .map((artifact) => readableArtifactName(artifact.name))
      .filter(Boolean)
      .slice(0, 2);
    if (names.length === 0) return `生成 ${run.artifacts.length} 个产出物`;
    const suffix = run.artifacts.length > names.length ? ` 等 ${run.artifacts.length} 个产出物` : '';
    return `生成 ${run.artifacts.length} 个产出物：${names.join('、')}${suffix}`;
  }

  function readableArtifactName(value: string): string {
    const name = (value || '').split(/[\\/]/).filter(Boolean).pop() || value || '';
    return compactText(name, 48);
  }

  function isPathLikeOutput(value: string): boolean {
    const text = value.trim();
    return text.startsWith('/') || text.includes('/root/') || text.includes('/data/') || text.includes('\\');
  }

  function shouldShowTimelineField(filter: TimelineFilter, kind: TimelineFilter): boolean {
    if (filter === 'io') return kind === 'input' || kind === 'output';
    return filter === 'all' || filter === kind;
  }

  function taskTimelineNodeLabel(item: GroupTimelineItem): string {
    return `${timelineKindLabel(item.kind)} · ${item.message || '-'}`;
  }

  function conversationItemKindLabel(kind: ConversationTaskKind): string {
    if (kind === 'manual_conversation') return '手动对话';
    if (kind === 'automation_task') return '自动化任务';
    return '概览';
  }

  function conversationItemTone(item: ConversationTaskItem): string {
    if (item.failedCount > 0) return 'red';
    if (item.runs.some((run) => ['等待中', '运行中'].includes(run.status))) return 'blue';
    if (item.latestStatus === '成功' || item.latestStatus === '已停止') return 'green';
    return 'gray';
  }

  function conversationDetailTitle(item: ConversationTaskItem | null): string {
    if (!item) return '时间线';
    if (item.kind === 'manual_conversation') return `手动对话：${item.title}`;
    if (item.kind === 'automation_task') return `自动化任务：${item.title}`;
    return '全部运行记录';
  }

  function conversationDetailDescription(item: ConversationTaskItem | null): string {
    if (!item) return '请选择一个对话或任务。';
    if (item.kind === 'manual_conversation') return '按时间展示这次手动对话的输入、输出、错误、产出物和系统事件。';
    if (item.kind === 'automation_task') return '聚合展示该任务在当前智能体下的执行情况和任务时间线。';
    return '覆盖当前智能体下所有手动对话和自动化任务触发运行。';
  }

  function recentExecutionRuns(item: ConversationTaskItem | null, filter: TimelineFilter): ProductRun[] {
    if (!item || item.kind !== 'automation_task') return [];
    const sourceRuns = filter === 'all'
      ? item.runs
      : item.runs.filter((run) => filterTimelineItems(buildRunsTimeline([run]), filter).length > 0);
    return sourceRuns.slice(0, 5);
  }

  function conversationEmptyTitle(item: ConversationTaskItem | null): string {
    if (!item) return '暂无内容';
    if (item.kind === 'automation_task') return '该任务还没有执行记录';
    if (item.kind === 'manual_conversation') return '这次手动对话暂无时间线内容';
    return '暂无时间线内容';
  }

  function agentTodayRunCount(agent: AgentObservation | null): number {
    return agent ? agent.runs.filter((run) => isToday(run.startedAt || run.completedAt)).length : 0;
  }

  function resolveSelectedChatRun(
    mode: WorkbenchMode,
    targetConversationId: string,
    targetRunId: string,
    agent: AgentObservation | null,
    filteredRuns: ProductRun[],
    baseRuns: ProductRun[],
  ): ProductRun | null {
    if (mode !== 'chat') return null;
    const targetId = targetConversationId || targetRunId;
    const source = agent?.runs || [];
    const explicit = targetId ? source.find((run) => run.id === targetId && !run.automationId) || null : null;
    if (explicit) return explicit;
    return baseRuns.find((run) => !run.automationId) || filteredRuns.find((run) => !run.automationId) || source.filter((run) => !run.automationId).sort(compareRunTimeDesc)[0] || null;
  }

  function preferredWorkbenchModeForAgent(agent: AgentObservation | null, sourceRuns: ProductRun[] = []): WorkbenchMode {
    if (sourceRuns.some((run) => !run.automationId)) return 'chat';
    if (sourceRuns.some((run) => Boolean(run.automationId)) || (agent?.taskCount || 0) > 0) return 'tasks';
    return 'timeline';
  }

  function setWorkbenchMode(mode: WorkbenchMode): void {
    if (workbenchMode === mode) return;
    workbenchMode = mode;
    if (mode !== 'chat') {
      selectedConversationId = '';
      conversationId = '';
      if (mode === 'tasks') {
        selectedContextKey = selectedTaskId ? `task:${selectedTaskId}` : 'overview';
        selectedRunId = '';
        auxiliaryPanelKind = 'none';
        sidePanelOpen = false;
      } else if (mode === 'timeline') {
        selectedContextKey = selectedTaskId ? `task:${selectedTaskId}` : 'overview';
        selectedRunId = '';
        auxiliaryPanelKind = 'none';
        sidePanelOpen = false;
      } else if (!selectedRunId) {
        selectedContextKey = selectedTaskId ? `task:${selectedTaskId}` : 'overview';
        auxiliaryPanelKind = 'none';
        sidePanelOpen = false;
      }
    } else {
      const run = selectedRun && !selectedRun.automationId ? selectedRun : recentManualConversationRuns[0] || null;
      if (run) {
        selectedRunId = run.id;
        selectedConversationId = manualConversationId(run);
        conversationId = selectedConversationId;
        selectedContextKey = `manual:${run.id}`;
        selectedTaskId = '';
        taskFilter = '';
        auxiliaryPanelKind = 'none';
        sidePanelOpen = false;
        void loadRunDetail(run);
      }
    }
    updateURL();
    if (mode === 'chat') {
      void scrollMessagesToBottom();
      scheduleMessageBottomCorrection();
    }
  }

  function openAuxiliaryPanel(kind: AuxiliaryPanelKind): void {
    auxiliaryPanelKind = kind;
    sidePanelOpen = true;
  }

  function selectTaskForTimeline(item: ConversationTaskItem): void {
    workbenchMode = 'tasks';
    activeMode = workbenchMode;
    selectedContextKey = item.key;
    selectedTaskId = item.taskId;
    taskFilter = selectedTaskId;
    clearTimelineRunSelection();
    timelineVisibleLimit = 100;
    auxiliaryPanelKind = 'none';
    sidePanelOpen = false;
    applyFilters();
    if (selectedGroup) void loadGroupDetail(selectedGroup);
  }

  function clearTaskFilter(): void {
    selectedTaskId = '';
    taskFilter = '';
    selectedContextKey = 'overview';
    selectedRunId = '';
    timelineVisibleLimit = 100;
    auxiliaryPanelKind = 'none';
    sidePanelOpen = false;
    applyFilters();
  }

  function selectTimelineTaskFilter(): void {
    activeMode = workbenchMode;
    selectedContextKey = selectedTaskId ? `task:${selectedTaskId}` : 'overview';
    clearTimelineRunSelection();
    auxiliaryPanelKind = 'none';
    sidePanelOpen = false;
    applyFilters();
  }

  function setTimelineQueryFromInput(event: Event): void {
    const target = event.currentTarget;
    timelineQuery = target instanceof HTMLInputElement ? target.value : '';
    clearTimelineRunSelection();
    applyFilters();
  }

  function setTimelineTimeRangeFromSelect(event: Event): void {
    const target = event.currentTarget;
    timelineTimeRange = timeFilterFromValue(target instanceof HTMLSelectElement ? target.value : '');
    clearTimelineRunSelection();
    applyFilters();
  }

  function setTimelineStatusFromSelect(event: Event): void {
    const target = event.currentTarget;
    timelineStatus = normalizeStatusFilter('all', target instanceof HTMLSelectElement ? target.value : 'all');
    clearTimelineRunSelection();
    applyFilters();
  }

  function setTimelineTaskFromSelect(event: Event): void {
    const target = event.currentTarget;
    selectedTaskId = target instanceof HTMLSelectElement ? target.value : '';
    selectTimelineTaskFilter();
  }

  function setTimelineFilterFromSelect(event: Event): void {
    const target = event.currentTarget;
    setTimelineFilter((target instanceof HTMLSelectElement ? target.value : 'all') as TimelineFilter);
  }

  function hasTimelineFilters(): boolean {
    return Boolean(
      timelineQuery.trim() ||
      timelineStatus !== 'all' ||
      timelineTimeRange !== 'all' ||
      timelineFilter !== 'all' ||
      selectedTaskId,
    );
  }

  function clearTimelineFilters(): void {
    timelineQuery = '';
    keyword = '';
    timelineStatus = 'all';
    statusFilter = 'all';
    timelineTimeRange = 'all';
    runTimeFilter = 'all';
    timelineFilter = 'all';
    selectedTaskId = '';
    taskFilter = '';
    selectedContextKey = 'overview';
    selectedRunId = '';
    selectedConversationId = '';
    conversationId = '';
    timelineVisibleLimit = 100;
    auxiliaryPanelKind = 'none';
    sidePanelOpen = false;
    applyFilters();
  }

  function focusCurrentRunning(): void {
    timelineStatus = 'running';
    statusFilter = timelineStatus;
    timelineFilter = 'all';
    timelineVisibleLimit = 100;
    clearTimelineRunSelection();
    applyFilters();
  }

  function focusRecentFailure(): void {
    timelineStatus = 'all';
    statusFilter = timelineStatus;
    timelineFilter = 'error';
    timelineVisibleLimit = 100;
    clearTimelineRunSelection();
    applyFilters();
  }

  function focusRecentArtifact(): void {
    timelineStatus = 'all';
    statusFilter = timelineStatus;
    timelineFilter = 'artifact';
    timelineVisibleLimit = 100;
    clearTimelineRunSelection();
    applyFilters();
  }

  function selectSearchMatch(direction: 'previous' | 'next'): void {
    if (timelineSearchMatches.length === 0) return;
    const nextCursor = direction === 'next'
      ? (timelineSearchCursor + 1) % timelineSearchMatches.length
      : (timelineSearchCursor - 1 + timelineSearchMatches.length) % timelineSearchMatches.length;
    timelineSearchCursor = nextCursor;
    openRunFromTimeline(timelineSearchMatches[nextCursor].run);
  }

  function selectAgentObservation(agent: AgentObservation): void {
    selectedAgentId = agent.key;
    selectedGroupKey = selectedAgentId;
    selectedRunId = '';
    selectedConversationId = '';
    conversationId = '';
    workbenchMode = preferredWorkbenchModeForAgent(agent, filterAgentRuns(agent.runs, { ignoreTimeline: true }));
    activeMode = workbenchMode;
    auxiliaryPanelKind = 'none';
    sidePanelOpen = false;
    selectedContextKey = 'overview';
    selectedTaskId = '';
    taskFilter = '';
    activeGroupTab = 'runs';
    syncAgentSelectionWithFilters();
    updateURL();
    void loadGroupDetail(agentObservationToGroup(agent, filterAgentRuns(agent.runs, { ignoreTimeline: true })));
  }

  function selectedRunIndex(): number {
    const runsForItem = selectedConversationTaskItem?.runs || currentAgentRuns;
    return runsForItem.findIndex((run) => run.id === selectedRunId);
  }

  function previousRun(): ProductRun | null {
    const runsForItem = selectedConversationTaskItem?.runs || currentAgentRuns;
    const index = selectedRunIndex();
    return index > 0 ? runsForItem[index - 1] : null;
  }

  function nextRun(): ProductRun | null {
    const runsForItem = selectedConversationTaskItem?.runs || currentAgentRuns;
    const index = selectedRunIndex();
    return index >= 0 && index < runsForItem.length - 1 ? runsForItem[index + 1] : null;
  }

  function selectAdjacentRun(direction: 'previous' | 'next'): void {
    const run = direction === 'previous' ? previousRun() : nextRun();
    if (run) {
      selectRun(run);
    }
  }

  function runSourceText(run: ProductRun): string {
    return run.automationId ? '任务触发运行' : '手动对话';
  }

  function taskNameForRun(run: ProductRun): string {
    if (!run.automationId) return '';
    const task = automationTasks.find((item) => item.id === run.automationId);
    return task?.name || run.automation || run.automationId;
  }

  function isRunTaskDeleted(run: ProductRun): boolean {
    return Boolean(run.automationId && !automationTasks.some((task) => task.id === run.automationId));
  }

  function runMessageCount(run: ProductRun): number {
    return run.messageCount || run.messages.length;
  }

  function runEventCount(run: ProductRun): number {
    return run.eventCount || run.events.length;
  }

  function runArtifactCount(run: ProductRun): number {
    return run.artifacts.length;
  }

  function runTitle(run: ProductRun): string {
    return run.title || run.input || run.id;
  }

  function shortRunId(id: string): string {
    const normalized = id.trim();
    if (!normalized) return '-';
    return normalized.length > 10 ? normalized.slice(0, 8) : normalized;
  }

  function isTempId(id: string): boolean {
    return id.startsWith('s-') && id.length < 20;
  }

  function runTriggerText(run: ProductRun): string {
    return run.trigger && run.trigger !== '-' ? run.trigger : '未标注触发规则';
  }

  function detailTabHasPrimaryContent(run: ProductRun): boolean {
    if (run.type === 'work_session') {
      return runMessages(run).length > 0;
    }
    return Boolean(run.output || run.errorSummary);
  }

  function preferredDetailTab(run: ProductRun): typeof activeDetailTab {
    if (detailTabHasPrimaryContent(run)) return 'result';
    if (run.events.length > 0) return 'events';
    if (run.artifacts.length > 0) return 'artifacts';
    return 'input';
  }

  function ensureUsefulDetailTab(run: ProductRun): void {
    if (activeDetailTab !== 'result') return;
    const nextTab = preferredDetailTab(run);
    if (nextTab !== activeDetailTab) {
      activeDetailTab = nextTab;
      updateURL(true);
    }
  }

  function agentStatusTone(agent: AgentObservation): string {
    if (agent.deleted) return 'gray';
    if (agent.failedCount > 0) return 'red';
    if (agent.runningCount > 0) return 'blue';
    if (agent.enabled) return 'green';
    return 'gray';
  }

  function goAgentDetail(agent: AgentObservation): void {
    if (!agent.agentId || agent.deleted) return;
    window.location.assign(appPath(`/agents?agent=${encodeURIComponent(agent.agentId)}`));
  }

  function goAgentManualRun(agent: AgentObservation): void {
    if (!agent.agentId || agent.deleted) return;
    const def = agentDefinitions.find((item) => item.id === agent.agentId);
    if (!def) {
      window.location.assign(appPath(`/agents?agent=${encodeURIComponent(agent.agentId)}`));
      return;
    }
    openRun(def);
  }

  function openRun(agent: AgentDefinition): void {
    runAgent = agent;
    runWorkspaceMode = agent.workFiles.source === 'git' || agent.workFiles.source === 'file' ? agent.workFiles.source : 'empty';
    runWorkspaceId = agent.workspaceId;
    runTask = '';
    running = false;
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

  function onRunWorkspaceModeChange(): void {
    if (runWorkspaceMode === 'empty') {
      runWorkspaceId = '';
      return;
    }
    if (!runWorkspaceId || workspaces.find((workspace) => workspace.id === runWorkspaceId)?.type !== runWorkspaceMode) {
      runWorkspaceId = workspaces.find((workspace) => workspace.type === runWorkspaceMode)?.id ?? '';
    }
  }

  async function runSelectedAgent(): Promise<void> {
    if (!runAgent) return;
    const agent = runAgent;
    const initialMessage = runTask.trim();
    const tempId = `s-${Date.now().toString(36)}`;
    const mark = new Date().toISOString();
    const pending: ProductRun = {
      id: tempId,
      title: initialMessage ? `${initialMessage} ${formatSessionTime(new Date())}` : `${agent.name} ${formatSessionTime(new Date())}`,
      type: 'work_session',
      status: '创建中...',
      agentId: agent.id,
      agent: agent.name,
      automationId: '',
      automation: '-',
      sourceSessionTags: [],
      trigger: '手动触发',
      capabilitySet: '',
      workspace: '',
      startedAt: mark,
      completedAt: mark,
      duration: '-',
      rawStatus: 'PENDING',
      agentProvider: agent.provider,
      messageCount: 0,
      eventCount: 0,
      errorSummary: '',
      output: '',
      input: initialMessage,
      messages: [],
      events: [],
      artifacts: [],
    };

    const ctx = {
      tempId,
      agent,
      initialMessage,
      pending,
      resolved: false,
      statusPollTimer: null as ReturnType<typeof setTimeout> | null,
      initialMessageSent: false,
    };

    running = true;
    error = '';
    message = '';

    function cleanup(): void {
      if (ctx.statusPollTimer) clearTimeout(ctx.statusPollTimer);
    }

    function resolve(): void {
      if (ctx.resolved) return;
      ctx.resolved = true;
      running = false;
      cleanup();
    }

    runs = sortRunsByUpdatedTime([pending, ...runs]);
    selectedAgentId = agent.id;
    selectedRunId = tempId;
    selectedConversationId = tempId;
    conversationId = tempId;
    workbenchMode = 'chat';
    activeMode = 'chat';
    selectedTaskId = '';
    taskFilter = '';
    selectedContextKey = `manual:${tempId}`;
    closeRun();

    function updateTemp(partial: Partial<ProductRun>): void {
      if (ctx.resolved) return;
      runs = sortRunsByUpdatedTime(runs.map((r) => r.id === tempId ? { ...r, ...partial } : r));
    }

    updateTemp({ status: '等待中', rawStatus: 'PENDING' });

    function startStatusFallbackPoll(sessionId: string): void {
      if (ctx.statusPollTimer) clearTimeout(ctx.statusPollTimer);
      async function statusPoll(): Promise<void> {
        try {
          const { sessions } = await listWorkSessions(50);
          const s = sessions.find((item) => item.id === sessionId);
          if (s) {
            const updated = sessionToRun(s);
            runs = sortRunsByUpdatedTime(runs.map((r) => r.id === sessionId
              ? mergeSessionUpdate(r, updated)
              : r));
            const key = s.status.toUpperCase();
            if (key === 'RUNNING' || key === 'STOPPED' || key === 'FAILED' || key === 'START_FAILED') {
              return;
            }
          }
        } catch { /* ignore */ }
        ctx.statusPollTimer = setTimeout(statusPoll, 3000);
      }
      ctx.statusPollTimer = setTimeout(statusPoll, 3000);
    }

    function sendInitialMessage(sessionId: string): void {
      if (ctx.initialMessageSent || !initialMessage) return;
      ctx.initialMessageSent = true;
      const provider = (agent.provider || 'codex').trim().toLowerCase();
      if (provider === 'claude' || provider === 'gemini' || provider === 'codex' || provider === 'opencode') {
        void sendWorkSessionMessageStream(sessionId, provider, initialMessage, () => {}).catch(() => {});
      }
    }

    // Fire RPC to create the session. When it returns with a sessionId we
    // swap out the temp entry. No blind polling — only the RPC knows the
    // exact sessionId, and matching on agent+timestamp races across
    // concurrent calls.
    void createAgentDefinitionSession({
      agentId: agent.id,
      title: initialMessage ? `${initialMessage} ${formatSessionTime(new Date())}` : `${agent.name} ${formatSessionTime(new Date())}`,
      workspaceId: runWorkspaceMode === 'git' || runWorkspaceMode === 'file' ? runWorkspaceId : '',
      driver: agent.driver || 'docker',
      guestImage: agent.guestImage,
      message: '',
      provider: agent.provider,
    }).then((sessionId) => {
      if (ctx.resolved || !sessionId) return;
      resolve();
      // The session just created may still be PENDING in listWorkSessions;
      // fetch it so we can update the temp entry and start status polling.
      listWorkSessions(50).then(({ sessions }) => {
        const real = sessions.find((s) => s.id === sessionId);
        runs = sortRunsByUpdatedTime(runs.map((r) => r.id === tempId
          ? real ? { ...pending, ...sessionToRun(real), id: sessionId, input: pending.input || sessionToRun(real).input } : { ...r, id: sessionId }
          : r));
        selectedRunId = sessionId;
        selectedConversationId = sessionId;
        conversationId = sessionId;
        selectedContextKey = `manual:${sessionId}`;
        const fresh = runs.find((r) => r.id === sessionId);
        if (fresh) {
          startWatching(fresh);
          startStatusFallbackPoll(sessionId);
        }
        message = `工作会话已创建：${sessionId}`;
        messageTimer = setTimeout(() => { message = ''; }, 5000);
        sendInitialMessage(sessionId);
      }).catch(() => {});
    }).catch(() => {
      // RPC failed — nothing we can do without a sessionId.
      resolve();
    });

    setTimeout(() => {
      if (!ctx.resolved) { resolve(); }
    }, 120_000);
  }

  function formatSessionTime(date: Date): string {
    const pad = (value: number) => String(value).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
  }

  function goTaskDetail(run: ProductRun): void {
    if (!run.automationId || isRunTaskDeleted(run)) return;
    window.location.assign(appPath(`/automation-tasks?task=${encodeURIComponent(run.automationId)}`));
  }

  function mergeSessionUpdate(existing: ProductRun, updated: ReturnType<typeof sessionToRun>): ProductRun {
    return {
      ...existing,
      ...updated,
      agentId: updated.agentId || existing.agentId,
      agent: updated.agent || existing.agent,
      agentProvider: existing.agentProvider || updated.agentProvider,
      automationId: updated.automationId || existing.automationId,
      automation: updated.automation && updated.automation !== '-' ? updated.automation : existing.automation,
      messages: existing.messages,
      events: existing.events,
      artifacts: existing.artifacts,
    };
  }

  type RunFilterCriteria = {
    tab: typeof activeTab;
    status: string;
    agent: string;
    trigger: string;
    task: string;
    keyword: string;
  };

  function currentFilterCriteria(): RunFilterCriteria {
    return {
      tab: activeTab,
      status: statusFilter,
      agent: agentFilter,
      trigger: triggerFilter,
      task: taskFilter,
      keyword,
    };
  }

  function filterRuns(source: ProductRun[], criteria: RunFilterCriteria): ProductRun[] {
    return source.filter((run) => matchesRunFilters(run, criteria));
  }

  function matchesRunFilters(run: ProductRun, criteria = currentFilterCriteria()): boolean {
    const tabMatch = criteria.tab === 'all' || run.type === criteria.tab;
    const statusMatch = matchesRunStatusFilter(run, criteria.status);
    const agentMatch = !criteria.agent || runAgentKey(run) === criteria.agent;
    const triggerMatch = !criteria.trigger || runTriggerKey(run) === criteria.trigger;
    const taskMatch = !criteria.task || run.automationId === criteria.task;
    const keywordMatch = !criteria.keyword.trim() || `${run.id} ${run.title} ${run.agent} ${run.automation} ${run.workspace}`.toLowerCase().includes(criteria.keyword.trim().toLowerCase());
    return tabMatch && statusMatch && agentMatch && triggerMatch && taskMatch && keywordMatch;
  }

  function matchesRunStatusFilter(run: ProductRun, status = statusFilter): boolean {
    if (status === 'all') return true;
    const key = runStatusKey(run);
    if (status === 'attention') {
      return key === 'failed' || key === 'skipped' || key === 'cancelled';
    }
    return key === status;
  }

  function runStatusKey(run: ProductRun): string {
    const raw = run.rawStatus.toUpperCase();
    if (run.type === 'work_session') {
      if (raw === 'STARTING') return 'starting';
      if (raw === 'RUNNING') return 'running';
      if (raw === 'STOPPED') return 'stopped';
      if (raw === 'FAILED' || raw === 'START_FAILED') return 'failed';
    }
    if (run.type === 'automation_run') {
      if (raw === 'PENDING') return 'pending';
      if (raw === 'RUNNING') return 'running';
      if (raw === 'SUCCEEDED' || raw === 'SUCCESS') return 'succeeded';
      if (raw === 'FAILED' || raw === 'FAILURE') return 'failed';
      if (raw === 'SKIPPED') return 'skipped';
      if (raw === 'CANCELED' || raw === 'CANCELLED') return 'cancelled';
    }
    if (run.status === '成功') return 'succeeded';
    if (run.status === '失败' || run.status === '启动失败') return 'failed';
    if (run.status === '已停止') return 'stopped';
    if (run.status === '已取消') return 'cancelled';
    return run.status;
  }

  function runTriggerKey(run: ProductRun): string {
    if (run.trigger === '手动触发') return 'manual';
    if (run.trigger === '定时触发') return 'cron';
    if (run.trigger === '周期触发') return 'interval';
    if (run.trigger === '事件触发') return 'event';
    if (run.trigger === '延迟触发') return 'timeout';
    return run.trigger || '';
  }

  function runAgentKey(run: ProductRun): string {
    return run.agentId || '';
  }

  function buildAgentOptions(definitions: AgentDefinition[], sourceRuns: ProductRun[]): Array<{ value: string; label: string }> {
    const agentById = new Map(definitions.map((agent) => [agent.id, agent]));
    const options = new Map<string, string>();
    for (const run of sourceRuns) {
      const agentId = runAgentKey(run);
      if (!agentId) continue;
      const agent = agentById.get(agentId);
      options.set(agentId, agent?.name || run.agent || agentId);
    }
    return Array.from(options, ([value, label]) => ({ value, label }))
      .sort((left, right) => left.label.localeCompare(right.label, 'zh-Hans-CN'));
  }

  function normalizeTriggerFilter(value: string): string {
    if (value === '手动触发') return 'manual';
    if (value === '定时触发') return 'cron';
    if (value === '周期触发') return 'interval';
    if (value === '事件触发') return 'event';
    if (value === '延迟触发') return 'timeout';
    return triggerOptions().some((option) => option.value === value) ? value : '';
  }

  function normalizeStatusFilter(tab: typeof activeTab, status: string): string {
    const value = legacyStatusFilterValue(tab, status || 'all');
    if (value === 'all') return 'all';
    if (tab === 'all') {
      return ['running', 'succeeded', 'failed', 'stopped', 'attention', 'cancelled'].includes(value) ? value : 'all';
    }
    if (value === 'attention') return 'attention';
    if (value === 'cancelled' && tab !== 'automation_run') return 'all';
    return statusOptionsForTab(tab).some((option) => option.value === value) ? value : 'all';
  }

  function legacyStatusFilterValue(tab: typeof activeTab, status: string): string {
    if (status === '运行中') return 'running';
    if (status === '已取消') return 'cancelled';
    if (status === '失败 / 异常') return 'attention';
    if (tab === 'work_session') {
      if (status === '等待中') return 'pending';
      if (status === '已停止') return 'stopped';
      if (status === '启动失败') return 'failed';
    }
    if (tab === 'automation_run') {
      if (status === '等待中') return 'pending';
      if (status === '成功') return 'succeeded';
      if (status === '失败') return 'failed';
      if (status === '跳过') return 'skipped';
    }
    if (status === '失败' || status === '启动失败' || status === '跳过') return 'attention';
    return status;
  }

  function uniqueOptions(values: string[]): string[] {
    return Array.from(new Set(values));
  }

  function typeOptions(): Array<{ value: 'all' | 'work_session' | 'automation_run'; label: string }> {
    return [
      { value: 'all', label: '全部类型' },
      { value: 'work_session', label: '运行记录' },
      { value: 'automation_run', label: '任务执行' },
    ];
  }

  function triggerOptions(): Array<{ value: string; label: string }> {
    return [
      { value: 'manual', label: '手动触发' },
      { value: 'cron', label: '定时触发' },
      { value: 'interval', label: '周期触发' },
      { value: 'event', label: '事件触发' },
      { value: 'timeout', label: '延迟触发' },
    ];
  }

  function statusOptions(): Array<{ value: string; label: string }> {
    return statusOptionsForTab(activeTab);
  }

  function statusOptionsForTab(tab: typeof activeTab): Array<{ value: string; label: string }> {
    if (tab === 'work_session') {
      return [
        { value: 'all', label: '全部状态' },
        { value: 'attention', label: '异常' },
        { value: 'pending', label: '等待中' },
        { value: 'running', label: '运行中' },
        { value: 'stopped', label: '已停止' },
        { value: 'failed', label: '启动失败' },
      ];
    }
    if (tab === 'automation_run') {
      return [
        { value: 'all', label: '全部状态' },
        { value: 'attention', label: '异常' },
        { value: 'pending', label: '等待中' },
        { value: 'running', label: '运行中' },
        { value: 'succeeded', label: '成功' },
        { value: 'failed', label: '失败' },
        { value: 'skipped', label: '跳过' },
        { value: 'cancelled', label: '已取消' },
      ];
    }
    return [
      { value: 'all', label: '全部状态' },
      { value: 'running', label: '运行中' },
      { value: 'succeeded', label: '成功' },
      { value: 'failed', label: '失败' },
      { value: 'stopped', label: '已停止' },
    ];
  }

  function runModeOptions(): Array<{ value: 'run' | 'group'; label: string }> {
    return [
      { value: 'run', label: '运行记录' },
      { value: 'group', label: '智能体观察' },
    ];
  }

  function groupDimensionOptions(): Array<{ value: Exclude<RunView, 'run'>; label: string }> {
    return [
      { value: 'agent', label: '按智能体' },
    ];
  }

  function timelineFilterOptions(): Array<{ value: TimelineFilter; label: string }> {
    return [
      { value: 'all', label: '全部' },
      { value: 'manual', label: '对话' },
      { value: 'task', label: '任务' },
      { value: 'io', label: '输入输出' },
      { value: 'error', label: '错误' },
      { value: 'artifact', label: '产出物' },
    ];
  }

  function detailTabOptions(): Array<{ value: typeof activeDetailTab; label: string }> {
    return [
      { value: 'result', label: '本次消息' },
      { value: 'events', label: '本次事件' },
      { value: 'artifacts', label: '本次产出' },
      { value: 'input', label: '运行信息' },
    ];
  }

  function buildRunGroups(source: ProductRun[], view: RunView): RunGroup[] {
    if (view === 'run') return [];
    const groups = new Map<string, RunGroup>();
    for (const run of source) {
      const groupSeed = groupSeedForRun(run, view);
      if (!groupSeed) continue;
      const current = groups.get(groupSeed.key) || {
        key: groupSeed.key,
        view,
        title: groupSeed.title,
        subtitle: groupSeed.subtitle,
        agentId: groupSeed.agentId,
        agent: groupSeed.agent,
        automationId: groupSeed.automationId,
        automation: groupSeed.automation,
        runs: [],
        sessionCount: 0,
        automationRunCount: 0,
        runningCount: 0,
        attentionCount: 0,
        artifactCount: 0,
        latestAt: '',
        earliestAt: '',
        errorSummary: '',
      };
      current.runs = [...current.runs, run].sort(compareRunTimeDesc);
      current.sessionCount = current.runs.filter((item) => item.type === 'work_session').length;
      current.automationRunCount = current.runs.filter((item) => item.type === 'automation_run').length;
      current.runningCount = current.runs.filter((item) => ['等待中', '运行中'].includes(item.status)).length;
      current.attentionCount = current.runs.filter((item) => ['启动失败', '失败', '跳过', '已取消'].includes(item.status)).length;
      current.artifactCount = current.runs.reduce((total, item) => total + item.artifacts.length, 0);
      current.latestAt = latestRunTime(current.runs);
      current.earliestAt = earliestRunTime(current.runs);
      current.errorSummary = current.runs.find((item) => item.errorSummary)?.errorSummary || '';
      groups.set(groupSeed.key, current);
    }
    return Array.from(groups.values()).sort((left, right) => compareDateDesc(left.latestAt, right.latestAt));
  }

  function groupSeedForRun(run: ProductRun, view: Exclude<RunView, 'run'>): Pick<RunGroup, 'key' | 'title' | 'subtitle' | 'agentId' | 'agent' | 'automationId' | 'automation'> | null {
    const agentKey = runAgentGroupKey(run);
    const agentLabel = run.agent || (run.agentId ? run.agentId : '未关联智能体');
    const taskKey = run.automationId || '';
    const taskLabel = run.automation && run.automation !== '-' ? run.automation : (taskKey ? taskKey : '');
    if (view === 'agent') {
      return {
        key: agentKey,
        title: agentLabel,
        subtitle: run.agentId ? `智能体 ${run.agentId}` : '未关联智能体',
        agentId: run.agentId,
        agent: agentLabel,
        automationId: '',
        automation: '',
      };
    }
    if (view === 'task') {
      if (!taskKey) return null;
      return {
        key: taskKey,
        title: taskLabel || taskKey,
        subtitle: `自动化任务 ${taskKey}`,
        agentId: run.agentId,
        agent: agentLabel,
        automationId: taskKey,
        automation: taskLabel || taskKey,
      };
    }
    const normalizedTaskKey = taskKey || 'manual';
    const normalizedTaskLabel = taskLabel || '手动对话';
    return {
      key: `${agentKey}:${normalizedTaskKey}`,
      title: `${agentLabel} / ${normalizedTaskLabel}`,
      subtitle: agentTaskGroupSubtitle(run, agentLabel, taskKey, normalizedTaskLabel),
      agentId: run.agentId,
      agent: agentLabel,
      automationId: taskKey,
      automation: normalizedTaskLabel,
    };
  }

  function agentTaskGroupSubtitle(run: ProductRun, agentLabel: string, taskKey: string, taskLabel: string): string {
    const parts = [
      run.agentId && run.agentId !== agentLabel ? `智能体 ID ${run.agentId}` : '',
      taskKey && taskKey !== taskLabel ? `任务 ID ${taskKey}` : '',
    ].filter(Boolean);
    return parts.length > 0 ? parts.join(' · ') : `对象 ${taskKey ? '自动化任务' : '手动对话'}`;
  }

  function runAgentGroupKey(run: ProductRun): string {
    if (run.agentId) return run.agentId;
    if (run.agent && run.agent !== '-') return `name:${run.agent}`;
    return 'unassigned-agent';
  }

  function compareRunTimeDesc(left: ProductRun, right: ProductRun): number {
    return compareDateDesc(left.completedAt || left.startedAt, right.completedAt || right.startedAt);
  }

  function compareDateDesc(left: string, right: string): number {
    return new Date(right || 0).getTime() - new Date(left || 0).getTime();
  }

  function latestRunTime(items: ProductRun[]): string {
    return items
      .map((item) => item.completedAt || item.startedAt)
      .filter(Boolean)
      .sort((left, right) => compareDateDesc(left, right))[0] || '';
  }

  function earliestRunTime(items: ProductRun[]): string {
    return items
      .map((item) => item.startedAt || item.completedAt)
      .filter(Boolean)
      .sort((left, right) => compareDateDesc(right, left))[0] || '';
  }

  function selectRun(run: ProductRun): void {
    openRunFromTimeline(run);
  }

  function syncSelectedRunWithFilters(): void {
    const current = currentAgentRuns.find((run) => run.id === selectedRunId);
    const nextRun = current || currentAgentRuns[0] || null;
    if (selectedRunId === (nextRun?.id || '')) {
      return;
    }
    selectedRunId = nextRun?.id || '';
    if (nextRun) {
      void loadRunDetail(nextRun);
    }
  }

  function selectGroup(group: RunGroup): void {
    if (selectedAgentId === group.key) {
      return;
    }
    selectedAgentId = group.key;
    selectedGroupKey = selectedAgentId;
    selectedRunId = '';
    activeGroupTab = 'runs';
    syncAgentSelectionWithFilters();
    updateURL();
    void loadGroupDetail(group);
  }

  function openRunFromGroup(run: ProductRun): void {
    openRunFromTimeline(run);
  }

  function toggleTaskExecutionDetail(run: ProductRun): void {
    if (selectedRunId === run.id) {
      selectedRunId = '';
      selectedContextKey = selectedTaskId ? `task:${selectedTaskId}` : 'overview';
      activeDetailTab = 'result';
      auxiliaryPanelKind = 'none';
      sidePanelOpen = false;
      updateURL();
      return;
    }
    openRunFromTimeline(run);
  }

  function toggleActivityDetail(run: ProductRun): void {
    if (selectedRunId === run.id) {
      selectedRunId = '';
      selectedContextKey = 'overview';
      activeDetailTab = 'result';
      auxiliaryPanelKind = 'none';
      sidePanelOpen = false;
      updateURL();
      return;
    }
    selectedContextKey = run.automationId ? `task:${run.automationId}` : `manual:${run.id}`;
    if (!run.automationId || (selectedTaskId && selectedTaskId !== run.automationId)) {
      selectedTaskId = '';
    }
    taskFilter = selectedTaskId;
    selectedRunId = run.id;
    activeDetailTab = 'result';
    auxiliaryPanelKind = 'none';
    sidePanelOpen = false;
    updateURL();
    void loadRunDetail(run);
  }

  function selectConversationTaskItem(item: ConversationTaskItem): void {
    selectedContextKey = item.key;
    timelineFilter = 'all';
    if (item.kind === 'overview') {
      selectedRunId = '';
      selectedTaskId = '';
      taskFilter = '';
      auxiliaryPanelKind = 'none';
      sidePanelOpen = false;
      updateURL();
      if (selectedGroup) void loadGroupDetail(selectedGroup);
      return;
    }
    if (item.kind === 'manual_conversation') {
      const run = item.runs[0] || null;
      selectedRunId = run?.id || '';
      selectedTaskId = '';
      taskFilter = '';
      activeDetailTab = 'result';
      auxiliaryPanelKind = 'run';
      sidePanelOpen = true;
      updateURL();
      if (run) void loadRunDetail(run);
      return;
    }
    selectedTaskId = item.taskId;
    taskFilter = selectedTaskId;
    if (selectedRunId && !item.runs.some((run) => run.id === selectedRunId)) {
      selectedRunId = '';
    }
    auxiliaryPanelKind = 'none';
    sidePanelOpen = false;
    updateURL();
    if (selectedGroup) void loadGroupDetail(selectedGroup);
  }

  function openRunFromTimeline(run: ProductRun): void {
    selectedContextKey = run.automationId ? `task:${run.automationId}` : `manual:${run.id}`;
    if (!run.automationId || (selectedTaskId && selectedTaskId !== run.automationId)) {
      selectedTaskId = '';
    }
    taskFilter = selectedTaskId;
    selectedRunId = run.id;
    activeDetailTab = 'result';
    if (workbenchMode === 'tasks') {
      auxiliaryPanelKind = 'none';
      sidePanelOpen = false;
    } else {
      auxiliaryPanelKind = 'run';
      sidePanelOpen = true;
    }
    updateURL();
    void loadRunDetail(run);
  }

  function continueManualConversation(run: ProductRun): void {
    if (!canContinueManualConversation(run)) return;
    workbenchMode = 'chat';
    activeMode = workbenchMode;
    selectedRunId = run.id;
    selectedConversationId = manualConversationId(run);
    conversationId = selectedConversationId;
    selectedContextKey = `manual:${run.id}`;
    selectedTaskId = '';
    taskFilter = '';
    activeDetailTab = 'result';
    auxiliaryPanelKind = 'none';
    sidePanelOpen = false;
    updateURL();
    void loadRunDetail(run).then(() => {
      void scrollMessagesToBottom();
      scheduleMessageBottomCorrection();
    });
  }

  function setDetailTab(tab: typeof activeDetailTab): void {
    if (activeDetailTab === tab) {
      return;
    }
    activeDetailTab = tab;
    updateURL();
    if (tab === 'result' && selectedRun?.type === 'work_session') {
      void scrollMessagesToBottom();
      scheduleMessageBottomCorrection();
    }
  }

  function setGroupTab(tab: GroupTab): void {
    if (activeGroupTab === tab) {
      return;
    }
    activeGroupTab = tab;
    updateURL();
    if (selectedGroup) {
      void loadGroupDetail(selectedGroup);
    }
  }

  function setTimelineFilter(filter: TimelineFilter): void {
    timelineFilter = filter;
    showAllTaskTimeline = false;
    timelineVisibleLimit = 100;
    clearTimelineRunSelection();
    applyFilters();
  }

  function clearTimelineRunSelection(): void {
    selectedRunId = '';
    selectedConversationId = '';
    conversationId = '';
    activeDetailTab = 'result';
    if (auxiliaryPanelKind === 'run') {
      auxiliaryPanelKind = 'none';
      sidePanelOpen = false;
    }
  }

  async function copyText(value: string, event?: MouseEvent): Promise<void> {
    event?.stopPropagation();
    if (!value) return;
    if (copiedTimer) {
      clearTimeout(copiedTimer);
    }
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
      } else {
        fallbackCopyText(value);
      }
      copiedId = value;
      copyNotice = { text: '已复制', ok: true };
      copiedTimer = setTimeout(() => {
        copiedId = '';
        copyNotice = null;
      }, 1400);
    } catch (err) {
      copiedId = value;
      copyNotice = { text: err instanceof Error ? err.message : '复制失败', ok: false };
      copiedTimer = setTimeout(() => {
        copiedId = '';
        copyNotice = null;
      }, 1800);
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

  function statusClass(status: string): string {
    if (['启动失败', '失败', '跳过', '已取消'].includes(status)) return 'red';
    if (['成功', '已停止'].includes(status)) return 'green';
    if (['运行中', '停止中...', '恢复中...'].includes(status)) return 'blue';
    return 'gray';
  }

  function hasRunningAgentMessage(run: ProductRun): boolean {
    return run.status === '运行中' && run.messages.some((message) => message.role === 'agent' && message.running);
  }

  function isReplyPending(run: ProductRun): boolean {
    return hasRunningAgentMessage(run);
  }

  function canSendMessage(run: ProductRun): boolean {
    return run.type === 'work_session' && run.status === '运行中' && !isReplyPending(run);
  }

  function displayStatus(run: ProductRun): string {
    if (sessionAction?.runId === run.id) {
      return sessionAction.type === 'stop' ? '停止中...' : '恢复中...';
    }
    return run.status;
  }

  function displayStatusClass(run: ProductRun): string {
    if (sessionAction?.runId === run.id) return 'blue';
    return statusClass(run.status);
  }

  function chatRunActionLabel(run: ProductRun): string {
    if (sessionAction?.runId === run.id) {
      return sessionAction.type === 'stop' ? '停止中...' : '恢复中...';
    }
    if (run.status === '运行中') return '停止运行';
    if (['启动失败', '失败'].includes(run.status)) return '重试';
    return '继续对话';
  }

  function chatRunActionDisabled(run: ProductRun): boolean {
    if (Boolean(sessionAction)) return true;
    return !['运行中', '已停止', '启动失败', '失败'].includes(run.status);
  }

  async function handleChatRunAction(run: ProductRun): Promise<void> {
    if (run.status === '运行中') {
      await stopSelectedRun(run);
      return;
    }
    await resumeSelectedRun(run);
  }

  function canOpenDebug(run: ProductRun): boolean {
    return run.type === 'work_session' && run.status === '运行中';
  }

  function messageInputHint(run: ProductRun): string {
    if (isReplyPending(run)) return '等待当前回复完成';
    if (run.status === '等待中') return '运行启动中';
    if (run.status === '已停止') return '运行已停止';
    if (run.status === '启动失败') return '运行启动失败';
    if (run.status !== '运行中') return '运行未运行';
    return 'Shift + Enter 换行';
  }

  function runMessages(run: ProductRun): ProductRun['messages'] {
    return run.messages;
  }

  function runMessageSummaryItems(run: ProductRun): Array<{ key: string; title: string; content: string; at: string; tone: string }> {
    const items = run.messages
      .filter((message) => message.role === 'user' || message.role === 'agent')
      .map((message) => ({
        key: message.renderKey || message.id || `${message.role}-${message.at}`,
        title: `${message.role === 'user' ? '输入' : '输出'} · ${messageStatus(message)}`,
        content: sanitizeRunText(messageSummaryText(message), 320),
        at: message.at,
        tone: messageStatusTone(message),
      }))
      .filter((item) => item.content);
    if (!items.some((item) => item.title.startsWith('输入'))) {
      const input = runEventInputSummary(run);
      if (input) {
        items.unshift({ key: `${run.id}-event-input`, title: '输入', content: input.message, at: input.at || run.startedAt, tone: 'succeeded' });
      }
    }
    if (items.length > 0) return items;
    const fallback: Array<{ key: string; title: string; content: string; at: string; tone: string }> = [];
    const input = runInputSummary(run);
    const output = runOutputSummary(run);
    if (input) {
      fallback.push({ key: `${run.id}-input`, title: '输入', content: input, at: run.startedAt, tone: 'succeeded' });
    }
    if (output) {
      fallback.push({ key: `${run.id}-output`, title: '输出', content: output, at: runEndedAt(run) || run.completedAt || run.startedAt, tone: statusClass(run.status) === 'red' ? 'failed' : 'succeeded' });
    }
    return fallback;
  }

  const AGENT_RESULT_PREFIXES = ['__ADP_LITE_AGENT_RESULT__', '__AGENT_RESULT__'];

  function stripAgentResultPayload(text: string): string {
    const result = text || '';
    const indexes = AGENT_RESULT_PREFIXES
      .map((prefix) => result.lastIndexOf(prefix))
      .filter((index) => index >= 0);
    const index = indexes.length > 0 ? Math.min(...indexes) : -1;
    return index >= 0 ? result.slice(0, index) : result;
  }

  function sanitizeRunText(value: string, maxLength = 260): string {
    const raw = value || '';
    const payloadText = readableAgentResultPayload(raw);
    let text = payloadText || raw;
    for (const prefix of AGENT_RESULT_PREFIXES) {
      const index = text.lastIndexOf(prefix);
      if (index >= 0) {
        text = text.slice(0, index);
      }
    }
    const jsonText = readableJsonPayload(text);
    if (jsonText) {
      text = jsonText;
    }
    text = text
      .replace(/\u001b\[[0-9;?]*[A-Za-z]/g, '')
      .split(/\r?\n/)
      .filter((line) => !/^\s*"?(?:sessionId|session_id|provider|stderr|transcript|agentSessionId|agent_session_id|raw|payload)"?\s*[:=]/i.test(line))
      .filter((line) => !/^\s*(?:stderr|transcript)\b/i.test(line))
      .join(' ')
      .replace(/"?(?:sessionId|session_id|provider|stderr|transcript|agentSessionId|agent_session_id)"?\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,}]+)/gi, '')
      .replace(/\s+/g, ' ')
      .trim();
    if (/^\{.*\}$/.test(text) && /"(?:sessionId|provider|stderr|transcript)"/i.test(text)) {
      return '';
    }
    return compactText(text, maxLength);
  }

  function readableAgentResultPayload(value: string): string {
    for (const prefix of AGENT_RESULT_PREFIXES) {
      const index = value.lastIndexOf(prefix);
      if (index < 0) continue;
      const payload = value.slice(index + prefix.length).trim();
      const parsed = readableJsonPayload(payload);
      if (parsed) return parsed;
    }
    return '';
  }

  function readableJsonPayload(value: string): string {
    const text = value.trim();
    if (!text || (!text.startsWith('{') && !text.startsWith('['))) return '';
    try {
      const parsed = JSON.parse(text);
      return readablePayloadValue(parsed);
    } catch {
      return '';
    }
  }

  function readablePayloadValue(value: unknown): string {
    if (typeof value === 'string') return sanitizeRunText(value, 260);
    if (!value || typeof value !== 'object') return '';
    if (Array.isArray(value)) {
      return value.map((item) => readablePayloadValue(item)).filter(Boolean).join(' ');
    }
    const record = value as Record<string, unknown>;
    for (const key of ['finalText', 'final_text', 'text', 'output', 'result', 'summary', 'message']) {
      const current = readablePayloadValue(record[key]);
      if (current) return current;
    }
    return '';
  }

  function agentMessageContent(message: ProductRun['messages'][number], content: string): string {
    return message.role === 'agent' ? stripAgentResultPayload(content) : content;
  }

  function mergeMessageContent(existing: ProductRun['messages'][number], next: ProductRun['messages'][number]): string {
    return agentMessageContent(next, mergeCellContent(agentMessageContent(next, existing.content), agentMessageContent(next, next.content)));
  }

  function cellMessage(cell: Awaited<ReturnType<typeof listWorkSessionCells>>[number]): ProductRun['messages'][number] {
    const role = cell.agent ? 'agent' : 'user';
    return {
      id: cell.id,
      renderKey: cell.id,
      role,
      type: cell.type,
      agent: cell.agent,
      source: cell.source,
      content: role === 'agent' ? stripAgentResultPayload(cell.output || cell.stopReason || '') : cell.output || cell.stopReason || '',
      at: cell.createdAt || cell.id,
      running: cell.running,
      success: cell.success,
      exitCode: cell.exitCode,
      stopReason: cell.stopReason,
      agentSessionId: cell.agentSessionId,
      failed: Boolean(!cell.running && !cell.success),
    };
  }

  function cellToMessages(cell: Awaited<ReturnType<typeof listWorkSessionCells>>[number]): ProductRun['messages'][] {
    const agent = cellMessage(cell);
    if (agent.role !== 'agent' || !cell.source?.trim()) return [agent];
    // Agent cells carry the user's input in cell.source. Surface it as a
    // distinct user message so both input and output appear in the chat.
    const user: ProductRun['messages'][number] = {
      id: `${cell.id}-input`,
      renderKey: `${cell.id}-input`,
      role: 'user',
      type: cell.type,
      source: cell.source,
      content: '',
      at: cell.createdAt || cell.id,
    };
    return [user, agent];
  }

  function latestCellAgent(cells: Awaited<ReturnType<typeof listWorkSessionCells>>): string {
    for (let index = cells.length - 1; index >= 0; index -= 1) {
      const agent = cells[index].agent?.trim();
      if (agent) return agent;
    }
    return '';
  }

  function tagValue(tags: Array<{ name: string; value: string }>, name: string): string {
    return tags.find((tag) => tag.name === name)?.value || '';
  }

  function upsertMessage(messages: ProductRun['messages'], next: ProductRun['messages'][number]): ProductRun['messages'] {
    if (!next.id) {
      return [...messages, next];
    }
    const index = messages.findIndex((message) => message.id === next.id);
    if (index < 0) {
      const pendingIndex = messages.findIndex((message) => message.role === next.role && message.running && !message.id);
      if (pendingIndex >= 0) {
        const updated = [...messages];
        const existing = updated[pendingIndex];
        updated[pendingIndex] = {
          ...next,
          renderKey: existing.renderKey || next.renderKey || next.id,
          content: mergeMessageContent(existing, next),
        };
        return updated;
      }
      return [...messages, next];
    }
    const updated = [...messages];
    const existing = updated[index];
    updated[index] = {
      ...next,
      renderKey: existing.renderKey || next.renderKey || next.id,
      content: mergeMessageContent(existing, next),
    };
    return updated;
  }

  function mergeCellContent(existing: string, incoming: string): string {
    if (!existing || existing === '-') return incoming;
    if (!incoming || incoming === '-') return existing;
    if (existing === incoming) return incoming;
    if (existing.startsWith(incoming)) return existing;
    return incoming;
  }

  function appendPendingChunk(cellId: string, chunk: string): void {
    if (!cellId) return;
    pendingCellChunks.set(cellId, `${pendingCellChunks.get(cellId) || ''}${chunk}`);
  }

  function appendAgentChunk(runId: string, cellId: string, chunk: string): boolean {
    let applied = false;
    runs = runs.map((item) => {
      if (item.id !== runId) return item;
      const messages = [...item.messages];
      let index = cellId ? messages.findIndex((message) => message.id === cellId) : -1;
      if (index < 0) {
        index = messages.findIndex((message) => message.role === 'agent' && message.running);
      }
      if (index >= 0 && messages[index].role === 'agent') {
        applied = true;
        messages[index] = { ...messages[index], content: stripAgentResultPayload(`${messages[index].content || ''}${chunk}`) };
      }
      return { ...item, messages };
    });
    return applied;
  }

  async function scrollMessagesToBottom(): Promise<void> {
    await tick();
    if (messageScrollFrame) return;
    messageScrollFrame = requestAnimationFrame(() => {
      messageScrollFrame = 0;
      if (messageScroll) {
        messageScroll.scrollTop = messageScroll.scrollHeight;
      }
    });
  }

  function isMessageScrollNearBottom(): boolean {
    if (!messageScroll) return true;
    return messageScroll.scrollHeight - messageScroll.scrollTop - messageScroll.clientHeight < 96;
  }

  function scheduleMessageBottomCorrection(delayMs = 80): void {
    const shouldCorrect = isMessageScrollNearBottom();
    window.setTimeout(() => {
      if (messageScroll && shouldCorrect) {
        messageScroll.scrollTop = messageScroll.scrollHeight;
      }
    }, delayMs);
  }

  function ensureTerminalObserver(): void {
    if (terminalObserver || !messageScroll) return;
    terminalObserverRoot = messageScroll;
    terminalObserver = new IntersectionObserver(
      (entries) => {
        let changed = false;
        for (const entry of entries) {
          const id = terminalNodeIds.get(entry.target);
          if (!id) continue;
          if (entry.isIntersecting) {
            if (!terminalVisibleIds.has(id)) {
              terminalVisibleIds.add(id);
              changed = true;
            }
          }
        }
        if (changed) {
          terminalVisibleIds = new Set(terminalVisibleIds);
        }
      },
      { root: messageScroll, rootMargin: '400px 0px' },
    );
    for (const node of terminalNodeIds.keys()) {
      terminalObserver.observe(node);
    }
  }

  function syncTerminalObserverRoot(root: HTMLDivElement | null): void {
    if (root === terminalObserverRoot) return;
    terminalObserver?.disconnect();
    terminalObserver = null;
    terminalObserverRoot = root;
    terminalVisibleIds = new Set();
    if (root) {
      ensureTerminalObserver();
    }
  }

  function trackTerminalVisibility(node: HTMLElement, id: string) {
    terminalNodeIds.set(node, id);
    terminalObserver?.observe(node);
    return {
      update(nextId: string) {
        if (id !== nextId && terminalVisibleIds.has(id)) {
          const nextVisibleIds = new Set(terminalVisibleIds);
          nextVisibleIds.delete(id);
          if (nextId) {
            nextVisibleIds.add(nextId);
          }
          terminalVisibleIds = nextVisibleIds;
        }
        id = nextId;
        terminalNodeIds.set(node, nextId);
      },
      destroy() {
        terminalObserver?.unobserve(node);
        terminalNodeIds.delete(node);
        if (id) {
          const nextVisibleIds = new Set(terminalVisibleIds);
          nextVisibleIds.delete(id);
          terminalVisibleIds = nextVisibleIds;
        }
      },
    };
  }

  function estimateTerminalHeight(message: ProductRun['messages'][number]): number {
    const cached = message.id ? terminalHeights.get(message.id) : undefined;
    if (cached) return cached;
    const text = messageTerminalText(message);
    const lines = text ? text.split('\n').length : 1;
    return Math.min(Math.max(lines, 1) * 18 + 16, 4000);
  }

  function applyPendingChunks(message: ProductRun['messages'][number]): ProductRun['messages'][number] {
    if (!message.id) return message;
    const pending = pendingCellChunks.get(message.id);
    if (!pending) return message;
    pendingCellChunks.delete(message.id);
    return {
      ...message,
      content: agentMessageContent(message, `${message.content || ''}${pending}`),
    };
  }

  function terminalRenderer(node: HTMLElement, params: TerminalActionParams) {
    const term = new Terminal({
      convertEol: true,
      disableStdin: true,
      cursorBlink: false,
      cols: 100,
      rows: 1,
      fontFamily: 'IBM Plex Mono, Fira Code, monospace',
      fontSize: 13,
      lineHeight: 1.25,
      scrollback: 0,
      theme: { ...terminalTheme, cursor: params.running ? terminalTheme.cursor : 'transparent' },
    });
    const fitAddon = new FitAddon();
    let currentText = '';
    let currentHeight = 0;
    const fallbackLineHeight = Math.ceil(13 * 1.25);
    const observer = new ResizeObserver(() => scheduleTerminalHeightSync());
    let heightFrame = 0;
    let disposed = false;

    term.loadAddon(fitAddon);
    term.open(node);
    observer.observe(node);

    function setCursorActive(active: boolean): void {
      node.classList.toggle('terminal-cursor-active', active);
    }

    const visibleRows = (text: string) => {
      const cols = Math.max(term.cols || 1, 1);
      const lines = text.length > 0 ? text.split(/\r?\n/) : [''];
      let rows = 0;
      for (const rawLine of lines) {
        const plainLine = rawLine.replace(/\u001b\[[0-9;?]*[A-Za-z]/g, '');
        rows += Math.max(1, Math.ceil(Math.max(plainLine.length, 1) / cols));
      }
      return Math.max(rows, 1);
    };

    function fitTerminalRows(text: string): number {
      fitAddon.fit();
      const cols = Math.max(term.cols || 1, 1);
      const rows = visibleRows(text);
      if (term.rows !== rows) {
        term.resize(cols, rows);
      }
      return rows;
    }

    function scheduleTerminalHeightSync(): void {
      if (disposed || heightFrame) return;
      heightFrame = requestAnimationFrame(() => {
        heightFrame = 0;
        if (!disposed) {
          syncTerminalHeight();
        }
      });
    }

    function syncTerminalHeight() {
      if (disposed || !node.isConnected) return;
      const rows = fitTerminalRows(currentText);
      if (disposed || !node.isConnected) return;
      const measuredLineHeight = Math.ceil(
        Number((term as any)?._core?._renderService?.dimensions?.css?.cell?.height) || fallbackLineHeight,
      );
      const nextHeight = rows * measuredLineHeight + 16;
      if (nextHeight > currentHeight || !params.running) {
        currentHeight = nextHeight;
        node.style.height = `${nextHeight}px`;
        if (!params.running && params.id) {
          terminalHeights.set(params.id, nextHeight);
        }
        scheduleMessageBottomCorrection(0);
      }
      term.scrollToBottom();
    }

    function cancelXtermViewportRefresh(): void {
      const core = (term as any)?._core;
      const viewport = core?.viewport;
      const frame = viewport?._refreshAnimationFrame;
      const win = core?._coreBrowserService?.window || window;
      if (typeof frame === 'number') {
        win.cancelAnimationFrame(frame);
        viewport._refreshAnimationFrame = null;
      }
    }

    function applyText(text: string) {
      if (text === currentText) {
        return;
      }
      fitTerminalRows(text);
      if (text.startsWith(currentText)) {
        term.write(text.slice(currentText.length));
      } else {
        term.reset();
        term.write(text);
        currentHeight = 0;
      }
      currentText = text;
      scheduleTerminalHeightSync();
    }

    setCursorActive(params.running);
    applyText(params.text);

    return {
      update(next: TerminalActionParams) {
        term.options.theme = { ...terminalTheme, cursor: next.running ? terminalTheme.cursor : 'transparent' };
        params = next;
        setCursorActive(next.running);
        applyText(next.text);
      },
      destroy() {
        disposed = true;
        observer.disconnect();
        if (heightFrame) {
          cancelAnimationFrame(heightFrame);
          heightFrame = 0;
        }
        cancelXtermViewportRefresh();
        term.dispose();
      },
    };
  }

  function formatRole(role: string): string {
    if (role === 'user') return '用户';
    if (role === 'agent') return '智能体';
    return '系统';
  }

  function messageStatus(message: ProductRun['messages'][number]): string {
    if (message.role === 'user') return '发送';
    if (message.running) {
      return '运行中';
    }
    const exitCode = message.exitCode ?? (message.success === false ? 1 : 0);
    return exitCode === 0 ? '完成' : `退出码 ${exitCode}`;
  }

  function messageStatusTone(message: ProductRun['messages'][number]): string {
    if (message.role === 'user') return 'succeeded';
    if (message.running) return 'running';
    if (message.success === false || message.failed) return 'failed';
    return 'succeeded';
  }

  function messageTerminalText(message: ProductRun['messages'][number]): string {
    if (message.role === 'user') return message.source || '-';
    return agentMessageContent(message, message.content || (message.running ? '' : '-'));
  }

  function messageSummaryText(message: ProductRun['messages'][number]): string {
    return agentMessageContent(message, message.content || message.source || '');
  }

  function eventLevelTone(level: string): string {
    const normalized = level.toLowerCase();
    if (normalized === 'error' || normalized === 'failed' || normalized === 'failure') return 'error';
    if (normalized === 'warn' || normalized === 'warning') return 'warning';
    if (normalized === 'debug' || normalized === 'trace') return 'debug';
    if (normalized === 'success' || normalized === 'succeeded') return 'success';
    return 'info';
  }

  function formatTime(value: string): string {
    return formatBeijingTime(value);
  }

  function runSortTimestamp(run: ProductRun): number {
    const updatedAt = Date.parse(run.completedAt || '');
    if (!Number.isNaN(updatedAt)) return updatedAt;
    const createdAt = Date.parse(run.startedAt || '');
    return Number.isNaN(createdAt) ? 0 : createdAt;
  }

  function sortRunsByUpdatedTime(source: ProductRun[]): ProductRun[] {
    return [...source].sort((left, right) => {
      const byUpdatedTime = runSortTimestamp(right) - runSortTimestamp(left);
      if (byUpdatedTime !== 0) return byUpdatedTime;
      return right.id.localeCompare(left.id);
    });
  }

  function runDuration(run: ProductRun): string {
    const startedAt = new Date(run.startedAt).getTime();
    if (!run.startedAt || Number.isNaN(startedAt)) {
      return run.duration || '-';
    }
    const completedAt = new Date(run.completedAt).getTime();
    const endedAt = run.status === '运行中' || !run.completedAt || Number.isNaN(completedAt) ? Date.now() : completedAt;
    const seconds = Math.max(0, Math.round((endedAt - startedAt) / 1000));
    if (seconds < 60) return `${seconds}s`;
    if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
    return `${(seconds / 3600).toFixed(1)}h`;
  }

  async function load(): Promise<void> {
    loading = true;
    error = '';
    loadedGroupKeys = new Set();
    try {
      const sessionResult = await listWorkSessions(50, 0);
      const sessions = sessionResult.sessions;
      sessionOffset = 50;
      sessionHasMore = sessionResult.hasMore;
      const [tasks, agents, presets] = await Promise.all([listAutomationTasks(), listAgentDefinitions(), listWorkspacePresets()]);
      workspaces = presets.filter((item) => item.type === 'git' || item.type === 'file');
      const automationRuns = await listRecentAutomationRuns(tasks.map((item) => item.id), 20);
      const taskById = new Map(tasks.map((task) => [task.id, task]));
      const agentById = new Map(agents.map((agent) => [agent.id, agent]));
      agentDefinitions = agents;
      automationTasks = tasks;
      const sessionRuns = sessions.map((session) => {
        const productRun = sessionToRun(session);
        const agentID = tagValue(session.tags, 'agent_id');
        const agent = agentID ? agentById.get(agentID) : null;
        const loaderID = tagValue(session.tags, 'loader_id') || productRun.automationId;
        const loaderName = tagValue(session.tags, 'loader_name') || productRun.automation;
        const task = loaderID ? taskById.get(loaderID) : null;
        return {
          ...productRun,
          agentId: agentID || productRun.agentId,
          agent: agent?.name || productRun.agent || agentID,
          agentProvider: agent?.provider || productRun.agentProvider,
          automationId: loaderID,
          automation: loaderName && loaderName !== '-' ? loaderName : task?.name || loaderID ? (task?.name || loaderID) : '-',
        };
      });
      let initialRuns = sortRunsByUpdatedTime([
        ...sessionRuns,
        ...automationRuns.map((run) => {
          const productRun = automationRunToRun(run);
          const task = taskById.get(run.loaderId);
          const agent = task?.agentId ? agentById.get(task.agentId) : null;
          const agentName = agent?.name || task?.agentId || productRun.agent;
          return task
            ? {
              ...productRun,
              title: task.name || productRun.title,
              automation: task.name || productRun.automation,
              agent: agentName,
              agentId: task.agentId || productRun.agentId,
              agentProvider: agent?.provider || task.defaultAgent || productRun.agentProvider,
              workspace: task.workspaceId || productRun.workspace,
          }
            : productRun;
        }),
      ]);
      initialRuns = await hydrateFirstUserMessages(initialRuns);
      runs = initialRuns;
      await syncSelectionAfterRunsLoaded();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  async function hydrateFirstUserMessages(targets: ProductRun[]): Promise<ProductRun[]> {
    const toHydrate = targets.filter((r) => r.type === 'work_session' && r.messageCount > 0);
    if (toHydrate.length === 0) return targets;
    const results = await Promise.all(
      toHydrate.map(async (run) => {
        try {
          const cells = await listWorkSessionCells(run.id);
          const firstSource = cells.find((c) => c.source?.trim())?.source?.trim() || '';
          return { id: run.id, input: firstSource };
        } catch {
          return { id: run.id, input: '' };
        }
      }),
    );
    const inputMap = new Map(results.map((r) => [r.id, r.input]));
    return targets.map((run) => {
      const input = inputMap.get(run.id);
      if (input === undefined) return run;
      return { ...run, input: input || run.input };
    });
  }

  async function loadMoreManualConversations(): Promise<void> {
    const manualRuns = currentAgentBaseRuns.filter((run) => !run.automationId);
    if (manualConversationVisible >= manualRuns.length && sessionHasMore) {
      try {
        const result = await listWorkSessions(50, sessionOffset);
        const agentById = new Map(agentDefinitions.map((agent) => [agent.id, agent]));
        const taskById = new Map(automationTasks.map((task) => [task.id, task]));
        const newRuns = result.sessions
          .filter((session) => !runs.some((run) => run.id === session.id))
          .map((session) => {
            const productRun = sessionToRun(session);
            const agentID = tagValue(session.tags, 'agent_id');
            const agent = agentID ? agentById.get(agentID) : null;
            const loaderID = tagValue(session.tags, 'loader_id') || productRun.automationId;
            const loaderName = tagValue(session.tags, 'loader_name') || productRun.automation;
            const task = loaderID ? taskById.get(loaderID) : null;
            return {
              ...productRun,
              agentId: agentID || productRun.agentId,
              agent: agent?.name || productRun.agent || agentID,
              agentProvider: agent?.provider || productRun.agentProvider,
              automationId: loaderID,
              automation: loaderName && loaderName !== '-' ? loaderName : task?.name || loaderID ? (task?.name || loaderID) : '-',
            };
          });
        const hydratedNewRuns = await hydrateFirstUserMessages(newRuns);
        runs = sortRunsByUpdatedTime([...runs, ...hydratedNewRuns]);
        sessionOffset += result.sessions.length;
        sessionHasMore = result.hasMore;
      } catch { /* ignore */ }
    }
    manualConversationVisible += 10;
  }

  async function syncSelectionAfterRunsLoaded(): Promise<void> {
    const byRun = selectedRunId ? runs.find((run) => run.id === selectedRunId) || null : null;
    if (byRun) {
      const byKey = runAgentGroupKey(byRun);
      if (byKey !== 'unassigned-agent' || !selectedAgentId) {
        selectedAgentId = byKey;
      }
      selectedGroupKey = selectedAgentId;
      if (byRun.automationId && !selectedTaskId) {
        selectedTaskId = byRun.automationId;
        taskFilter = selectedTaskId;
      }
      if (pendingLegacyRunIdMode) {
        workbenchMode = byRun.automationId ? 'timeline' : 'chat';
        activeMode = workbenchMode;
        if (!byRun.automationId) {
          selectedConversationId = manualConversationId(byRun);
          conversationId = selectedConversationId;
        }
        pendingLegacyRunIdMode = false;
      }
    }
    const loadedAgents = buildAgentObservations(runs);
    if (selectedTaskId && (!selectedAgentId || !loadedAgents.some((agent) => agent.key === selectedAgentId))) {
      const task = automationTasks.find((item) => item.id === selectedTaskId) || null;
      const runForTask = runs.find((run) => run.automationId === selectedTaskId) || null;
      selectedAgentId = task?.agentId || (runForTask ? runAgentGroupKey(runForTask) : selectedAgentId);
      selectedGroupKey = selectedAgentId;
    }
    const agents = filterAgentObservations(loadedAgents);
    const currentAgent = agents.find((agent) => agent.key === selectedAgentId) || agents[0] || null;
    selectedAgentId = currentAgent?.key || '';
    selectedGroupKey = selectedAgentId;
    const agentRuns = currentAgent ? filterAgentRuns(currentAgent.runs, { ignoreTimeline: true }) : [];
    const items = currentAgent ? buildConversationTaskItems(currentAgent, agentRuns, tasksForAgent(currentAgent)) : [];
    if (workbenchMode === 'chat') {
      const chatRun = (selectedConversationId || selectedRunId)
        ? currentAgent?.runs.find((run) => run.id === (selectedConversationId || selectedRunId) && !run.automationId) || null
        : null;
      const fallbackChatRun = chatRun || agentRuns.find((run) => !run.automationId) || currentAgent?.runs.filter((run) => !run.automationId).sort(compareRunTimeDesc)[0] || null;
      if (fallbackChatRun) {
        selectedRunId = fallbackChatRun.id;
        selectedConversationId = selectedRunId;
        conversationId = selectedConversationId;
        selectedContextKey = `manual:${selectedRunId}`;
        selectedTaskId = '';
        taskFilter = '';
      } else {
        workbenchMode = preferredWorkbenchModeForAgent(currentAgent, agentRuns);
        activeMode = workbenchMode;
        selectedRunId = '';
        selectedConversationId = '';
        conversationId = '';
        selectedContextKey = selectedTaskId ? `task:${selectedTaskId}` : 'overview';
      }
    } else if (byRun) {
      selectedContextKey = byRun.automationId ? `task:${byRun.automationId}` : `manual:${byRun.id}`;
    } else if (selectedTaskId) {
      selectedContextKey = `task:${selectedTaskId}`;
    } else {
      selectedContextKey = 'overview';
    }
    const currentItem = items.find((item) => item.key === selectedContextKey) || items[0] || null;
    selectedContextKey = currentItem?.key || 'overview';
    if (!currentItem || currentItem.kind === 'overview') {
      if (workbenchMode !== 'chat') {
        selectedRunId = '';
      }
      if (!selectedTaskId) {
        taskFilter = '';
      }
      auxiliaryPanelKind = 'none';
      sidePanelOpen = false;
    } else if (currentItem.kind === 'manual_conversation') {
      selectedRunId = currentItem.runs[0]?.id || '';
      if (workbenchMode === 'chat') {
        selectedConversationId = selectedRunId;
        conversationId = selectedConversationId;
        auxiliaryPanelKind = 'none';
        sidePanelOpen = false;
      }
      selectedTaskId = '';
      taskFilter = '';
    } else {
      selectedTaskId = currentItem.taskId;
      taskFilter = selectedTaskId;
      if (selectedRunId && !currentItem.runs.some((run) => run.id === selectedRunId)) {
        selectedRunId = '';
      }
      if (workbenchMode === 'tasks') {
        auxiliaryPanelKind = 'none';
        sidePanelOpen = false;
      }
    }
    updateURL(true);
    const currentRun = selectedRunId ? agentRuns.find((run) => run.id === selectedRunId) || currentAgent?.runs.find((run) => run.id === selectedRunId) || null : null;
    if (currentRun) {
      await loadRunDetail(currentRun);
      return;
    }
    if (currentAgent) {
      await loadGroupDetail(agentObservationToGroup(currentAgent, agentRuns));
    }
  }

  async function loadRunDetail(run: ProductRun): Promise<void> {
    if (isTempId(run.id)) return;
    startWatching(run);
    detailLoading = true;
    error = '';
    try {
      if (run.type === 'work_session') {
        // Avoid overwriting optimistic action status. If there is an active
        // sessionAction for this run (resume/stop), skip getWorkSessionStatus so the
        // action handler's result (or the watch stream) updates the status.
        const hasActiveAction = sessionAction?.runId === run.id;
        const [session, cells, events] = await Promise.all([
          hasActiveAction ? Promise.resolve(null) : getWorkSessionStatus(run.id).catch(() => null),
          listWorkSessionCells(run.id).catch(() => []),
          listWorkSessionEvents(run.id).catch(() => []),
        ]);
        const sessionRun = session ? sessionToRun(session) : null;
        runs = runs.map((item) => item.id === run.id
          ? {
            ...item,
            ...(sessionRun || {}),
            status: hasActiveAction ? item.status : (sessionRun?.status || item.status),
            rawStatus: hasActiveAction ? item.rawStatus : (sessionRun?.rawStatus || item.rawStatus),
            title: item.title,
            automation: item.automation,
            agent: item.agent,
            agentId: item.agentId,
            workspace: item.workspace,
            sourceSessionTags: item.sourceSessionTags,
            trigger: item.trigger,
            messages: cells.flatMap(cellToMessages),
          }
          : item);
      } else if (run.automationId) {
        const [detail, events] = await Promise.all([
          getAutomationRun(run.automationId, run.id).catch(() => null),
          listAutomationEvents(run.automationId, 50).catch(() => []),
        ]);
        runs = runs.map((item) => item.id === run.id
          ? {
            ...item,
            ...(detail ? automationRunToRun(detail) : {}),
            title: item.title,
            automation: item.automation,
            agent: item.agent,
            agentId: item.agentId,
            workspace: item.workspace,
            events: events
              .filter((event) => !event.runId || event.runId === run.id)
              .map((event) => ({
                type: event.type,
                level: event.level,
                message: event.message,
                createdAt: event.createdAt,
              })),
          }
          : item);
      }
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      detailLoading = false;
      const updatedRun = runs.find((item) => item.id === run.id) || run;
      ensureUsefulDetailTab(updatedRun);
      if (run.type === 'work_session') {
        void scrollMessagesToBottom();
        scheduleMessageBottomCorrection();
      }
    }
  }

  async function loadGroupDetail(group: RunGroup): Promise<void> {
    const cacheKey = `${group.view}:${group.key}`;
    if (loadedGroupKeys.has(cacheKey)) {
      return;
    }
    const token = groupLoadToken + 1;
    groupLoadToken = token;
    groupDetailLoading = true;
    error = '';
    try {
      const workSessionRuns = group.runs.filter((run) => run.type === 'work_session');
      const automationRuns = group.runs.filter((run) => run.type === 'automation_run' && run.automationId);
      const sessionDetails = new Map<string, Pick<ProductRun, 'messages' | 'events' | 'agentProvider'>>();
      const automationDetails = new Map<string, ProductRun>();
      const automationEventsByTask = new Map<string, Array<{ type: string; level: string; message: string; createdAt: string; runId: string }>>();

      await mapWithConcurrency(workSessionRuns, 4, async (run) => {
        const [cells, events] = await Promise.all([
          listWorkSessionCells(run.id).catch(() => []),
          listWorkSessionEvents(run.id).catch(() => []),
        ]);
        sessionDetails.set(run.id, {
          messages: cells.flatMap(cellToMessages),
          agentProvider: latestCellAgent(cells) || run.agentProvider,
          events: events.map((event) => ({
            type: event.type,
            level: event.level,
            message: event.message,
            createdAt: event.createdAt,
          })),
        });
      });

      await mapWithConcurrency(automationRuns, 4, async (run) => {
        const detail = await getAutomationRun(run.automationId, run.id).catch(() => null);
        if (detail) {
          automationDetails.set(run.id, automationRunToRun(detail));
        }
      });

      const automationIds = uniqueOptions(automationRuns.map((run) => run.automationId).filter(Boolean));
      await mapWithConcurrency(automationIds, 3, async (automationId) => {
        const events = await listAutomationEvents(automationId, 100).catch(() => []);
        automationEventsByTask.set(automationId, events);
      });

      if (token !== groupLoadToken) {
        return;
      }

      runs = runs.map((item) => {
        const sessionDetail = sessionDetails.get(item.id);
        if (sessionDetail) {
          return {
            ...item,
            messages: sessionDetail.messages,
            events: sessionDetail.events,
            agentProvider: sessionDetail.agentProvider,
          };
        }
        const automationDetail = automationDetails.get(item.id);
        if (automationDetail) {
          const events = automationEventsByTask.get(item.automationId) || [];
          return {
            ...item,
            ...automationDetail,
            title: item.title,
            automation: item.automation,
            agent: item.agent,
            agentId: item.agentId,
            workspace: item.workspace,
            events: events
              .filter((event) => !event.runId || event.runId === item.id)
              .map((event) => ({
                type: event.type,
                level: event.level,
                message: event.message,
                createdAt: event.createdAt,
              })),
          };
        }
        return item;
      });

      loadedGroupKeys = new Set([...loadedGroupKeys, cacheKey]);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      if (token === groupLoadToken) {
        groupDetailLoading = false;
      }
    }
  }

  async function mapWithConcurrency<T>(items: T[], limit: number, worker: (item: T) => Promise<void>): Promise<void> {
    const queue = [...items];
    const workers = Array.from({ length: Math.min(limit, queue.length) }, async () => {
      while (queue.length > 0) {
        const item = queue.shift();
        if (item !== undefined) {
          await worker(item);
        }
      }
    });
    await Promise.all(workers);
  }

  function buildGroupTimeline(group: RunGroup): GroupTimelineItem[] {
    return buildRunsTimeline(group.runs);
  }

  function buildRunsTimeline(sourceRuns: ProductRun[]): GroupTimelineItem[] {
    const items: GroupTimelineItem[] = [];
    for (const run of sourceRuns) {
      items.push({
        id: `run-${run.id}`,
        kind: 'run',
        level: statusClass(run.status),
        title: `${runSourceText(run)}开始`,
        message: `${taskNameForRun(run) || run.agent || '手动对话'} · ${runTriggerText(run)} · ${run.status}`,
        at: run.startedAt,
        runId: run.id,
        runTitle: run.title,
        runType: run.type,
        source: runContextLabel(run),
        detailTitle: '运行记录',
        detail: `状态：${run.status}\n对象：${runContextLabel(run)}\n触发规则：${runTriggerText(run)}`,
        order: 10,
      });
      const input = runInputSummary(run);
      if (input) {
        items.push({
          id: `input-${run.id}`,
          kind: 'input',
          level: 'info',
          title: '运行输入',
          message: input,
          at: run.startedAt,
          runId: run.id,
          runTitle: run.title,
          runType: run.type,
          source: runContextLabel(run),
          detailTitle: '输入',
          detail: input,
          order: 20,
        });
      }
      const output = runOutputSummary(run);
      if (output) {
        items.push({
          id: `output-${run.id}`,
          kind: 'output',
          level: ['失败', '启动失败'].includes(run.status) ? 'error' : 'success',
          title: '运行输出',
          message: output,
          at: run.completedAt || latestMessageAt(run) || run.startedAt,
          runId: run.id,
          runTitle: run.title,
          runType: run.type,
          source: runContextLabel(run),
          detailTitle: '输出',
          detail: output,
          order: 30,
        });
      }
      if (run.errorSummary) {
        items.push({
          id: `error-${run.id}`,
          kind: 'error',
          level: 'error',
          title: '错误',
          message: run.errorSummary,
          at: run.completedAt || latestMessageAt(run) || run.startedAt,
          runId: run.id,
          runTitle: run.title,
          runType: run.type,
          source: runContextLabel(run),
          detailTitle: '错误摘要',
          detail: run.errorSummary,
          order: 40,
        });
      }
      for (const artifact of run.artifacts) {
        items.push({
          id: `artifact-${run.id}-${artifact.name}`,
          kind: 'artifact',
          level: 'info',
          title: '产出物',
          message: `文件：${artifact.name}`,
          at: run.completedAt || latestMessageAt(run) || run.startedAt,
          runId: run.id,
          runTitle: run.title,
          runType: run.type,
          source: runContextLabel(run),
          detailTitle: '产出物',
          detail: `${artifact.name}\n${artifact.mimeType} · ${artifact.size} · ${artifact.source}`,
          order: 50,
        });
      }
      for (const event of run.events) {
        const tone = eventLevelTone(event.level);
        items.push({
          id: `event-${run.id}-${event.type}-${event.createdAt}`,
          kind: 'system',
          level: tone,
          title: '系统事件',
          message: `${event.level || 'info'} · ${event.type} · ${event.message}`,
          at: event.createdAt,
          runId: run.id,
          runTitle: run.title,
          runType: run.type,
          source: runContextLabel(run),
          detailTitle: '原始事件',
          detail: `level: ${event.level || 'info'}\ntype: ${event.type}\nmessage: ${event.message}\ntime: ${formatTime(event.createdAt)}\nrun: ${run.id}`,
          order: 60,
        });
      }
      if (run.completedAt && run.completedAt !== run.startedAt) {
        items.push({
          id: `end-${run.id}`,
          kind: 'end',
          level: statusClass(run.status),
          title: `${runSourceText(run)}结束`,
          message: `${run.status} · 耗时 ${runDuration(run)}`,
          at: run.completedAt,
          runId: run.id,
          runTitle: run.title,
          runType: run.type,
          source: runContextLabel(run),
          detailTitle: '运行结束',
          detail: `状态：${run.status}\n开始：${formatTime(run.startedAt)}\n结束：${formatTime(run.completedAt)}\n短 ID：#${shortRunId(run.id)}`,
          order: 70,
        });
      }
    }
    return sortTimelineItems(items.filter((item) => item.at));
  }

  function sortTimelineItems(items: GroupTimelineItem[]): GroupTimelineItem[] {
    return [...items].sort((left, right) => {
      const time = new Date(left.at || 0).getTime() - new Date(right.at || 0).getTime();
      if (time !== 0) return time;
      const run = left.runId.localeCompare(right.runId);
      if (run !== 0) return run;
      return (left.order || 0) - (right.order || 0);
    });
  }

  function filterTimelineItems(items: GroupTimelineItem[], filter: TimelineFilter): GroupTimelineItem[] {
    return items.filter((item) => {
      if (filter === 'all') {
        return item.kind === 'input' ||
          item.kind === 'output' ||
          item.kind === 'error' ||
          item.kind === 'artifact' ||
          (item.kind === 'system' && (item.level === 'error' || item.level === 'warning'));
      }
      if (filter === 'error') {
        return item.kind === 'error' || (item.kind === 'system' && (item.level === 'error' || item.level === 'warning'));
      }
      return item.kind === filter;
    });
  }

  function groupTimelineByDate(items: GroupTimelineItem[]): Array<{ key: string; label: string; items: GroupTimelineItem[] }> {
    const groups = new Map<string, GroupTimelineItem[]>();
    for (const item of items) {
      const key = timelineDateKey(item.at);
      groups.set(key, [...(groups.get(key) || []), item]);
    }
    return Array.from(groups.entries())
      .sort(([left], [right]) => right.localeCompare(left))
      .map(([key, groupItems]) => ({
        key,
        label: timelineDateLabel(key),
        items: sortTimelineItems(groupItems),
      }));
  }

  function runInputSummary(run: ProductRun): string {
    const userMessage = run.messages.find((message) => message.role === 'user');
    const eventInput = runEventInputSummary(run);
    const firstSource = run.messages.length > 0 ? run.messages[0].source : '';
    const text = run.input || userMessage?.source || userMessage?.content || eventInput?.message || firstSource || '';
    const sanitized = sanitizeTimelineSummary(text, 240);
    if (sanitized) return sanitized;
    if (run.automationId) {
      return `任务：${taskNameForRun(run) || run.automationId}；触发规则：${runTriggerText(run)}`;
    }
    return '';
  }

  function runEventInputSummary(run: ProductRun): { message: string; at: string } | null {
    const event = run.events.find((item) => {
      const type = item.type.toLowerCase();
      return type === 'agent.user' || type.endsWith('.user') || type === 'user';
    });
    if (!event?.message) return null;
    const message = sanitizeTimelineSummary(event.message, 240);
    return message ? { message, at: event.createdAt } : null;
  }

  function runOutputSummary(run: ProductRun): string {
    const agentMessages = run.messages.filter((message) => message.role === 'agent');
    const lastAgent = agentMessages[agentMessages.length - 1];
    const text = run.output || (lastAgent ? messageSummaryText(lastAgent) : '');
    const artifactSummary = artifactOutputSummary(run);
    if (artifactSummary && (!text.trim() || run.automationId || isPathLikeOutput(text))) {
      return artifactSummary;
    }
    const sanitized = sanitizeTimelineSummary(text, 260);
    if (sanitized) return sanitized;
    return artifactSummary;
  }

  function latestMessageAt(run: ProductRun): string {
    return run.messages
      .map((message) => message.at)
      .filter(Boolean)
      .sort((left, right) => compareDateDesc(left, right))[0] || '';
  }

  function compactText(value: string, maxLength: number): string {
    const text = value.replace(/\s+/g, ' ').trim();
    return text.length > maxLength ? `${text.slice(0, maxLength)}...` : text;
  }

  function sanitizeTimelineSummary(value: string, maxLength = 260): string {
    const sanitized = sanitizeRunText(value, Math.max(maxLength * 2, 320));
    const displayText = sanitized
      .replace(/\s+\$ (?:\/bin\/(?:ba)?sh|bash|sh)\b.*$/i, '')
      .replace(/\s+\/bin\/(?:ba)?sh:\s+.*$/i, '')
      .replace(/\s+Chunk ID:\s+.*$/i, '')
      .replace(/\s+Process exited with code\s+.*$/i, '')
      .replace(/\s+Wall time:\s+.*$/i, '')
      .replace(/\s+Original token count:\s+.*$/i, '')
      .replace(/\s+Output:\s+.*$/i, '')
      .trim();
    return compactText(displayText || sanitized, maxLength);
  }

  function timelineDateKey(value: string): string {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return 'unknown';
    const pad = (part: number) => String(part).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
  }

  function timelineDateLabel(key: string): string {
    if (key === 'unknown') return '未知日期';
    const today = timelineDateKey(new Date().toISOString());
    const yesterdayDate = new Date();
    yesterdayDate.setDate(yesterdayDate.getDate() - 1);
    const yesterday = timelineDateKey(yesterdayDate.toISOString());
    if (key === today) return '今天';
    if (key === yesterday) return '昨天';
    return key.replace(/-/g, '/');
  }

  function timelineTime(value: string): string {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '--:--:--';
    const pad = (part: number) => String(part).padStart(2, '0');
    return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
  }

  function timelineKindLabel(kind: GroupTimelineItem['kind']): string {
    if (kind === 'input') return '输入';
    if (kind === 'output') return '输出';
    if (kind === 'error') return '错误';
    if (kind === 'artifact') return '产出物';
    if (kind === 'system') return '系统事件';
    if (kind === 'end') return '运行结束';
    return '运行开始';
  }

  function timelineRunBadge(runId: string): string {
    return `#${shortRunId(runId)}`;
  }

  function timelineItemRun(item: GroupTimelineItem): ProductRun | null {
    return currentAgentRuns.find((run) => run.id === item.runId) || null;
  }

  function buildGroupArtifacts(group: RunGroup): GroupTimelineItem[] {
    return buildGroupTimeline(group).filter((item) => item.kind === 'artifact');
  }

  function runTypeLabel(run: ProductRun): string {
    return run.type === 'work_session' ? '运行记录' : '任务执行';
  }

  function runSourceLabel(run: ProductRun): string {
    const task = run.automation && run.automation !== '-' ? ` · ${run.automation}` : '';
    return `${runTypeLabel(run)} · ${run.agent || '-'}${task}`;
  }

  function runContextLabel(run: ProductRun): string {
    if (run.automationId) return taskNameForRun(run) || run.automationId;
    return manualConversationTitle(run);
  }

  function groupListTitle(view: RunView): string {
    if (view === 'agent') return '智能体组';
    if (view === 'task') return '任务组';
    if (view === 'agent_task') return '智能体+任务组';
    return '运行列表';
  }

  function groupEmptyTitle(view: RunView): string {
    if (view === 'task') return '没有可聚合的自动化任务运行';
    if (view === 'agent_task') return '没有可聚合的智能体任务组合';
    return '没有可聚合的智能体运行';
  }

  function groupEmptyDescription(view: RunView): string {
    if (view === 'task') return '当前筛选结果里没有关联自动化任务的运行记录。';
    if (view === 'agent_task') return '当前筛选结果里没有可按智能体和任务组合展示的运行记录。';
    return '当前筛选结果里没有可按智能体展示的运行记录。';
  }

  function startWatching(run: ProductRun): void {
    stopWatching();
    if (document.visibilityState === 'hidden') return;
    if (run.type !== 'work_session') return;
    const controller = new AbortController();
    watchAbort = controller;
    void watchRunLoop(run.id, controller);
  }

  function resumeVisibleWatch(): void {
    if (watchAbort || document.visibilityState === 'hidden') return;
    const run = visibleRuns.find((item) => item.id === selectedRunId) || selectedRun;
    if (run?.type === 'work_session') {
      startWatching(run);
    }
  }

  async function watchRunLoop(runId: string, controller: AbortController): Promise<void> {
    let retryDelay = 1000;
    while (!controller.signal.aborted) {
      try {
        await watchWorkSession(runId, (event) => {
          if (event.type === 'session') {
            retryDelay = 1000;
            runs = sortRunsByUpdatedTime(runs.map((item) => item.id === runId ? mergeSessionUpdate(item, sessionToRun(event.session)) : item));
          } else if (event.type === 'event') {
            runs = runs.map((item) => item.id === runId
              ? { ...item, events: [...item.events, { type: event.event.type, level: event.event.level, message: event.event.message, createdAt: event.event.createdAt }] }
              : item);
          } else if (event.type === 'cell') {
            runs = runs.map((item) => {
              if (item.id !== runId) return item;
              let messages = item.messages;
              for (const msg of cellToMessages(event.cell)) {
                if (msg.role === 'user' && messages.some((m) => m.role === 'user' && (m.source || m.content || '') === (msg.source || msg.content || ''))) {
                  continue;
                }
                messages = upsertMessage(messages, applyPendingChunks(msg));
              }
              return { ...item, messages };
            });
            if (event.cell.agent && !event.cell.running) {
              sendingMessage = false;
            }
            void scrollMessagesToBottom();
          } else if (event.type === 'chunk') {
            const applied = appendAgentChunk(runId, event.cellId, event.chunk);
            if (!applied) {
              appendPendingChunk(event.cellId, event.chunk);
            }
            void scrollMessagesToBottom();
          }
        }, controller.signal);
      } catch (err) {
        if (!controller.signal.aborted) {
          error = err instanceof Error ? err.message : String(err);
        }
      }
      if (!controller.signal.aborted) {
        await delay(retryDelay, controller.signal);
        retryDelay = Math.min(retryDelay * 2, 30000);
      }
    }
  }

  function stopWatching(): void {
    if (watchAbort) {
      watchAbort.abort();
      watchAbort = null;
    }
  }

  function stopSendingMessage(): void {
    if (messageAbort) {
      messageAbort.abort();
      messageAbort = null;
    }
  }

  function delay(ms: number, signal: AbortSignal): Promise<void> {
    return new Promise((resolve) => {
      const timer = window.setTimeout(resolve, ms);
      signal.addEventListener(
        'abort',
        () => {
          window.clearTimeout(timer);
          resolve();
        },
        { once: true },
      );
    });
  }

  async function stopSelectedRun(run: ProductRun): Promise<void> {
    if (sessionAction) return;
    sessionAction = { runId: run.id, type: 'stop' };
    error = '';
    // Optimistically update status so the UI responds immediately.
    runs = runs.map((item) => item.id === run.id ? { ...item, status: '停止中...', rawStatus: 'STOPPING' } : item);
    try {
      const updated = await stopWorkSession(run.id);
      runs = sortRunsByUpdatedTime(runs.map((item) => item.id === run.id ? mergeSessionUpdate(item, sessionToRun(updated)) : item));
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      // Restore previous status on failure.
      runs = runs.map((item) => item.id === run.id ? { ...item, status: run.status, rawStatus: run.rawStatus } : item);
    } finally {
      sessionAction = null;
    }
  }

  async function resumeSelectedRun(run: ProductRun): Promise<void> {
    if (sessionAction) return;
    sessionAction = { runId: run.id, type: 'resume' };
    error = '';
    // Optimistically update status so the UI responds immediately.
    runs = runs.map((item) => item.id === run.id ? { ...item, status: '恢复中...', rawStatus: 'RESUMING' } : item);
    try {
      const updated = await resumeWorkSession(run.id);
      runs = sortRunsByUpdatedTime(runs.map((item) => item.id === run.id ? mergeSessionUpdate(item, sessionToRun(updated)) : item));
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      // Restore previous status on failure.
      runs = runs.map((item) => item.id === run.id ? { ...item, status: run.status, rawStatus: run.rawStatus } : item);
    } finally {
      sessionAction = null;
    }
  }

  async function rerunAutomationRun(run: ProductRun): Promise<void> {
    if (automationActionRunId || !run.automationId) return;
    automationActionRunId = run.id;
    error = '';
    try {
      const payload = run.input?.trim() || '{}';
      JSON.parse(payload);
      const nextRun = await runAutomationTaskNow(run.automationId, payload);
      selectedRunId = nextRun.id;
      activeTab = 'automation_run';
      activeDetailTab = 'result';
      updateURL();
      await load();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      automationActionRunId = '';
    }
  }

  async function sendMessage(run: ProductRun): Promise<void> {
    if (!messageText.trim() || !canSendMessage(run)) return;
    stopSendingMessage();
    const controller = new AbortController();
    messageAbort = controller;
    sendingMessage = true;
    error = '';
    try {
      const sentText = messageText.trim();
      const isFirstUserMessage = !run.messages.some((m) => m.role === 'user');
      const pendingMessageId = `pending-${Date.now()}`;
      const pendingRenderKey = pendingMessageId;
      messageText = '';
      runs = runs.map((item) => item.id === run.id
        ? { ...item, title: isFirstUserMessage ? sentText : item.title, input: isFirstUserMessage ? sentText : item.input, messages: [...item.messages,
            { renderKey: `user-${pendingRenderKey}`, role: 'user', source: sentText, content: '', at: new Date().toISOString() },
            { id: pendingMessageId, renderKey: pendingRenderKey, role: 'agent', content: '', at: new Date().toISOString(), running: true, agent: run.agentProvider || 'codex', type: CellType.AGENT },
          ] }
        : item);
      await scrollMessagesToBottom();
      await sendWorkSessionMessageStream(run.id, run.agentProvider || 'codex', sentText, (event) => {
        if (event.type === 'started' && event.runId) {
          runs = runs.map((item) => {
            if (item.id !== run.id) return item;
            const pending = item.messages.find((message) => message.id === pendingMessageId);
            if (!pending) return item;
            if (item.messages.some((message) => message.id === event.runId)) {
              const messages = item.messages
                .filter((message) => message.id !== pendingMessageId)
                .map((message) => message.id === event.runId
                  ? { ...message, renderKey: pending.renderKey || message.renderKey, content: mergeMessageContent(message, pending) }
                  : message);
              return { ...item, messages };
            }
            return {
              ...item,
              messages: item.messages.map((message) => message.id === pendingMessageId ? { ...message, id: event.runId, renderKey: message.renderKey || pendingRenderKey } : message),
            };
          });
        } else if (event.type === 'chunk' && event.chunk) {
          const applied = appendAgentChunk(run.id, event.runId, event.chunk);
          if (applied) {
            void scrollMessagesToBottom();
          }
        } else if (event.type === 'completed' && event.run) {
          runs = runs.map((item) => item.id === run.id
            ? {
              ...item,
              messages: upsertMessage(item.messages, {
                id: event.run?.id || event.runId,
                renderKey: pendingRenderKey,
                role: 'agent',
                agent: event.run?.agent || run.agentProvider || 'codex',
                content: event.run?.output || event.run?.stopReason || '',
                at: event.run?.createdAt || new Date().toISOString(),
                running: event.run?.running,
                success: event.run?.success,
                exitCode: event.run?.exitCode,
                stopReason: event.run?.stopReason,
                agentSessionId: event.run?.agentSessionId,
                failed: Boolean(!event.run?.running && !event.run?.success),
                type: CellType.AGENT,
              }),
            }
            : item);
        }
      }, controller.signal);
      await scrollMessagesToBottom();
    } catch (err) {
      if (!controller.signal.aborted) {
        error = err instanceof Error ? err.message : String(err);
        await loadRunDetail(run);
      }
    } finally {
      if (messageAbort === controller) {
        messageAbort = null;
        sendingMessage = false;
      }
    }
  }

  function handleMessageKeydown(event: KeyboardEvent, run: ProductRun): void {
    if (event.key !== 'Enter' || event.shiftKey || event.metaKey || event.ctrlKey || event.altKey) {
      return;
    }
    event.preventDefault();
    if (!messageText.trim() || !canSendMessage(run)) {
      return;
    }
    void sendMessage(run);
  }
</script>


{#if error}
  <div class="alert danger">{error}</div>
{/if}
{#if message}
  <div class="alert success">{message}</div>
{/if}

<section class="panel runs-panel agent-runs-panel">
  <div class="runs-toolbar agent-runs-toolbar">
    <div class="run-command-metrics compact">
      <button><span>智能体</span><b>{agentObservations.length}</b></button>
      <button><span>运行中</span><b>{agentObservations.reduce((total, agent) => total + agent.runningCount, 0)}</b></button>
      <button><span>运行记录</span><b>{runs.length}</b></button>
      <button><span>异常</span><b>{runs.filter((run) => ['启动失败', '失败', '跳过', '已取消'].includes(run.status)).length}</b></button>
    </div>
    <div class="runs-filters agent-run-filters">
      <input class="filter-keyword" bind:value={agentKeyword} on:change={() => applyAgentFilters()} placeholder="筛选智能体名称、描述" aria-label="智能体关键词">
      <select bind:value={agentStatusFilter} on:change={() => applyAgentFilters()} aria-label="智能体状态">
        <option value="all">全部智能体</option>
        <option value="running">有运行中</option>
        <option value="failed">有失败</option>
        <option value="active">近期活跃</option>
        <option value="empty">无运行</option>
      </select>
      <select bind:value={agentSort} on:change={() => applyAgentFilters()} aria-label="智能体排序">
        <option value="recent">最近运行优先</option>
        <option value="failed">失败优先</option>
        <option value="name">名称</option>
      </select>
      <button on:click={load}>{loading ? '刷新中...' : '刷新'}</button>
    </div>
  </div>

  {#if loading && runs.length === 0}
    <div class="runs-workbench-layout loading-layout" aria-label="正在加载运行中心">
      <div class="run-list-card"><div class="run-list-head"><b>智能体列表</b><span>加载中</span></div><div class="run-list">{#each Array(5) as _}<div class="run-card skeleton-card"><span></span><span></span><span></span><span></span></div>{/each}</div></div>
      <div class="agent-workbench skeleton-run-detail"><div class="run-detail-head skeleton-detail-head"><div><span></span><span></span></div><div class="toolbar"><span></span><span></span></div></div><div class="detail-tabs skeleton-tabs">{#each Array(2) as _}<button disabled aria-label="加载中"><span></span></button>{/each}</div><div class="skeleton-content-block"></div></div>
    </div>
  {:else if agentObservations.length === 0}
    <div class="empty-state">
      <div class="empty-state-icon"><AntIcon definition={InboxOutlined} /></div>
      <h3>还没有智能体</h3>
      <p>先创建智能体，再从智能体页运行或创建自动化任务。</p>
      <div class="empty-state-actions"><button class="primary" on:click={() => window.location.assign(appPath('/agents'))}>创建智能体</button></div>
    </div>
  {:else if filteredAgentObservations.length === 0}
    <div class="empty-state">
      <div class="empty-state-icon"><AntIcon definition={FilterOutlined} /></div>
      <h3>没有匹配的智能体</h3>
      <p>当前筛选没有结果。</p>
      <div class="empty-state-actions"><button on:click={clearFilters}>清除筛选</button></div>
    </div>
  {:else}
    <div class="runs-workbench-layout" class:side-panel-closed={!sidePanelOpen}>
      <div class="run-list-card agent-observation-card">
        <div class="run-list-head">
          <b>智能体列表</b>
          <span>{filteredAgentObservations.length} 个</span>
        </div>
        <div class="run-list">
          {#each filteredAgentObservations as agent (agent.key)}
            <button type="button" class="run-card agent-observation-item" class:selected={selectedAgentObservation?.key === agent.key} aria-current={selectedAgentObservation?.key === agent.key ? 'page' : undefined} on:click={() => selectAgentObservation(agent)} title={`${agent.name} · ${agent.status}\n运行中 ${agent.runningCount} · 累计 ${agent.totalCount} · 失败 ${agent.failedCount}\n最近 ${agent.latestStatus} · ${formatTime(agent.latestAt)}\n自动化任务 ${agent.taskCount}`}>
              <span class="run-card-head"><b>{agent.name}</b><em class={agentStatusTone(agent)}>{agent.status}</em></span>
              <span class="run-card-meta">运行中 {agent.runningCount} · 累计 {agent.totalCount} · 失败 {agent.failedCount}</span>
              <span class="run-card-time">最近 {agent.latestStatus} · {formatTime(agent.latestAt)}</span>
              <span class="run-card-time">自动化任务 {agent.taskCount}</span>
            </button>
          {/each}
        </div>
      </div>

      <div class="agent-workbench">
        {#if selectedAgentObservation}
          <section class="workbench-summary-bar workbench-summary">
            <div class="summary-title">
              <h2>{selectedAgentObservation.name}</h2>
              <p>{selectedAgentObservation.description || selectedAgentObservation.status}</p>
            </div>
            <div class="summary-chips" aria-label="智能体运行汇总">
              <span>运行中 <b>{selectedAgentObservation.runningCount}</b></span>
              <span class="sep">·</span>
              <span>累计 <b>{selectedAgentObservation.totalCount}</b></span>
              <span class="sep">·</span>
              <span>今日 <b>{agentTodayRunCount(selectedAgentObservation)}</b></span>
              <span class="sep">·</span>
              <span>失败 <b>{selectedAgentObservation.failedCount}</b></span>
              <span class="sep">·</span>
              <span>自动化任务 <b>{selectedAgentObservation.taskCount}</b></span>
              <span class="sep">·</span>
              <span>最近 <b>{formatTime(selectedAgentObservation.latestAt)}</b></span>
              <button disabled={selectedAgentObservation.deleted || !selectedAgentObservation.agentId} on:click={() => goAgentDetail(selectedAgentObservation)}>查看详情</button>
              <button class="primary" disabled={selectedAgentObservation.deleted || !selectedAgentObservation.agentId} on:click={() => goAgentManualRun(selectedAgentObservation)}>运行</button>
            </div>
          </section>

          <div class="detail-tabs mode-tabs workbench-mode-tabs" role="tablist" aria-label="工作区模式">
            <button type="button" role="tab" aria-selected={workbenchMode === 'chat'} class:active={workbenchMode === 'chat'} disabled={recentManualConversationRuns.length === 0 && !selectedChatRun} on:click={() => setWorkbenchMode('chat')}>对话</button>
            <button type="button" role="tab" aria-selected={workbenchMode === 'tasks'} class:active={workbenchMode === 'tasks'} on:click={() => setWorkbenchMode('tasks')}>任务执行</button>
            <button type="button" role="tab" aria-selected={workbenchMode === 'timeline'} class:active={workbenchMode === 'timeline'} on:click={() => setWorkbenchMode('timeline')}>活动记录</button>
          </div>

          <div class="workbench-content" class:panel-closed={!sidePanelOpen && workbenchMode !== 'chat' && workbenchMode !== 'tasks'} class:picker-mode={workbenchMode === 'chat' || workbenchMode === 'tasks'}>
            <main class="workbench-main">
              {#if workbenchMode === 'chat'}
                {#if selectedChatRun}
                  <section class="chat-workbench">
                    <div class="chat-title-row">
                      <div>
                        <h2>{manualConversationTitle(selectedChatRun)}</h2>
                        <p>开始 {formatTime(selectedChatRun.startedAt)} · 最近 {formatTime(selectedChatRun.completedAt || selectedChatRun.startedAt)} · #{shortRunId(selectedChatRun.id)}</p>
                      </div>
                      <div class="toolbar">
                        <span class={`home-pill ${displayStatusClass(selectedChatRun)}`}>{displayStatus(selectedChatRun)}</span>
                        <button class:waiting={sessionAction?.runId === selectedChatRun.id} disabled={chatRunActionDisabled(selectedChatRun)} on:click={() => handleChatRunAction(selectedChatRun)}>{chatRunActionLabel(selectedChatRun)}</button>
                      </div>
                    </div>
                    <div class="run-chat-pane">
                      <div class="message-stack chat-message-stack" bind:this={messageScroll}>
                        {#if detailLoading}<div class="alert info">正在加载对话...</div>{/if}
                        {#if runMessages(selectedChatRun).length === 0}
                          <div class="run-result-summary"><span class={`home-pill ${displayStatusClass(selectedChatRun)}`}>{displayStatus(selectedChatRun)}</span><h3>暂无消息</h3><p>{selectedChatRun.errorSummary || '消息流加载后会显示在这里。'}</p></div>
                        {:else}
                          {#each runMessages(selectedChatRun) as message (message.renderKey || message.id || `${message.role}-${message.at}`)}
                            <article class={`message-card role-${message.role} ${messageStatusTone(message)}`}>
                              <div class="message-cell-head">
                                <div class="message-cell-summary">
                                  <div class="message-title-row">
                                    <b>{formatRole(message.role)}</b>
                                    <span class={`message-status ${messageStatusTone(message)}`}>{messageStatus(message)}</span>
                                    <small>{formatTime(message.at)}</small>
                                  </div>
                                </div>
                                {#if message.id}
                                  <button class="message-cell-id" type="button" title={message.id} on:click={(event) => copyText(message.id || '', event)}>#{shortRunId(message.id)}</button>
                                {/if}
                              </div>
                              <pre class="message-source">{messageTerminalText(message) || message.source || '-'}</pre>
                            </article>
                          {/each}
                        {/if}
                      </div>
                      <div class="run-message-composer" class:disabled={!canSendMessage(selectedChatRun) || sendingMessage}>
                        <textarea bind:value={messageText} disabled={!canSendMessage(selectedChatRun) || sendingMessage} on:keydown={(event) => handleMessageKeydown(event, selectedChatRun)} placeholder={isReplyPending(selectedChatRun) ? '等待当前回复完成' : messageInputHint(selectedChatRun)}></textarea>
                        <div class="run-input-actions">
                          <span class:waiting={isReplyPending(selectedChatRun)}>{isReplyPending(selectedChatRun) ? '等待当前回复完成' : messageInputHint(selectedChatRun)}</span>
                          <button class="run-send-button" disabled={!messageText.trim() || !canSendMessage(selectedChatRun) || sendingMessage} on:click={() => sendMessage(selectedChatRun)}>{sendingMessage ? '发送中' : '发送'}</button>
                        </div>
                      </div>
                    </div>
                  </section>
                {:else}
                  <div class="empty-state compact-page-empty">
                    <div class="empty-state-icon"><AntIcon definition={InboxOutlined} /></div>
                    <h3>暂无可继续的手动对话</h3>
                    <p>选择左侧的手动对话，或从智能体页发起新的运行。</p>
                    <div class="empty-state-actions"><button class="primary" disabled={selectedAgentObservation.deleted || !selectedAgentObservation.agentId} on:click={() => goAgentManualRun(selectedAgentObservation)}>运行</button></div>
                  </div>
                {/if}
              {:else if workbenchMode === 'tasks'}
                <section class="workbench-section overall-timeline-panel">
                  <div class="runs-filters timeline-filter-bar task-mode-filter-bar task-execution-filter-bar section-filter-row">
                    <input class="filter-keyword" value={timelineQuery} on:input={setTimelineQueryFromInput} placeholder="搜索任务、输入、输出、错误、产出物、短 ID" aria-label="任务执行关键词">
                    <select value={timelineTimeRange} on:change={setTimelineTimeRangeFromSelect} aria-label="时间范围">
                      <option value="all">全部时间</option>
                      <option value="today">今天</option>
                      <option value="7d">近 7 天</option>
                      <option value="30d">近 30 天</option>
                    </select>
                    <select value={timelineStatus} on:change={setTimelineStatusFromSelect} aria-label="运行状态">
                      {#each statusOptionsForTab('all') as option}
                        <option value={option.value}>{option.label}</option>
                      {/each}
                    </select>
                    <button type="button" class="icon-button" disabled={!timelineFiltersActive} on:click={clearTimelineFilters} title="清除筛选"><AntIcon definition={ClearOutlined} /></button>
                  </div>

                  <div class="section-title-row">
                    <h3>任务执行记录</h3>
                    <span>{taskModeRunCards.length} 条</span>
                  </div>
                  {#if groupDetailLoading}<div class="alert info">正在加载任务执行明细...</div>{/if}
                  {#if taskModeRunCards.length === 0}
                    <div class="empty-state compact-page-empty">
                      <div class="empty-state-icon"><AntIcon definition={FilterOutlined} /></div>
                      <h3>当前筛选无任务执行记录</h3>
                      <p>清除关键词、时间、状态或任务筛选后再查看。</p>
                      <div class="empty-state-actions"><button type="button" on:click={clearTimelineFilters}>清除筛选</button></div>
                    </div>
                  {:else}
                    <div class="aggregate-timeline-groups">
                      {#each taskModeDateGroups as group (group.key)}
                        <section class="timeline-day">
                          <h3>{group.label}</h3>
                          <div class="aggregate-timeline-list">
                            {#each group.items as card (card.id)}
                              <div class="task-execution-card-frame">
                                <button type="button" class={`timeline-run-card timeline-run-${card.kind} event-${statusClass(card.status)}`} class:selected={selectedRunId === card.id} aria-expanded={selectedRunId === card.id} on:click={() => toggleTaskExecutionDetail(card.run)}>
                                  <div class="timeline-run-time">
                                    <b>{timelineCardTimeRange(card)}</b>
                                    <span>#{shortRunId(card.id)}</span>
                                  </div>
                                  <div class="timeline-run-main">
                                    <div class="timeline-item-head">
                                      <b>{card.title} · #{shortRunId(card.id)}</b>
                                      <em>{card.status}</em>
                                    </div>
                                    {#if card.input}<p><span>输入：</span>{card.input}</p>{/if}
                                    {#if card.output}<p><span>输出：</span>{card.output}</p>{/if}
                                    {#if card.artifactCount > 0}<p><span>产出物：</span>{card.artifactCount} 个</p>{/if}
                                    {#if card.error || card.errorEventCount > 0}<p class="danger-text"><span>错误：</span>{card.error || `${card.errorEventCount} 个错误系统事件`}</p>{/if}
                                    <div class="timeline-run-actions">
                                      <span class="card-toggle-hint">{selectedRunId === card.id ? '点击卡片收起详情' : '点击卡片查看详情'}</span>
                                    </div>
                                  </div>
                                </button>
                                {#if selectedRunId === card.id}
                                  {@const detailRun = detailRunForCard(card)}
                                  <div class="task-execution-detail">
                                    {#if detailLoading}<div class="alert info">正在加载运行详情...</div>{/if}
                                    <div class="execution-detail-section">
                                      <div class="section-title-row">
                                        <h3>系统日志</h3>
                                        <span>{sortedRunEvents(detailRun).length} 条</span>
                                      </div>
                                      {#if sortedRunEvents(detailRun).length === 0}
                                        <div class="empty compact-empty">暂无系统日志。</div>
                                      {:else}
                                        <div class="event-list task-execution-log-list">
                                          {#each sortedRunEvents(detailRun) as event, index (`${event.createdAt}-${event.type}-${index}`)}
                                            <div class={`list-item event-item event-${eventLevelTone(event.level)}`}>
                                              <span>
                                                <b><span>{event.type}</span><em class="event-level-pill">{event.level || 'info'}</em></b>
                                                <small>{event.message} · {formatTime(event.createdAt)}</small>
                                              </span>
                                            </div>
                                          {/each}
                                        </div>
                                      {/if}
                                    </div>
                                    <div class="execution-detail-section">
                                      <div class="section-title-row">
                                        <h3>运行信息</h3>
                                        <span title={detailRun.id}>#{shortRunId(detailRun.id)}</span>
                                      </div>
                                      <div class="side-facts wide-facts execution-detail-facts">
                                        <div><span>运行类型</span><b>{runSourceText(detailRun)}</b></div>
                                        <div><span>自动化任务</span><b>{taskNameForRun(detailRun) || '-'}</b></div>
                                        <div><span>触发规则</span><b>{runTriggerText(detailRun)}</b></div>
                                        <div><span>开始时间</span><b>{formatTime(detailRun.startedAt)}</b></div>
                                        <div><span>结束时间</span><b>{formatTime(runEndedAt(detailRun))}</b></div>
                                        <div><span>运行耗时</span><b>{runDuration(detailRun)}</b></div>
                                        <div><span>运行状态</span><b>{detailRun.status}</b></div>
                                        <div><span>产出物</span><b>{detailRun.artifacts.length} 个</b></div>
                                        <div><span>错误信息</span><b>{detailRun.errorSummary || '-'}</b></div>
                                        <div><span>运行 ID</span><b title={detailRun.id}>{detailRun.id}</b></div>
                                      </div>
                                      {#if detailRun.automationId}
                                        <div class="timeline-run-actions">
                                          <button type="button" disabled={isRunTaskDeleted(detailRun)} on:click|stopPropagation={() => goTaskDetail(detailRun)}>{isRunTaskDeleted(detailRun) ? '任务已删除' : '查看任务配置'}</button>
                                        </div>
                                      {/if}
                                    </div>
                                  </div>
                                {/if}
                              </div>
                            {/each}
                          </div>
                        </section>
                      {/each}
                    </div>
                    {#if taskModeRunCards.length > visibleTaskModeRunCards.length}
                      <button class="load-more-button" on:click={() => timelineVisibleLimit += 100}>加载更多</button>
                    {:else}
                      <div class="timeline-end-note">已显示最近 {visibleTaskModeRunCards.length} 条任务执行。</div>
                    {/if}
                  {/if}
                </section>
              {:else}
                <section class="workbench-section overall-timeline-panel">
                  <div class="runs-filters timeline-filter-bar section-filter-row">
                    <input class="filter-keyword" value={timelineQuery} on:input={setTimelineQueryFromInput} placeholder="搜索输入、输出、错误、任务、产出物、短 ID" aria-label="时间线关键词">
                    <select value={timelineTimeRange} on:change={setTimelineTimeRangeFromSelect} aria-label="时间范围">
                      <option value="all">全部时间</option>
                      <option value="today">今天</option>
                      <option value="7d">近 7 天</option>
                      <option value="30d">近 30 天</option>
                    </select>
                    <select value={timelineStatus} on:change={setTimelineStatusFromSelect} aria-label="运行状态">
                      {#each statusOptionsForTab('all') as option}
                        <option value={option.value}>{option.label}</option>
                      {/each}
                    </select>
                    <select value={timelineFilter} on:change={setTimelineFilterFromSelect} aria-label="类型筛选">
                      <option value="all">全部类型</option>
                      {#each timelineFilterOptions().filter(o => o.value !== 'all') as option}
                        <option value={option.value}>{option.label}</option>
                      {/each}
                    </select>
                    <select value={selectedTaskId} on:change={setTimelineTaskFromSelect} aria-label="任务筛选">
                      <option value="">全部任务</option>
                      {#each timelineTaskFilterOptions() as item (item.key)}
                        <option value={item.taskId}>{item.deletedTask ? `已删除任务：${item.title}` : item.title}</option>
                      {/each}
                    </select>
                    <button type="button" class="icon-button" disabled={!timelineFiltersActive} on:click={clearTimelineFilters} title="清除筛选"><AntIcon definition={ClearOutlined} /></button>
                  </div>
                  {#if timelineQuery.trim() && timelineSearchMatches.length > 0}
                    <div class="timeline-quick-row">
                      <span>{timelineSearchMatches.length} 条匹配记录</span>
                      <button type="button" on:click={() => selectSearchMatch('previous')}>上一个</button>
                      <button type="button" on:click={() => selectSearchMatch('next')}>下一个</button>
                    </div>
                  {/if}

                  <div class="section-title-row">
                    <h3>活动记录</h3>
                    <span>{filteredTimelineRunCards.length} 条</span>
                  </div>
                  {#if groupDetailLoading}<div class="alert info">正在加载时间线明细...</div>{/if}
                  {#if selectedAgentObservation.totalCount === 0 && currentAgentTasks.length === 0}
                    <div class="empty-state compact-page-empty">
                      <div class="empty-state-icon"><AntIcon definition={InboxOutlined} /></div>
                      <h3>该智能体还没有运行记录</h3>
                      <p>可从智能体页运行，或创建绑定该智能体的自动化任务。</p>
                    </div>
                  {:else if filteredTimelineRunCards.length === 0}
                    <div class="empty-state compact-page-empty">
                      <div class="empty-state-icon"><AntIcon definition={FilterOutlined} /></div>
                      <h3>当前筛选无结果</h3>
                      <p>清除关键词、时间、类型、状态或任务筛选后再查看。</p>
                      <div class="empty-state-actions"><button type="button" on:click={clearTimelineFilters}>清除筛选</button></div>
                    </div>
                  {:else}
                    <div class="aggregate-timeline-groups">
                      {#each timelineRunDateGroups as group (group.key)}
                        <section class="timeline-day">
                          <h3>{group.label}</h3>
                          <div class="aggregate-timeline-list">
                            {#each group.items as card (card.id)}
                              <div class="task-execution-card-frame">
                                <button type="button" class={`timeline-run-card timeline-run-${card.kind} event-${statusClass(card.status)}`} class:selected={selectedRunId === card.id} aria-expanded={selectedRunId === card.id} on:click={() => toggleActivityDetail(card.run)}>
                                  <div class="timeline-run-time">
                                    <b>{timelineCardTimeRange(card)}</b>
                                    <span>#{shortRunId(card.id)}</span>
                                  </div>
                                  <div class="timeline-run-main">
                                    <div class="timeline-item-head">
                                      <b>{timelineCardSummaryLine(card)}</b>
                                      <em>{card.status}</em>
                                    </div>
                                    <p><span>{card.kind === 'manual' ? '对话' : '任务'}：</span>{card.title}</p>
                                    {#if shouldShowTimelineField(timelineFilter, 'input') && card.input}<p class:highlighted={timelineFilter === 'io'}><span>输入：</span>{card.input}</p>{/if}
                                    {#if shouldShowTimelineField(timelineFilter, 'output') && card.output}<p class:highlighted={timelineFilter === 'io'}><span>输出：</span>{card.output}</p>{/if}
                                    {#if shouldShowTimelineField(timelineFilter, 'artifact') && card.artifactCount > 0}<p class:highlighted={timelineFilter === 'artifact'}><span>产出物：</span>{card.artifactCount} 个</p>{/if}
                                    {#if shouldShowTimelineField(timelineFilter, 'error') && (card.error || card.errorEventCount > 0)}<p class="danger-text" class:highlighted={timelineFilter === 'error'}><span>错误：</span>{card.error || `${card.errorEventCount} 个错误系统事件`}</p>{/if}
                                    <div class="timeline-run-actions">
                                      <span class="card-toggle-hint">{selectedRunId === card.id ? '点击卡片收起详情' : '点击卡片查看详情'}</span>
                                    </div>
                                  </div>
                                </button>
                                {#if selectedRunId === card.id}
                                  {@const detailRun = detailRunForCard(card)}
                                  <div class="task-execution-detail">
                                    {#if detailLoading}<div class="alert info">正在加载运行详情...</div>{/if}
                                    <div class="execution-detail-section">
                                      <div class="section-title-row">
                                        <h3>系统日志</h3>
                                        <span>{sortedRunEvents(detailRun).length} 条</span>
                                      </div>
                                      {#if sortedRunEvents(detailRun).length === 0}
                                        <div class="empty compact-empty">暂无系统日志。</div>
                                      {:else}
                                        <div class="event-list task-execution-log-list">
                                          {#each sortedRunEvents(detailRun) as event, index (`${event.createdAt}-${event.type}-${index}`)}
                                            <div class={`list-item event-item event-${eventLevelTone(event.level)}`}>
                                              <span>
                                                <b><span>{event.type}</span><em class="event-level-pill">{event.level || 'info'}</em></b>
                                                <small>{event.message} · {formatTime(event.createdAt)}</small>
                                              </span>
                                            </div>
                                          {/each}
                                        </div>
                                      {/if}
                                    </div>
                                    <div class="execution-detail-section">
                                      <div class="section-title-row">
                                        <h3>运行信息</h3>
                                        <span title={detailRun.id}>#{shortRunId(detailRun.id)}</span>
                                      </div>
                                      <div class="side-facts wide-facts execution-detail-facts">
                                        <div><span>运行类型</span><b>{runSourceText(detailRun)}</b></div>
                                        <div><span>自动化任务</span><b>{taskNameForRun(detailRun) || '-'}</b></div>
                                        <div><span>触发规则</span><b>{runTriggerText(detailRun)}</b></div>
                                        <div><span>开始时间</span><b>{formatTime(detailRun.startedAt)}</b></div>
                                        <div><span>结束时间</span><b>{formatTime(runEndedAt(detailRun))}</b></div>
                                        <div><span>运行耗时</span><b>{runDuration(detailRun)}</b></div>
                                        <div><span>运行状态</span><b>{detailRun.status}</b></div>
                                        <div><span>产出物</span><b>{detailRun.artifacts.length} 个</b></div>
                                        <div><span>错误信息</span><b>{detailRun.errorSummary || '-'}</b></div>
                                        <div><span>运行 ID</span><b title={detailRun.id}>{detailRun.id}</b></div>
                                      </div>
                                      {#if detailRun.automationId}
                                        <div class="timeline-run-actions">
                                          <button type="button" disabled={isRunTaskDeleted(detailRun)} on:click|stopPropagation={() => goTaskDetail(detailRun)}>{isRunTaskDeleted(detailRun) ? '任务已删除' : '查看任务配置'}</button>
                                        </div>
                                      {:else if canContinueManualConversation(detailRun)}
                                        <div class="timeline-run-actions">
                                          <button type="button" on:click|stopPropagation={() => continueManualConversation(detailRun)}>继续对话</button>
                                        </div>
                                      {/if}
                                    </div>
                                  </div>
                                {/if}
                              </div>
                            {/each}
                          </div>
                        </section>
                      {/each}
                    </div>
                    {#if filteredTimelineRunCards.length > visibleTimelineRunCards.length}
                      <button class="load-more-button" on:click={() => timelineVisibleLimit += 100}>加载更多</button>
                    {:else}
                      <div class="timeline-end-note">已显示最近 {visibleTimelineRunCards.length} 条活动记录。</div>
                    {/if}
                  {/if}
                </section>
              {/if}
            </main>

            {#if workbenchMode === 'chat'}
              <aside class="auxiliary-panel context-picker-panel">
                <div class="auxiliary-panel-head">
                  <div>
                    <h3>最近对话</h3>
                    <p>{recentManualConversationRuns.length} 个手动对话</p>
                  </div>
                </div>
                {#if recentManualConversationRuns.length === 0}
                  <div class="empty compact-empty">暂无手动对话。</div>
                {:else}
                  <div class="context-picker-list recent-manual-list">
                    {#each recentManualConversationRuns as run (run.id)}
                      <button type="button" class="run-card task-selector-card" class:selected={selectedChatRun?.id === run.id} on:click={() => continueManualConversation(run)} title={`${manualConversationTitle(run)} · ${run.status}\n开始 ${formatTime(run.startedAt)}\n最近 ${formatTime(run.completedAt || run.startedAt)}`}>
                        <span class="run-card-head"><b>{manualConversationTitle(run)}</b><em class={statusClass(run.status)}>{run.status}</em></span>
                        <span class="run-card-meta">开始 {formatTime(run.startedAt)}</span>
                        <span class="run-card-time">最近 {formatTime(run.completedAt || run.startedAt)}</span>
                      </button>
                    {/each}
                    {#if currentAgentBaseRuns.filter(r => !r.automationId).length >= 10 && (manualConversationVisible < currentAgentBaseRuns.filter(r => !r.automationId).length || sessionHasMore)}
                      <button type="button" class="view-more-button" on:click={loadMoreManualConversations}>查看更多</button>
                    {/if}
                  </div>
                {/if}
              </aside>
            {:else if workbenchMode === 'tasks'}
              <aside class="auxiliary-panel context-picker-panel">
                <div class="auxiliary-panel-head">
                  <div>
                    <h3>自动化任务</h3>
                    <p>{automationTaskItems.length} 个任务</p>
                  </div>
                  {#if selectedTaskId}
                    <button type="button" on:click={clearTaskFilter}>全部</button>
                  {/if}
                </div>
                {#if automationTaskItems.length === 0}
                  <div class="empty compact-empty">暂无关联自动化任务。</div>
                {:else}
                  <div class="context-picker-list task-selector-list">
                    {#each automationTaskItems as item (item.key)}
                      <button type="button" class="run-card task-selector-card" class:selected={selectedTaskId === item.taskId || selectedContextKey === item.key} on:click={() => selectTaskForTimeline(item)} title={`${item.title} · ${item.latestStatus}\n最近 ${formatTime(item.latestAt)}${item.failedCount > 0 ? `\n失败 ${item.failedCount} 次` : item.nextTriggerAt ? `\n下一次触发 ${formatTime(item.nextTriggerAt)}` : ''}`}>
                        <span class="run-card-head"><b>{item.title}</b><em class={conversationItemTone(item)}>{item.latestStatus}</em></span>
                        <span class="run-card-meta">最近 {formatTime(item.latestAt)}</span>
                        {#if item.failedCount > 0}
                          <span class="run-card-error">失败 {item.failedCount} 次</span>
                        {:else}
                          <span class="run-card-time">{item.deletedTask ? '已删除任务' : item.nextTriggerAt ? `下一次触发 ${formatTime(item.nextTriggerAt)}` : '暂无下一次触发'}</span>
                        {/if}
                      </button>
                    {/each}
                  </div>
                {/if}
              </aside>
            {:else if sidePanelOpen}
              <aside class="auxiliary-panel">
                <div class="auxiliary-panel-head">
                  <div>
                    <h3>辅助面板</h3>
                    <p>{workbenchMode === 'chat' ? '本次运行信息' : auxiliaryRun ? '选中对象详情' : selectedAutomationTaskItem ? '自动化任务摘要' : '智能体摘要'}</p>
                  </div>
                  <button on:click={() => sidePanelOpen = false}>关闭</button>
                </div>

                {#if auxiliaryRun && (workbenchMode === 'chat' || auxiliaryPanelKind === 'run')}
                  <section class="run-instance-panel auxiliary-run-panel">
                    <div class="run-instance-head">
                      <div>
                        <h3>{auxiliaryRun.automationId ? '本次任务触发运行' : '手动对话摘要'}</h3>
                        <p class="copy-line detail-id">
                          <span title={auxiliaryRun.id}>#{shortRunId(auxiliaryRun.id)}</span>
                          <span class="icon-copy" role="button" tabindex="0" title={copiedId === auxiliaryRun.id ? '已复制' : '复制运行 ID'} on:click={(event) => copyText(auxiliaryRun.id, event)} on:keydown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); void copyText(auxiliaryRun.id); } }}><AntIcon definition={CopyOutlined} /></span>
                          {#if copiedId === auxiliaryRun.id && copyNotice}<span class="copy-tip" class:bad={!copyNotice.ok}>{copyNotice.text}</span>{/if}
                        </p>
                      </div>
                      <div class="toolbar">
                        <span class={`home-pill ${displayStatusClass(auxiliaryRun)}`}>{displayStatus(auxiliaryRun)}</span>
                        {#if auxiliaryRun.automationId}
                          <button disabled={isRunTaskDeleted(auxiliaryRun)} on:click={() => goTaskDetail(auxiliaryRun)}>{isRunTaskDeleted(auxiliaryRun) ? '任务已删除' : '查看任务详情'}</button>
                        {:else if workbenchMode !== 'chat' && canContinueManualConversation(auxiliaryRun)}
                          <button class="primary" on:click={() => continueManualConversation(auxiliaryRun)}>继续对话</button>
                        {/if}
                      </div>
                    </div>
                    <div class="run-instance-body activity-detail-body">
                      {#if detailLoading}<div class="alert info">正在加载运行详情...</div>{/if}
                      <section class="execution-detail-section">
                        <div class="section-title-row">
                          <h3>系统日志</h3>
                          <span>{sortedRunEvents(auxiliaryRun).length} 条</span>
                        </div>
                        {#if sortedRunEvents(auxiliaryRun).length === 0}
                          <div class="empty compact-empty">暂无系统日志。</div>
                        {:else}
                          <div class="event-list task-execution-log-list">
                            {#each sortedRunEvents(auxiliaryRun) as event, index (`${event.createdAt}-${event.type}-${index}`)}
                              <div class={`list-item event-item event-${eventLevelTone(event.level)}`}>
                                <span>
                                  <b><span>{event.type}</span><em class="event-level-pill">{event.level || 'info'}</em></b>
                                  <small>{event.message} · {formatTime(event.createdAt)}</small>
                                </span>
                              </div>
                            {/each}
                          </div>
                        {/if}
                      </section>
                      <section class="execution-detail-section run-info-section">
                        <div class="section-title-row">
                          <h3>运行信息</h3>
                          <span title={auxiliaryRun.id}>#{shortRunId(auxiliaryRun.id)}</span>
                        </div>
                        <div class="side-facts wide-facts">
                          <div><span>运行类型</span><b>{runSourceText(auxiliaryRun)}</b></div>
                          <div><span>自动化任务</span><b>{taskNameForRun(auxiliaryRun) || '-'}</b></div>
                          <div><span>触发规则</span><b>{runTriggerText(auxiliaryRun)}</b></div>
                          <div><span>开始时间</span><b>{formatTime(auxiliaryRun.startedAt)}</b></div>
                          <div><span>结束时间</span><b>{formatTime(runEndedAt(auxiliaryRun))}</b></div>
                          <div><span>运行耗时</span><b>{runDuration(auxiliaryRun)}</b></div>
                          <div><span>运行状态</span><b>{auxiliaryRun.status}</b></div>
                          <div><span>产出物</span><b>{auxiliaryRun.artifacts.length} 个</b></div>
                          <div><span>错误信息</span><b>{auxiliaryRun.errorSummary || '-'}</b></div>
                          <div><span>运行 ID</span><b title={auxiliaryRun.id}>{auxiliaryRun.id}</b></div>
                        </div>
                      </section>
                    </div>
                  </section>
                {:else if selectedAutomationTaskItem}
                  <section class="run-instance-panel auxiliary-task-panel">
                    <div class="section-title-row">
                      <h3>{selectedAutomationTaskItem.title}</h3>
                      <span>{selectedAutomationTaskItem.deletedTask ? '已删除任务' : '任务筛选'}</span>
                    </div>
                    <div class="side-facts wide-facts task-filter-facts">
                      <div><span>最近状态</span><b>{selectedAutomationTaskItem.latestStatus}</b></div>
                      <div><span>最近运行</span><b>{formatTime(selectedAutomationTaskItem.latestAt)}</b></div>
                      <div><span>下一次触发</span><b>{formatTime(selectedAutomationTaskItem.nextTriggerAt)}</b></div>
                      <div><span>失败记录</span><b>{selectedAutomationTaskItem.failedCount > 0 ? `${selectedAutomationTaskItem.failedCount} 次` : '无'}</b></div>
                    </div>
                    <div class="toolbar">
                      <button disabled={selectedAutomationTaskItem.deletedTask} on:click={() => window.location.assign(appPath(`/automation-tasks?task=${encodeURIComponent(selectedAutomationTaskItem.taskId)}`))}>{selectedAutomationTaskItem.deletedTask ? '任务已删除' : '查看任务详情'}</button>
                      <button on:click={clearTaskFilter}>清除任务筛选</button>
                    </div>
                  </section>
                {:else}
                  <section class="run-instance-panel">
                    <div class="section-title-row">
                      <h3>{selectedAgentObservation.name}</h3>
                      <span>{selectedAgentObservation.status}</span>
                    </div>
                    <div class="side-facts wide-facts">
                      <div><span>当前运行中</span><b>{selectedAgentObservation.runningCount}</b></div>
                      <div><span>累计运行记录</span><b>{selectedAgentObservation.totalCount}</b></div>
                      <div><span>今日运行</span><b>{agentTodayRunCount(selectedAgentObservation)}</b></div>
                      <div><span>失败数</span><b>{selectedAgentObservation.failedCount}</b></div>
                      <div><span>最近运行</span><b>{formatTime(selectedAgentObservation.latestAt)}</b></div>
                      <div><span>自动化任务</span><b>{selectedAgentObservation.taskCount}</b></div>
                    </div>
                  </section>
                {/if}
              </aside>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}
</section>

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
    </div>
  </div>
{/if}
