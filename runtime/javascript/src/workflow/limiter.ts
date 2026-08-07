import { WorkflowAbortError } from "./errors.js";

export class WorkflowLimiter {
  private active = 0;
  private readonly waiting: Array<() => void> = [];

  constructor(readonly concurrency: number) {}

  async run<T>(signal: AbortSignal, operation: () => Promise<T>): Promise<T> {
    await this.acquire(signal);
    try {
      if (signal.aborted) {
        throw new WorkflowAbortError();
      }
      return await operation();
    } finally {
      this.release();
    }
  }

  private async acquire(signal: AbortSignal): Promise<void> {
    if (signal.aborted) {
      throw new WorkflowAbortError();
    }
    if (this.active < this.concurrency) {
      this.active++;
      return;
    }
    await new Promise<void>((resolve, reject) => {
      const resume = () => {
        signal.removeEventListener("abort", abort);
        this.active++;
        resolve();
      };
      const abort = () => {
        const index = this.waiting.indexOf(resume);
        if (index >= 0) {
          this.waiting.splice(index, 1);
        }
        reject(new WorkflowAbortError());
      };
      signal.addEventListener("abort", abort, { once: true });
      this.waiting.push(resume);
    });
  }

  private release(): void {
    this.active--;
    this.waiting.shift()?.();
  }
}
