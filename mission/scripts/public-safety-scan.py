#!/usr/bin/env python3

import argparse
import os
import re
import stat
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


MAX_FILES = 4096
MAX_FILE_BYTES = 1024 * 1024
MAX_TOTAL_BYTES = 16 * 1024 * 1024

PRIVATE_KEY = re.compile(
    r"-----BEGIN (?:RSA |OPENSSH |EC |DSA )?PRIVATE KEY-----"
)
OPENAI_CREDENTIAL = re.compile(
    r"(?<![A-Za-z0-9_-])(?:sk|rk)-(?:proj-)?[A-Za-z0-9_-]{20,}"
)
GITHUB_CREDENTIAL = re.compile(
    r"(?<![A-Za-z0-9_])(?:gh[pousr]_[A-Za-z0-9]{20,}|"
    r"github_pat_[A-Za-z0-9_]{20,})"
)
LOCAL_USER_PATH = re.compile(r"/" r"Users/([^/\s\"']+)(?:/[^\s\"']*)?")
LITERAL_SECRET_ASSIGNMENT = re.compile(
    r"""(?ix)
    (?<![A-Za-z0-9_])
    ["']?(?:token|secret|api[_-]?key|password)["']?
    \s*(?::=|=|:)\s*
    (?:
        (?P<quote>["'])(?P<quoted>[^"'\r\n]{8,})(?P=quote)
        |
        (?P<bare>[A-Za-z0-9+/=_-]{16,})
    )
    """
)
PLACEHOLDER_VALUES = {
    "DUMMY",
    "EXAMPLE",
    "PLACEHOLDER",
    "REDACTED",
    "REDACTED_DO_NOT_USE_REAL_TOKEN",
}


@dataclass(frozen=True, order=True)
class Finding:
    path: str
    line: int
    category: str


class ScanError(Exception):
    pass


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Scan bounded repository text for private paths and credentials."
    )
    parser.add_argument("--root", default=".", help="Repository scan root.")
    parser.add_argument(
        "paths",
        nargs="+",
        help="Files or directories under the scan root.",
    )
    return parser.parse_args()


def lexical_path(root: Path, selected: str) -> Path:
    candidate = Path(selected)
    if not candidate.is_absolute():
        candidate = root / candidate
    candidate = Path(os.path.abspath(candidate))
    try:
        candidate.relative_to(root)
    except ValueError as error:
        raise ScanError(f"input is outside scan root: {selected}") from error
    return candidate


def relative_name(root: Path, path: Path) -> str:
    return path.relative_to(root).as_posix()


def selected_files(
    root: Path, selected_paths: Iterable[str]
) -> tuple[list[Path], list[Finding]]:
    files: list[Path] = []
    findings: list[Finding] = []
    seen: set[Path] = set()

    for selected in selected_paths:
        candidate = lexical_path(root, selected)
        try:
            mode = candidate.lstat().st_mode
        except FileNotFoundError as error:
            raise ScanError(f"scan input does not exist: {selected}") from error

        if stat.S_ISLNK(mode):
            findings.append(Finding(relative_name(root, candidate), 0, "symlink"))
            continue
        if stat.S_ISREG(mode):
            if candidate not in seen:
                seen.add(candidate)
                files.append(candidate)
            continue
        if not stat.S_ISDIR(mode):
            raise ScanError(f"scan input is not a regular file or directory: {selected}")

        for current_root, directories, names in os.walk(candidate, followlinks=False):
            current = Path(current_root)
            retained_directories: list[str] = []
            for name in sorted(directories):
                child = current / name
                if child.is_symlink():
                    findings.append(Finding(relative_name(root, child), 0, "symlink"))
                else:
                    retained_directories.append(name)
            directories[:] = retained_directories

            for name in sorted(names):
                child = current / name
                child_mode = child.lstat().st_mode
                if stat.S_ISLNK(child_mode):
                    findings.append(Finding(relative_name(root, child), 0, "symlink"))
                elif stat.S_ISREG(child_mode) and child not in seen:
                    seen.add(child)
                    files.append(child)

    files.sort(key=lambda path: relative_name(root, path))
    findings.sort()
    if len(files) > MAX_FILES:
        raise ScanError(f"scan contains {len(files)} files, limit is {MAX_FILES}")
    return files, findings


def plausible_literal(match: re.Match[str]) -> bool:
    value = match.group("quoted") or match.group("bare") or ""
    if value.upper() in PLACEHOLDER_VALUES:
        return False
    if value.startswith(("$", "<")):
        return False
    if match.group("bare"):
        has_letter = any(character.isalpha() for character in value)
        has_other = any(not character.isalpha() for character in value)
        return has_letter and has_other
    return True


def line_findings(path: str, line_number: int, line: str) -> list[Finding]:
    findings: list[Finding] = []
    categories: set[str] = set()

    for match in LOCAL_USER_PATH.finditer(line):
        if match.group(1) != "example":
            categories.add("local_user_path")
    if PRIVATE_KEY.search(line):
        categories.add("private_key")
    if OPENAI_CREDENTIAL.search(line):
        categories.add("openai_credential")
    if GITHUB_CREDENTIAL.search(line):
        categories.add("github_credential")
    if any(plausible_literal(match) for match in LITERAL_SECRET_ASSIGNMENT.finditer(line)):
        categories.add("literal_secret_assignment")

    for category in sorted(categories):
        findings.append(Finding(path, line_number, category))
    return findings


def scan(root: Path, selected_paths: Iterable[str]) -> list[Finding]:
    files, findings = selected_files(root, selected_paths)
    total_bytes = 0

    for path in files:
        relative = relative_name(root, path)
        size = path.stat().st_size
        if size > MAX_FILE_BYTES:
            raise ScanError(
                f"scan input {relative} exceeds per-file byte limit {MAX_FILE_BYTES}"
            )
        total_bytes += size
        if total_bytes > MAX_TOTAL_BYTES:
            raise ScanError(f"scan exceeds total byte limit {MAX_TOTAL_BYTES}")

        body = path.read_bytes()
        if b"\x00" in body:
            continue
        try:
            text = body.decode("utf-8")
        except UnicodeDecodeError as error:
            raise ScanError(f"scan input is not UTF-8 text: {relative}") from error
        for line_number, line in enumerate(text.splitlines(), start=1):
            findings.extend(line_findings(relative, line_number, line))

    return sorted(set(findings))


def main() -> int:
    args = parse_args()
    root = Path(args.root).resolve()
    if not root.is_dir():
        print("public safety scan error: root is not a directory", file=sys.stderr)
        return 2
    try:
        findings = scan(root, args.paths)
    except (OSError, ScanError) as error:
        print(f"public safety scan error: {error}", file=sys.stderr)
        return 2

    for finding in findings:
        print(
            f"public safety finding: {finding.category} "
            f"{finding.path}:{finding.line}",
            file=sys.stderr,
        )
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
