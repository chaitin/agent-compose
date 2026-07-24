import { resolveCodexPath } from "./codex-path.js";
import {
  codexThreadStateMetadata,
  codexThreadStartWarning,
  decideCodexThreadResume,
  hashSystemContext,
} from "./codex-thread-resume.js";
import { stringEnv } from "./env.js";
import { warn } from "./mpi.js";
import { buildPromptRuntimeOptions } from "./prompt.js";
import { ClaudeRunner } from "./runners/claude.js";
import { CodexRunner } from "./runners/codex.js";
import { OpenCodeRunner } from "./runners/opencode.js";
import { PiRunner } from "./runners/pi.js";
import { readStoredThread, writeStoredThread } from "./session-state.js";
import type { TextWriter, TranscriptTextWriter } from "./transcript.js";
import type { AgentResult, Provider, RunnerOptions } from "./types.js";

export interface InteractiveStartOptions {
  provider?: string;
  stateRoot?: string;
  workspace?: string;
  home?: string;
  model?: string;
  outputSchemaFile?: string;
}

export type EmitInteractiveFrame = (type: string, fields?: object) => void;

export interface InteractiveSession {
  start(): Promise<void>;
  runHumanMessage(message: string): Promise<void>;
  finish(stopReason: string): Promise<AgentResult>;
}

export class UnsupportedProviderError extends Error {
  readonly code = "unsupported_provider";

  constructor(readonly provider: Provider) {
    super(`interactive stream is not supported for provider ${provider}`);
    this.name = "UnsupportedProviderError";
  }
}

export class CodexInteractiveSession implements InteractiveSession {
  private readonly runner: CodexRunner;
  private readonly writer: BufferedTextWriter;
  private readonly result: AgentResult;
  private readonly systemContextHash: string;
  private turnCount = 0;
  private thread?: {
    id?: string | null;
    runStreamed(input: string, options?: unknown): Promise<{ events: AsyncIterable<unknown> }>;
  };

  constructor(
    private readonly options: RunnerOptions,
    private readonly emit: EmitInteractiveFrame,
  ) {
    this.writer = new BufferedTextWriter();
    this.runner = new CodexRunner(options, this.writer);
    this.systemContextHash = hashSystemContext(options.systemContext);
    this.result = {
      provider: "codex",
      threadId: "",
      stopReason: "completed",
      finalText: "",
      transcript: "",
      stderr: "",
    };
  }

  async start(): Promise<void> {
    const { Codex } = await import("@openai/codex-sdk");
    const stored = await readStoredThread(this.options.stateRoot, "codex");
    const codex = new Codex({
      codexPathOverride: resolveCodexPath(),
      env: stringEnv(),
      ...(this.options.systemContext
        ? { config: { developer_instructions: this.options.systemContext } }
        : {}),
    });
    const resumeDecision = decideCodexThreadResume(stored, this.systemContextHash);
    this.thread = resumeDecision.action === "resume"
      ? codex.resumeThread(resumeDecision.threadId, this.runner.threadOptions())
      : codex.startThread(this.runner.threadOptions());
    if (resumeDecision.action === "start") {
      const warning = codexThreadStartWarning(resumeDecision.reason);
      if (warning) {
        warn(warning);
      }
    }
    const thread = this.thread;
    this.result.threadId = resumeDecision.action === "resume" ? resumeDecision.threadId : thread.id || "";
    this.emit("started", {
      provider: "codex",
      threadId: this.result.threadId,
    });
  }

  async runHumanMessage(message: string): Promise<void> {
    if (!this.thread) {
      throw new Error("stream has not been started");
    }
    if (this.turnCount > 0) {
      this.writer.beginTurn();
    }
    this.turnCount++;
    const { events } = await this.thread.runStreamed(
      message,
      this.options.outputSchema ? { outputSchema: this.options.outputSchema } : undefined,
    );
    for await (const event of events) {
      const sdkEvent = event as Record<string, unknown>;
      this.emit("agent_event", { event: sdkEvent });
      this.runner.handleEvent(sdkEvent, this.result);
    }
    this.result.threadId = this.thread.id || this.result.threadId;
    this.result.transcript = this.runner.transcript();
    if (!this.result.finalText && this.result.transcript) {
      this.result.finalText = this.result.transcript;
    }
    await this.writeThreadState();
    this.emit("agent_turn_completed", {
      provider: "codex",
      threadId: this.result.threadId,
      finalText: this.result.finalText,
    });
  }

  async finish(stopReason: string): Promise<AgentResult> {
    this.result.stopReason = stopReason;
    this.result.threadId = this.thread?.id || this.result.threadId;
    this.result.transcript = this.runner.transcript();
    if (!this.result.finalText && this.result.transcript) {
      this.result.finalText = this.result.transcript;
    }
    if (this.result.threadId) {
      await this.writeThreadState();
    }
    return { ...this.result };
  }

  private async writeThreadState(): Promise<void> {
    await writeStoredThread(
      this.options.stateRoot,
      "codex",
      this.result.threadId,
      new Date(),
      codexThreadStateMetadata(this.systemContextHash),
    );
  }
}

interface PromptTurnRunner {
  runPrompt(message: string): Promise<AgentResult>;
}

class PromptRunnerInteractiveSession implements InteractiveSession {
  private readonly writer: InteractiveTextWriter;
  private readonly runner: PromptTurnRunner;
  private readonly result: AgentResult;
  private started = false;
  private turnCount = 0;

  constructor(
    private readonly provider: Provider,
    private readonly options: RunnerOptions,
    private readonly emit: EmitInteractiveFrame,
    createRunner: (writer: TranscriptTextWriter) => PromptTurnRunner,
  ) {
    this.writer = new InteractiveTextWriter(provider, emit);
    this.runner = createRunner(this.writer);
    this.result = {
      provider,
      threadId: "",
      stopReason: "completed",
      finalText: "",
      transcript: "",
      stderr: "",
    };
  }

  async start(): Promise<void> {
    const stored = await readStoredThread(this.options.stateRoot, this.provider);
    this.result.threadId = stored?.threadId || "";
    this.started = true;
    this.emit("started", {
      provider: this.provider,
      threadId: this.result.threadId,
    });
  }

  async runHumanMessage(message: string): Promise<void> {
    if (!this.started) {
      throw new Error("stream has not been started");
    }
    if (this.turnCount > 0) {
      this.writer.beginTurn();
    }
    this.turnCount++;
    const turnResult = await this.runner.runPrompt(message);
    this.result.threadId = turnResult.threadId || this.result.threadId;
    this.result.stopReason = turnResult.stopReason;
    this.result.finalText = turnResult.finalText;
    this.result.transcript = turnResult.transcript;
    this.result.stderr = turnResult.stderr;
    this.emit("agent_turn_completed", {
      provider: this.provider,
      threadId: this.result.threadId,
      finalText: this.result.finalText,
      stopReason: this.result.stopReason,
    });
  }

  async finish(stopReason: string): Promise<AgentResult> {
    this.result.stopReason = stopReason;
    this.result.transcript = this.writer.transcript();
    return { ...this.result };
  }
}

class BufferedTextWriter implements TextWriter {
  private readonly chunks: string[] = [];

  write(text: string): void {
    if (text) {
      this.chunks.push(text);
    }
  }

  line(text = ""): void {
    this.write(text.endsWith("\n") ? text : `${text}\n`);
  }

  beginTurn(): void {
    if (this.chunks.length === 0) {
      return;
    }
    const last = this.chunks[this.chunks.length - 1] || "";
    if (!last.endsWith("\n")) {
      this.chunks.push("\n");
    }
  }

  transcript(): string {
    return this.chunks.join("").trimEnd();
  }
}

class InteractiveTextWriter extends BufferedTextWriter implements TranscriptTextWriter {
  constructor(
    private readonly provider: Provider,
    private readonly emit: EmitInteractiveFrame,
  ) {
    super();
  }

  override write(text: string): void {
    if (!text) {
      return;
    }
    super.write(text);
    this.emit("agent_event", {
      event: {
        type: "output",
        provider: this.provider,
        text,
      },
    });
  }
}

export async function createInteractiveSession(
  startOptions: InteractiveStartOptions,
  emit: EmitInteractiveFrame,
): Promise<InteractiveSession> {
  const options = await buildPromptRuntimeOptions(startOptions);
  let session: InteractiveSession;
  switch (options.provider) {
    case "codex":
      session = new CodexInteractiveSession(options, emit);
      break;
    case "claude":
      session = new PromptRunnerInteractiveSession(
        "claude",
        options,
        emit,
        (writer) => new ClaudeRunner(options, writer),
      );
      break;
    case "opencode":
      session = new PromptRunnerInteractiveSession(
        "opencode",
        options,
        emit,
        (writer) => new OpenCodeRunner(options, writer),
      );
      break;
    case "pi":
      session = new PromptRunnerInteractiveSession(
        "pi",
        options,
        emit,
        (writer) => new PiRunner(options, writer),
      );
      break;
    default:
      throw new UnsupportedProviderError(options.provider);
  }
  await session.start();
  return session;
}
