export class WorkflowAbortError extends Error {
  readonly code = "WORKFLOW_ABORTED";

  constructor(message = "workflow aborted") {
    super(message);
    this.name = "WorkflowAbortError";
  }
}

export function isWorkflowAbort(error: unknown): boolean {
  return error instanceof WorkflowAbortError || (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    error.code === "WORKFLOW_ABORTED"
  );
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function errorData(error: unknown) {
  if (error instanceof Error) {
    return { message: error.message, name: error.name, stack: error.stack };
  }
  return { message: String(error) };
}
