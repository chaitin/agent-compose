import { capabilityClient, configClient } from './client';
import { apiFetchJson } from './http';

export type CapabilityGatewayConfig = {
  addr: string;
  tokenSet: boolean;
};

export type CapabilityStatus = {
  configured: boolean;
  ok: boolean;
  status: string;
  serviceCount: number;
  error: string;
  runtimeConfigured: boolean;
  proxyListenConfigured: boolean;
  proxyTargetConfigured: boolean;
};

export async function getCapabilityGatewayConfig(): Promise<CapabilityGatewayConfig> {
  const response = await configClient.getCapabilityGatewayConfig({});
  return { addr: response.addr, tokenSet: response.tokenSet };
}

// updateCapabilityGatewayConfig saves the OctoBus connection. An empty token
// keeps the existing one (the backend never returns the token).
export async function updateCapabilityGatewayConfig(addr: string, token: string): Promise<CapabilityGatewayConfig> {
  const response = await configClient.updateCapabilityGatewayConfig({ addr, token });
  return { addr: response.addr, tokenSet: response.tokenSet };
}

export async function getCapabilityStatus(): Promise<CapabilityStatus> {
  const response = await capabilityClient.getCapabilityStatus({});
  return {
    configured: response.configured,
    ok: response.ok,
    status: response.status,
    serviceCount: response.serviceCount,
    error: response.error,
    runtimeConfigured: response.runtimeConfigured,
    proxyListenConfigured: response.proxyListenConfigured,
    proxyTargetConfigured: response.proxyTargetConfigured,
  };
}

export type CapabilitySet = {
  id: string;
  name: string;
  description: string;
  enabled: boolean;
};

export async function listCapabilitySets(): Promise<CapabilitySet[]> {
  const response = await capabilityClient.listCapabilitySets({});
  return response.capsets.map((item) => ({
    id: item.id,
    name: item.name,
    description: item.description,
    enabled: item.enabled,
  }));
}

export type CapabilityMethodInfo = {
  methodFullName: string;
  serviceId: string;
  instanceId: string;
  backendInstanceStatus: string;
};

export async function getCapabilityCatalog(capsetId: string): Promise<CapabilityMethodInfo[]> {
  const response = await capabilityClient.getCapabilityCatalog({ capsetId });
  return response.methods.map((method) => ({
    methodFullName: method.methodFullName,
    serviceId: method.serviceId,
    instanceId: method.instanceId,
    backendInstanceStatus: method.backendInstanceStatus,
  }));
}

export type EnvItem = {
  name: string;
  value: string;
  secret: boolean;
  valueKnown: boolean;
};

export type WorkspaceFileEntry = {
  path: string;
  dir: boolean;
  size: number;
  updatedAt: string;
};

export type WorkspacePreset = {
  id: string;
  name: string;
  type: string;
  configJson: string;
  comment: string;
  createdAt: string;
  updatedAt: string;
};

export type WorkspacePresetInput = {
  name: string;
  type: string;
  configJson: string;
  comment: string;
};

export type WebhookSource = {
  id: string;
  name: string;
  enabled: boolean;
  provider: string;
  topicPrefix: string;
  hasToken: boolean;
  bodyLimitBytes: number;
  createdAt: string;
  updatedAt: string;
};

export type WebhookSourceInput = {
  name: string;
  enabled: boolean;
  provider: string;
  topicPrefix: string;
  token: string;
  clearToken: boolean;
  bodyLimitBytes: number;
};

type WebhookSourceResponseItem = {
  id: string;
  name: string;
  enabled: boolean;
  provider: string;
  topic_prefix: string;
  has_token: boolean;
  body_limit_bytes: number;
  created_at: string;
  updated_at: string;
};

type WebhookSourceListResponse = {
  items?: WebhookSourceResponseItem[];
};

type WebhookSourceResponse = {
  source?: WebhookSourceResponseItem;
};

export async function listEnvItems(): Promise<EnvItem[]> {
  const response = await configClient.getGlobalEnvConfig({});
  return response.envItems.map((item) => ({
    name: item.name,
    value: item.secret && item.value ? '' : item.value,
    secret: item.secret,
    valueKnown: !item.secret || !item.value,
  }));
}

export async function updateEnvItems(envItems: EnvItem[]): Promise<void> {
  await configClient.updateGlobalEnvConfig({
    envItems: envItems
      .filter((item) => item.name.trim())
      .map((item) => ({
        name: item.name.trim(),
        value: item.value,
        secret: item.secret,
      })),
  });
}

export async function listWorkspacePresets(): Promise<WorkspacePreset[]> {
  const response = await configClient.listWorkspaceConfigs({});
  return response.workspaces.map((item) => ({
    id: item.id,
    name: item.name,
    type: item.type,
    configJson: item.configJson,
    comment: item.comment,
    createdAt: item.createdAt,
    updatedAt: item.updatedAt,
  }));
}

export async function createWorkspacePreset(input: WorkspacePresetInput): Promise<WorkspacePreset> {
  const response = await configClient.createWorkspaceConfig({
    name: input.name.trim(),
    type: input.type,
    configJson: input.configJson,
    comment: input.comment.trim(),
  });
  if (!response.workspace) {
    throw new Error('Workspace 配置保存失败');
  }
  return workspaceFromResponse(response.workspace);
}

export async function updateWorkspacePreset(id: string, input: WorkspacePresetInput): Promise<WorkspacePreset> {
  const response = await configClient.updateWorkspaceConfig({
    workspaceId: id,
    name: input.name.trim(),
    type: input.type,
    configJson: input.configJson,
    comment: input.comment.trim(),
  });
  if (!response.workspace) {
    throw new Error('Workspace 配置保存失败');
  }
  return workspaceFromResponse(response.workspace);
}

export async function deleteWorkspacePreset(id: string): Promise<void> {
  await configClient.deleteWorkspaceConfig({ workspaceId: id });
}

type WorkspaceFilesResponse = {
  files?: Array<{ path: string; dir: boolean; size: number; updated_at: string }>;
};

function workspaceFilesFromResponse(response: WorkspaceFilesResponse): WorkspaceFileEntry[] {
  return (response.files ?? []).map((item) => ({
    path: item.path,
    dir: item.dir,
    size: item.size,
    updatedAt: item.updated_at,
  }));
}

export async function listWorkspaceFiles(workspaceId: string): Promise<WorkspaceFileEntry[]> {
  const response = await apiFetchJson<WorkspaceFilesResponse>(
    `/api/agent-compose/workspaces/${encodeURIComponent(workspaceId)}/files`,
  );
  return workspaceFilesFromResponse(response);
}

export async function uploadWorkspaceArchive(workspaceId: string, file: File): Promise<WorkspaceFileEntry[]> {
  const formData = new FormData();
  formData.set('upload_type', 'archive');
  formData.set('file', file);
  const response = await apiFetchJson<WorkspaceFilesResponse>(
    `/api/agent-compose/workspaces/${encodeURIComponent(workspaceId)}/upload`,
    { method: 'POST', body: formData },
  );
  return workspaceFilesFromResponse(response);
}

export async function listWebhookSources(): Promise<WebhookSource[]> {
  const response = await apiFetchJson<WebhookSourceListResponse>('/api/webhook-sources');
  return (response.items ?? []).map(webhookSourceFromResponse);
}

export async function saveWebhookSource(id: string, input: WebhookSourceInput): Promise<WebhookSource> {
  const response = await apiFetchJson<WebhookSourceResponse>(
    `/api/webhook-sources/${encodeURIComponent(id)}`,
    {
      method: 'PUT',
      body: JSON.stringify({
        name: input.name.trim(),
        enabled: input.enabled,
        provider: input.provider.trim(),
        topic_prefix: input.topicPrefix.trim(),
        token: input.token,
        clear_token: input.clearToken,
        body_limit_bytes: input.bodyLimitBytes,
      }),
    },
  );
  if (!response.source) {
    throw new Error('Webhook 来源保存失败');
  }
  return webhookSourceFromResponse(response.source);
}

export async function deleteWebhookSource(id: string): Promise<void> {
  await apiFetch(`/api/webhook-sources/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

function workspaceFromResponse(item: NonNullable<Awaited<ReturnType<typeof configClient.createWorkspaceConfig>>['workspace']>): WorkspacePreset {
  return {
    id: item.id,
    name: item.name,
    type: item.type,
    configJson: item.configJson,
    comment: item.comment,
    createdAt: item.createdAt,
    updatedAt: item.updatedAt,
  };
}

function webhookSourceFromResponse(item: WebhookSourceResponseItem): WebhookSource {
  return {
    id: item.id,
    name: item.name,
    enabled: item.enabled,
    provider: item.provider,
    topicPrefix: item.topic_prefix,
    hasToken: item.has_token,
    bodyLimitBytes: item.body_limit_bytes,
    createdAt: item.created_at,
    updatedAt: item.updated_at,
  };
}
