#!/usr/bin/env python3
"""Move Service receiver files into the app package.

The first package split placed several files in domain/transport directories
while they still define methods on *Service. Go requires receiver methods to be
defined in the same package as the receiver type, so this script moves those
files to internal/app and changes their package declaration.
"""

from __future__ import annotations

import shutil
import subprocess
from pathlib import Path


MOVE_MAP = {
    "internal/dashboard/overview.go": "internal/app/dashboard_overview.go",
    "internal/image/ensure.go": "internal/app/image_ensure.go",
    "internal/loader/events.go": "internal/app/loader_events.go",
    "internal/project/down.go": "internal/app/project_down.go",
    "internal/project/service.go": "internal/app/project_service.go",
    "internal/run/preparation.go": "internal/app/run_preparation.go",
    "internal/run/session.go": "internal/app/run_session.go",
    "internal/session/reconcile.go": "internal/app/session_reconcile.go",
    "internal/transport/connect/agent_handler.go": "internal/app/agent_handler.go",
    "internal/transport/connect/capability_handler.go": "internal/app/capability_handler.go",
    "internal/transport/connect/exec_handler.go": "internal/app/exec_handler.go",
    "internal/transport/connect/image_handler.go": "internal/app/image_handler.go",
    "internal/transport/connect/loader_handler.go": "internal/app/loader_handler.go",
    "internal/transport/connect/run_handler.go": "internal/app/run_handler.go",
    "internal/transport/http/llm_facade.go": "internal/app/llm_facade.go",
    "internal/transport/http/webhook.go": "internal/app/webhook.go",
    "internal/transport/http/workspace.go": "internal/app/workspace_http.go",
}


def main() -> None:
    for src_name, dst_name in MOVE_MAP.items():
        src = Path(src_name)
        dst = Path(dst_name)
        if not src.exists() and dst.exists():
            continue
        if not src.exists():
            continue
        dst.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(str(src), str(dst))
        text = dst.read_text(encoding="utf-8")
        text = text.replace("package dashboard", "package app", 1)
        text = text.replace("package image", "package app", 1)
        text = text.replace("package loader", "package app", 1)
        text = text.replace("package project", "package app", 1)
        text = text.replace("package run", "package app", 1)
        text = text.replace("package session", "package app", 1)
        text = text.replace("package connecttransport", "package app", 1)
        text = text.replace("package httptransport", "package app", 1)
        dst.write_text(text, encoding="utf-8")
    subprocess.run(["gofmt", "-w", "internal/app"], check=True)


if __name__ == "__main__":
    main()
