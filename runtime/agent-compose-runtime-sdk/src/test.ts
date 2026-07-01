import { RuntimeUnsupportedError } from "./errors.js";
import type { RuntimeCapabilityCallOptions, RuntimeCapabilityRequest } from "./capability.js";
import type { RuntimeServiceInvokeOptions, RuntimeServiceRequest } from "./service.js";

export type RuntimeMockHandler<TRequest = unknown, TOutput = unknown> = (
  request: TRequest,
) => TOutput | Promise<TOutput>;

export interface RuntimeMockRegistry {
  services?: Record<string, RuntimeMockHandler<RuntimeServiceRequest, unknown>>;
  capabilities?: Record<string, RuntimeMockHandler<RuntimeCapabilityRequest, unknown>>;
}

export interface RuntimeMock {
  invokeService<TInput = unknown, TOutput = unknown>(
    serviceName: string,
    method: string,
    input: TInput,
    options?: RuntimeServiceInvokeOptions,
  ): Promise<TOutput>;
  service: {
    invoke<TInput = unknown, TOutput = unknown>(
      serviceName: string,
      method: string,
      input: TInput,
      options?: RuntimeServiceInvokeOptions,
    ): Promise<TOutput>;
  };
  capability: {
    call<TInput = unknown, TOutput = unknown>(
      method: string,
      input: TInput,
      options?: RuntimeCapabilityCallOptions,
    ): Promise<TOutput>;
  };
}

export function mockRuntime(registry: RuntimeMockRegistry = {}): RuntimeMock {
  async function invokeService<TInput = unknown, TOutput = unknown>(
    serviceName: string,
    method: string,
    input: TInput,
    _options: RuntimeServiceInvokeOptions = {},
  ): Promise<TOutput> {
    const normalizedServiceName = serviceName.trim();
    const normalizedMethod = method.trim();
    if (!normalizedServiceName) {
      throw new Error("service name is required");
    }
    if (!normalizedMethod) {
      throw new Error("service method is required");
    }
    const key = `${normalizedServiceName}.${normalizedMethod}`;
    const handler = registry.services?.[key] ?? registry.services?.[normalizedServiceName];
    if (!handler) {
      throw new RuntimeUnsupportedError(`mock runtime service handler is not configured for ${key}`);
    }
    return await handler({ service: normalizedServiceName, method: normalizedMethod, input }) as TOutput;
  }

  return {
    invokeService,
    service: {
      invoke: invokeService,
    },
    capability: {
      async call<TInput = unknown, TOutput = unknown>(
        method: string,
        input: TInput,
        _options: RuntimeCapabilityCallOptions = {},
      ): Promise<TOutput> {
        const normalizedMethod = method.trim();
        if (!normalizedMethod) {
          throw new Error("capability method is required");
        }
        const handler = registry.capabilities?.[normalizedMethod];
        if (!handler) {
          throw new RuntimeUnsupportedError(`mock runtime capability handler is not configured for ${normalizedMethod}`);
        }
        return await handler({ method: normalizedMethod, input }) as TOutput;
      },
    },
  };
}
