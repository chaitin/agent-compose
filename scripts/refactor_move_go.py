#!/usr/bin/env python3
"""Mechanical first-pass Go file mover for the agentcompose package split.

The script is intentionally conservative:
  * dry-run is the default;
  * apply mode uses git mv when possible;
  * package declaration rewrites are simple and local to moved files;
  * import/dependency repair is left for the follow-up compile-fix phase.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


DEFAULT_MAP = Path("scripts/refactor_move_map.json")
GO_PACKAGE_RE = re.compile(r"^package\s+\w+\s*$", re.MULTILINE)


@dataclass(frozen=True)
class Move:
    src: Path
    dst: Path
    package: str
    replacements: tuple[tuple[str, str], ...] = ()


@dataclass(frozen=True)
class Keep:
    src: Path
    reason: str
    dst: Path | None = None
    package: str | None = None


def load_plan(path: Path) -> tuple[list[Move], list[Keep]]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    moves = [
        Move(
            Path(item["src"]),
            Path(item["dst"]),
            item["package"],
            tuple((pair["old"], pair["new"]) for pair in item.get("replacements", [])),
        )
        for item in raw.get("moves", [])
    ]
    keeps = [
        Keep(
            Path(item["src"]),
            item.get("reason", ""),
            Path(item["dst"]) if item.get("dst") else None,
            item.get("package"),
        )
        for item in raw.get("keeps", [])
    ]
    return moves, keeps


def repo_root() -> Path:
    result = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return Path(result.stdout.strip())


def is_git_tracked(path: Path) -> bool:
    result = subprocess.run(
        ["git", "ls-files", "--error-unmatch", str(path)],
        text=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def git_mv(src: Path, dst: Path) -> None:
    dst.parent.mkdir(parents=True, exist_ok=True)
    if is_git_tracked(src):
        subprocess.run(["git", "mv", str(src), str(dst)], check=True)
    else:
        shutil.move(str(src), str(dst))


def rewrite_package(path: Path, package: str) -> bool:
    text = path.read_text(encoding="utf-8")
    rewritten, count = GO_PACKAGE_RE.subn(f"package {package}", text, count=1)
    if count == 0:
        return False
    if rewritten != text:
        path.write_text(rewritten, encoding="utf-8")
    return True


def apply_replacements(path: Path, replacements: tuple[tuple[str, str], ...]) -> None:
    if not replacements:
        return
    text = path.read_text(encoding="utf-8")
    for old, new in replacements:
        text = text.replace(old, new)
    path.write_text(text, encoding="utf-8")


def validate(moves: list[Move], keeps: list[Keep]) -> list[str]:
    errors: list[str] = []
    seen_src: set[Path] = set()
    seen_dst: set[Path] = set()

    for move in moves:
        if move.src in seen_src:
            errors.append(f"duplicate source in moves: {move.src}")
        if move.dst in seen_dst:
            errors.append(f"duplicate destination in moves: {move.dst}")
        seen_src.add(move.src)
        seen_dst.add(move.dst)

        if not move.src.exists():
            errors.append(f"missing source: {move.src}")
        if move.dst.exists():
            errors.append(f"destination already exists: {move.dst}")

    for keep in keeps:
        if keep.src in seen_src:
            errors.append(f"source is both move and keep: {keep.src}")
        if not keep.src.exists():
            errors.append(f"missing kept source: {keep.src}")

    return errors


def unmapped_go(moves: list[Move], keeps: list[Keep]) -> list[Path]:
    covered = {move.src for move in moves} | {keep.src for keep in keeps}
    all_go: list[Path] = []
    for root in (Path("pkg/agentcompose"), Path("pkg/driver")):
        all_go.extend(sorted(root.glob("*.go")))
    return [path for path in all_go if path not in covered]


def print_plan(moves: list[Move], keeps: list[Keep], unmapped: list[Path]) -> None:
    print(f"move_count={len(moves)}")
    print(f"keep_count={len(keeps)}")
    print(f"unmapped_count={len(unmapped)}")
    print()

    print("moves:")
    for move in moves:
        print(f"  {move.src} -> {move.dst}  package={move.package}")

    print()
    print("deferred keeps:")
    for keep in keeps:
        target = ""
        if keep.dst:
            target = f" -> {keep.dst}"
            if keep.package:
                target += f"  package={keep.package}"
        suffix = f"  reason={keep.reason}" if keep.reason else ""
        print(f"  {keep.src}{target}{suffix}")

    if unmapped:
        print()
        print("unmapped:")
        for path in unmapped:
            print(f"  {path}")


def apply_plan(moves: list[Move]) -> None:
    moved: list[Move] = []
    for move in moves:
        git_mv(move.src, move.dst)
        moved.append(move)

    for move in moved:
        if not rewrite_package(move.dst, move.package):
            raise RuntimeError(f"could not rewrite package declaration in {move.dst}")
        apply_replacements(move.dst, move.replacements)

    subprocess.run(["gofmt", "-w", *[str(move.dst) for move in moved]], check=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--map", type=Path, default=DEFAULT_MAP)
    parser.add_argument("--apply", action="store_true", help="perform moves")
    parser.add_argument("--dry-run", action="store_true", help="print plan only")
    args = parser.parse_args()

    root = repo_root()
    os.chdir(root)

    moves, keeps = load_plan(args.map)
    errors = validate(moves, keeps)
    unmapped = unmapped_go(moves, keeps)
    print_plan(moves, keeps, unmapped)

    if errors:
        print()
        print("validation_errors:")
        for error in errors:
            print(f"  {error}")
        return 2

    if unmapped:
        print()
        print("refusing to apply while covered source directories have unmapped Go files")
        if not args.apply:
            return 0
        return 2

    if args.apply:
        print()
        print("applying move plan")
        apply_plan(moves)
        return 0

    print()
    print("dry-run only; pass --apply to move files and rewrite package declarations")
    return 0


if __name__ == "__main__":
    sys.exit(main())
