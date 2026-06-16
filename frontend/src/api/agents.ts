import { agentClient, agentDefinitionClient } from './client';
import {
  AgentAvailabilityStatus,
  AgentCurrentRunStatus,
  AgentHealthStatus,
  AgentWorkFilesSource,
  type AgentDefinition as ProtoAgentDefinition,
} from '../gen/proto/agentcompose/v1/agentcompose_pb.js';

export type AgentWorkFiles = {
  source: 'empty' | 'file' | 'git';
  workspaceId: string;
  workspaceName: string;
  workspaceType: string;
  summary: string;
  configJson: string;
};

export type AgentEnvItem = {
  name: string;
  value: string;
  secret: boolean;
};

export type AgentRunSummary = {
  text: string;
  runningSessionCount: number;
  runningLoaderRunCount: number;
};

export type AgentLatestRun = {
  runType: string;
  status: string;
  runId: string;
  title: string;
  at: string;
};

export type AgentDefinition = {
  id: string;
  name: string;
  description: string;
  enabled: boolean;
  provider: string;
  model: string;
  systemPrompt: string;
  runtimeImageId: string;
  driver: string;
  guestImage: string;
  workspaceId: string;
  envItems: AgentEnvItem[];
  configJson: string;
  capsetIds: string[];
  availability: string;
  availabilityClass: 'green' | 'amber' | 'red';
  health: string;
  healthClass: 'green' | 'amber' | 'red';
  workFiles: AgentWorkFiles;
  currentRun: AgentRunSummary;
  latestRun: AgentLatestRun | null;
  createdAt: string;
  updatedAt: string;
  deletedAt: string;
};

export type AgentDefinitionInput = {
  name: string;
  description: string;
  enabled: boolean;
  provider: string;
  model: string;
  systemPrompt: string;
  runtimeImageId: string;
  driver: string;
  guestImage: string;
  workspaceId: string;
  envItems: AgentEnvItem[];
  configJson: string;
  capsetIds: string[];
};

export async function listAgentDefinitions(query = ''): Promise<AgentDefinition[]> {
  const response = await agentDefinitionClient.listAgentDefinitions({
    query,
    includeDisabled: true,
    limit: 200,
  });
  return response.agents.map(agentFromProto);
}

export async function createAgentDefinition(input: AgentDefinitionInput): Promise<AgentDefinition> {
  const response = await agentDefinitionClient.createAgentDefinition(requestFromInput(input));
  if (!response.agent) {
    throw new Error('智能体保存失败');
  }
  return agentFromProto(response.agent);
}

export async function updateAgentDefinition(id: string, input: AgentDefinitionInput): Promise<AgentDefinition> {
  const response = await agentDefinitionClient.updateAgentDefinition({
    agentId: id,
    ...requestFromInput(input),
  });
  if (!response.agent) {
    throw new Error('智能体保存失败');
  }
  return agentFromProto(response.agent);
}

export async function deleteAgentDefinition(id: string): Promise<void> {
  await agentDefinitionClient.deleteAgentDefinition({ agentId: id });
}

export async function createAgentDefinitionSession(input: {
  agentId: string;
  title: string;
  workspaceId: string;
  driver: string;
  guestImage: string;
  message: string;
  provider: string;
}): Promise<string> {
  const response = await agentDefinitionClient.createAgentSession({
    agentId: input.agentId,
    title: input.title,
    workspaceId: input.workspaceId,
    driver: input.driver,
    guestImage: input.guestImage,
  });
  const sessionId = response.session?.summary?.sessionId ?? '';
  const message = input.message.trim();
  if (sessionId && message) {
    await agentClient.sendAgentMessage({
      sessionId,
      agent: normalizeAgentProvider(input.provider),
      message,
    });
  }
  return sessionId;
}

function normalizeAgentProvider(value: string): string {
  const provider = value.trim().toLowerCase();
  if (provider === 'claude' || provider === 'gemini' || provider === 'codex' || provider === 'deepseek') {
    return provider;
  }
  throw new Error(`不支持的智能体 Provider：${value || '-'}`);
}

function requestFromInput(input: AgentDefinitionInput): AgentDefinitionInput {
  return {
    name: input.name.trim(),
    description: input.description.trim(),
    enabled: input.enabled,
    provider: normalizeAgentProvider(input.provider),
    model: input.model.trim(),
    systemPrompt: input.systemPrompt.trim(),
    runtimeImageId: input.runtimeImageId.trim(),
    driver: input.driver.trim(),
    guestImage: input.guestImage.trim(),
    workspaceId: input.workspaceId.trim(),
    envItems: input.envItems
      .map((item) => ({ name: item.name.trim(), value: item.value, secret: item.secret }))
      .filter((item) => item.name),
    configJson: input.configJson.trim() || '{}',
    capsetIds: input.capsetIds.map((id) => id.trim()).filter(Boolean),
  };
}

function agentFromProto(item: ProtoAgentDefinition): AgentDefinition {
  return {
    id: item.agentId,
    name: item.name,
    description: item.description,
    enabled: item.enabled,
    provider: item.provider || 'codex',
    model: item.model,
    systemPrompt: item.systemPrompt,
    runtimeImageId: item.runtimeImageId,
    driver: item.driver,
    guestImage: item.guestImage,
    workspaceId: item.workFiles?.workspaceId ?? '',
    envItems: item.envItems.map((env) => ({ name: env.name, value: env.value, secret: env.secret })),
    configJson: item.configJson || '{}',
    capsetIds: item.capsetIds ?? [],
    availability: availabilityLabel(item.availabilityStatus),
    availabilityClass: availabilityClass(item.availabilityStatus),
    health: healthLabel(item.healthStatus),
    healthClass: healthClass(item.healthStatus),
    workFiles: {
      source: workFilesSource(item.workFiles?.source),
      workspaceId: item.workFiles?.workspaceId ?? '',
      workspaceName: item.workFiles?.workspaceName ?? '',
      workspaceType: item.workFiles?.workspaceType ?? '',
      summary: item.workFiles?.summary ?? '',
      configJson: item.workFiles?.configJson ?? '',
    },
    currentRun: {
      text: item.currentRunSummary?.text || currentRunLabel(item.currentRunSummary?.status),
      runningSessionCount: Number(item.currentRunSummary?.runningSessionCount ?? 0),
      runningLoaderRunCount: Number(item.currentRunSummary?.runningLoaderRunCount ?? 0),
    },
    latestRun: item.latestRunSummary
      ? {
        runType: item.latestRunSummary.runType,
        status: item.latestRunSummary.status,
        runId: item.latestRunSummary.runId,
        title: item.latestRunSummary.title,
        at: item.latestRunSummary.at,
      }
      : null,
    createdAt: item.createdAt,
    updatedAt: item.updatedAt,
    deletedAt: item.deletedAt,
  };
}

function availabilityLabel(status: AgentAvailabilityStatus): string {
  if (status === AgentAvailabilityStatus.AVAILABLE) return '可用';
  if (status === AgentAvailabilityStatus.UNAVAILABLE) return '不可用';
  if (status === AgentAvailabilityStatus.VALIDATION_FAILED) return '校验失败';
  return '未知';
}

function availabilityClass(status: AgentAvailabilityStatus): 'green' | 'amber' | 'red' {
  if (status === AgentAvailabilityStatus.AVAILABLE) return 'green';
  if (status === AgentAvailabilityStatus.UNAVAILABLE) return 'amber';
  return 'red';
}

function healthLabel(status: AgentHealthStatus): string {
  if (status === AgentHealthStatus.HEALTHY) return '健康';
  if (status === AgentHealthStatus.AT_RISK) return '有风险';
  return '未知';
}

function healthClass(status: AgentHealthStatus): 'green' | 'amber' | 'red' {
  if (status === AgentHealthStatus.HEALTHY) return 'green';
  if (status === AgentHealthStatus.AT_RISK) return 'amber';
  return 'red';
}

function workFilesSource(source?: AgentWorkFilesSource): AgentWorkFiles['source'] {
  if (source === AgentWorkFilesSource.FILE_WORKSPACE) return 'file';
  if (source === AgentWorkFilesSource.GIT_WORKSPACE) return 'git';
  return 'empty';
}

function currentRunLabel(status?: AgentCurrentRunStatus): string {
  if (status === AgentCurrentRunStatus.HAS_RUNNING_SESSION) return '运行中';
  return '暂无运行';
}
