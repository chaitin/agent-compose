export class RuntimeUnsupportedError extends Error {
  readonly code = "ERR_AGENT_COMPOSE_RUNTIME_UNSUPPORTED";

  constructor(message: string) {
    super(message);
    this.name = "RuntimeUnsupportedError";
  }
}

export class RuntimeBridgeError extends Error {
  readonly code = "ERR_AGENT_COMPOSE_RUNTIME_BRIDGE";

  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = "RuntimeBridgeError";
  }
}
