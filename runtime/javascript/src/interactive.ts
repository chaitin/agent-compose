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
import type { AgentEvent } from "./agent-event.js";
import { ClaudeRunner } from "./runners/claude.js";
import { CodexRunner } from "./runners/codex.js";
import { DshRunner } from "./runners/dsh.js";
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
  effort?: "low" | "medium" | "high" | "xhigh" | "max";
  skills?: string[];
  outputSchemaFile?: string;
  abortController?: AbortController;
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
      finalTextSource: "none",
      transcript: "",
      stderr: "",
    };
  }

  async start(): Promise<void> {
    const { Codex } = await import("@openai/codex-sdk");
    const stored = await readStoredThread(this.options.sessionRoot, "codex");
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
    this.result.finalText = "";
    this.result.finalTextSource = "none";
    try {
      const turnOptions = this.options.outputSchema || this.options.abortController
        ? {
          ...(this.options.outputSchema ? { outputSchema: this.options.outputSchema } : {}),
          ...(this.options.abortController ? { signal: this.options.abortController.signal } : {}),
        }
        : undefined;
      const { events } = await this.thread.runStreamed(message, turnOptions);
      for await (const event of events) {
        // The runner's onEvent sink publishes the neutral events; the raw SDK
        // event is no longer forwarded verbatim.
        this.runner.handleEvent(event as Record<string, unknown>, this.result);
      }
    } catch (error) {
      if (!this.options.abortController?.signal.aborted) {
        throw error;
      }
    }
    if (this.options.abortController?.signal.aborted) {
      this.result.stopReason = "cancelled";
    }
    this.result.threadId = this.thread.id || this.result.threadId;
    this.result.transcript = this.runner.transcript();
    if (!this.result.finalText && this.result.transcript) {
      this.result.finalText = this.result.transcript;
      this.result.finalTextSource = "transcript_fallback";
    }
    await this.writeThreadState();
    this.emit("agent_turn_completed", {
      provider: "codex",
      threadId: this.result.threadId,
      finalText: this.result.finalText,
      finalTextSource: this.result.finalTextSource,
    });
  }

  async finish(stopReason: string): Promise<AgentResult> {
    this.result.stopReason = stopReason;
    this.result.threadId = this.thread?.id || this.result.threadId;
    this.result.transcript = this.runner.transcript();
    if (this.result.threadId) {
      await this.writeThreadState();
    }
    return { ...this.result };
  }

  private async writeThreadState(): Promise<void> {
    await writeStoredThread(
      this.options.sessionRoot,
      "codex",
      this.result.threadId,
      new Date(),
      codexThreadStateMetadata(this.systemContextHash),
    );
  }
}


/**
 * Wrap the runner's neutral events into `agent_event` frames.
 *
 * The frame carries only the event; the daemon derives the frame name and the
 * transcript text from its `kind` (see promptAttachProjector.agentEventText),
 * so there is one source of truth for both.
 */
function agentEventEmitter(emit: EmitInteractiveFrame): (event: AgentEvent) => void {
  return (event) => {
    emit("agent_event", { event });
  };
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
    this.writer = new InteractiveTextWriter();
    this.runner = createRunner(this.writer);
    this.result = {
      provider,
      threadId: "",
      stopReason: "completed",
      finalText: "",
      finalTextSource: "none",
      transcript: "",
      stderr: "",
    };
  }

  async start(): Promise<void> {
    const stored = await readStoredThread(this.options.sessionRoot, this.provider);
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
    this.result.finalTextSource = turnResult.finalTextSource;
    this.result.transcript = turnResult.transcript;
    this.result.stderr = turnResult.stderr;
    this.emit("agent_turn_completed", {
      provider: this.provider,
      threadId: this.result.threadId,
      finalText: this.result.finalText,
      finalTextSource: this.result.finalTextSource,
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

/**
 * Transcript accumulator for the prompt-runner sessions. It no longer
 * synthesises `agent_event` frames: structured events now come from the
 * runner's own `onEvent` sink, so the writer is purely a text buffer.
 */
class InteractiveTextWriter extends BufferedTextWriter implements TranscriptTextWriter {}

export async function createInteractiveSession(
  startOptions: InteractiveStartOptions,
  emit: EmitInteractiveFrame,
): Promise<InteractiveSession> {
  const base = await buildPromptRuntimeOptions(startOptions);
  // One sink for every provider: the runners publish neutral events, the
  // session classes no longer synthesise frames of their own.
  const options = { ...base, onEvent: agentEventEmitter(emit) };
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
    case "dsh":
      session = new PromptRunnerInteractiveSession(
        "dsh",
        options,
        emit,
        (writer) => new DshRunner(options, writer),
      );
      break;
    default:
      throw new UnsupportedProviderError(options.provider);
  }
  await session.start();
  return session;
}
