#!/usr/bin/env python3
"""Fail when protected package coverage falls below its checked-in floor."""

from __future__ import annotations

import argparse
import fnmatch
import json
import math
import re
import subprocess
import sys
from dataclasses import dataclass
from datetime import date
from pathlib import Path


PROFILE_LINE = re.compile(
    r"^(?P<file>.+):(?P<start>\d+)\.(?P<start_col>\d+),"
    r"(?P<end>\d+)\.(?P<end_col>\d+)\s+"
    r"(?P<statements>\d+)\s+(?P<count>\d+)$"
)
DIFF_HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(?P<start>\d+)(?:,(?P<count>\d+))? @@")


@dataclass
class Coverage:
    statements: int = 0
    covered: int = 0

    @property
    def percent(self) -> float:
        if self.statements == 0:
            return 0.0
        return self.covered * 100.0 / self.statements


@dataclass(frozen=True)
class CoverageBlock:
    path: str
    start_line: int
    end_line: int
    statements: int
    covered: bool


@dataclass(frozen=True)
class ChangedLineException:
    path: str
    reason: str
    expires: date


def validate_percentage(name: str, value: object) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{name} must be a number between 0 and 100")
    result = float(value)
    if not math.isfinite(result) or result < 0 or result > 100:
        raise ValueError(f"{name} must be between 0 and 100")
    return result


def validate_config(config: object, *, require_changed_lines: bool = False) -> dict:
    if not isinstance(config, dict):
        raise ValueError("coverage config must be an object")
    module = config.get("module", "")
    if not isinstance(module, str):
        raise ValueError("module must be a string")

    packages = config.get("packages")
    if not isinstance(packages, list) or not packages:
        raise ValueError("packages must be a non-empty array")
    seen_paths: set[str] = set()
    for index, rule in enumerate(packages):
        label = f"packages[{index}]"
        if not isinstance(rule, dict):
            raise ValueError(f"{label} must be an object")
        path = rule.get("path")
        if not isinstance(path, str) or not path:
            raise ValueError(f"{label}.path must be a non-empty string")
        if path in seen_paths:
            raise ValueError(f"duplicate package path {path!r}")
        seen_paths.add(path)
        validate_percentage(f"{label}.minimum", rule.get("minimum"))
        if "recursive" in rule and not isinstance(rule["recursive"], bool):
            raise ValueError(f"{label}.recursive must be boolean")
        reason = rule.get("reason")
        if reason is not None and (not isinstance(reason, str) or not reason.strip()):
            raise ValueError(f"{label}.reason must be a non-empty string")

    changed = config.get("changed_line_coverage")
    if require_changed_lines and not isinstance(changed, dict):
        raise ValueError("changed_line_coverage must be an object")
    if isinstance(changed, dict):
        validate_percentage("changed_line_coverage.minimum", changed.get("minimum"))
        excluded = changed.get("excluded_paths", [])
        if not isinstance(excluded, list) or not all(
            isinstance(pattern, str) and pattern for pattern in excluded
        ):
            raise ValueError("changed_line_coverage.excluded_paths must be a string array")
        representative_go_paths = ("cmd/ox/main.go", "internal/example/file.go")
        if all(path_matches(path, excluded) for path in representative_go_paths):
            raise ValueError("changed_line_coverage.excluded_paths cannot exclude all files")
        exceptions = changed.get("exceptions", [])
        if not isinstance(exceptions, list):
            raise ValueError("changed_line_coverage.exceptions must be an array")
    return config


def load_blocks(
    path: Path, module: str
) -> dict[tuple[str, int, int, int, int], CoverageBlock]:
    blocks: dict[tuple[str, int, int, int, int], CoverageBlock] = {}
    with path.open(encoding="utf-8") as profile:
        header = profile.readline().strip()
        if header not in {"mode: set", "mode: count", "mode: atomic"}:
            raise ValueError(f"{path}: invalid Go coverage mode header {header!r}")
        for line_number, raw_line in enumerate(profile, start=2):
            line = raw_line.strip()
            if not line:
                continue
            match = PROFILE_LINE.match(line)
            if match is None:
                raise ValueError(f"{path}:{line_number}: malformed coverage block")
            file_path = match.group("file")
            if file_path.startswith(module):
                file_path = file_path[len(module) :]
            start_line = int(match.group("start"))
            end_line = int(match.group("end"))
            statements = int(match.group("statements"))
            count = int(match.group("count"))
            key = (
                file_path,
                start_line,
                int(match.group("start_col")),
                end_line,
                int(match.group("end_col")),
            )
            previous = blocks.get(key)
            if previous is not None and previous.statements != statements:
                raise ValueError(
                    f"{path}:{line_number}: duplicate block has conflicting statement counts"
                )
            blocks[key] = CoverageBlock(
                path=file_path,
                start_line=start_line,
                end_line=end_line,
                statements=statements,
                covered=count > 0 or bool(previous and previous.covered),
            )
    return blocks


def load_profile(path: Path, module: str) -> dict[str, Coverage]:
    blocks = load_blocks(path, module)

    files: dict[str, Coverage] = {}
    for block in blocks.values():
        coverage = files.setdefault(block.path, Coverage())
        coverage.statements += block.statements
        if block.covered:
            coverage.covered += block.statements
    return files


def package_coverage(
    files: dict[str, Coverage], prefix: str, recursive: bool = True
) -> Coverage:
    result = Coverage()
    for path, coverage in files.items():
        matches = path.startswith(prefix)
        if matches and not recursive:
            matches = "/" not in path[len(prefix) :]
        if matches:
            result.statements += coverage.statements
            result.covered += coverage.covered
    return result


def check(profile_path: Path, config_path: Path) -> list[str]:
    config = validate_config(json.loads(config_path.read_text(encoding="utf-8")))
    module = config.get("module", "")
    files = load_profile(profile_path, module)
    failures: list[str] = []

    print("Protected package coverage:")
    for rule in config["packages"]:
        prefix = rule["path"]
        minimum = float(rule["minimum"])
        coverage = package_coverage(files, prefix, rule.get("recursive", True))
        if coverage.statements == 0:
            failures.append(f"{prefix}: no statements matched the coverage profile")
            print(f"  MISSING {prefix}")
            continue
        status = "PASS" if coverage.percent + 1e-9 >= minimum else "FAIL"
        print(
            f"  {status} {prefix} {coverage.percent:.1f}% "
            f"(floor {minimum:.1f}%, {coverage.covered}/{coverage.statements} statements)"
        )
        if status == "FAIL":
            failures.append(
                f"{prefix}: {coverage.percent:.1f}% is below the {minimum:.1f}% floor"
            )
    return failures


def parse_changed_lines(diff: str) -> dict[str, set[int]]:
    """Return added line numbers per new-side path from a unified-zero diff."""
    changed: dict[str, set[int]] = {}
    current_path: str | None = None
    for line in diff.splitlines():
        if line.startswith("+++ "):
            raw_path = line[4:].split("\t", 1)[0]
            if raw_path == "/dev/null":
                current_path = None
            else:
                current_path = raw_path.removeprefix("b/")
                changed.setdefault(current_path, set())
            continue
        if current_path is None or not line.startswith("@@ "):
            continue
        match = DIFF_HUNK.match(line)
        if match is None:
            raise ValueError(f"malformed diff hunk: {line}")
        start = int(match.group("start"))
        count = int(match.group("count") or "1")
        changed[current_path].update(range(start, start + count))
    return changed


def path_matches(path: str, patterns: list[str]) -> bool:
    return any(fnmatch.fnmatch(path, pattern) for pattern in patterns)


def validate_exceptions(
    exceptions: list[dict], today: date
) -> list[ChangedLineException]:
    validated: list[ChangedLineException] = []
    for exception in exceptions:
        if not isinstance(exception, dict):
            raise ValueError("changed-line exceptions must be objects")
        pattern = exception.get("path", "")
        reason = exception.get("reason", "").strip()
        expires_raw = exception.get("expires", "")
        if not pattern or not reason or not expires_raw:
            raise ValueError(
                "changed-line exceptions require path, non-empty reason, and expires"
            )
        try:
            expires = date.fromisoformat(expires_raw)
        except ValueError as error:
            raise ValueError(
                f"changed-line exception {pattern!r} has invalid expires date"
            ) from error
        if expires < today:
            raise ValueError(
                f"changed-line exception {pattern!r} expired on {expires.isoformat()}"
            )
        validated.append(ChangedLineException(pattern, reason, expires))
    return validated


def active_exception(
    path: str, exceptions: list[ChangedLineException]
) -> str | None:
    for exception in exceptions:
        if fnmatch.fnmatch(path, exception.path):
            return (
                f"{exception.reason} (expires {exception.expires.isoformat()})"
            )
    return None


def evaluate_changed_lines(
    blocks: dict[tuple[str, int, int, int, int], CoverageBlock],
    changed: dict[str, set[int]],
    settings: dict,
    *,
    today: date | None = None,
) -> tuple[Coverage, list[str], list[str]]:
    """Evaluate executable statements overlapping changed production lines."""
    today = today or date.today()
    minimum = float(settings["minimum"])
    excluded = list(settings.get("excluded_paths", []))
    exceptions = validate_exceptions(list(settings.get("exceptions", [])), today)
    result = Coverage()
    failures: list[str] = []
    notices: list[str] = []

    for path, lines in sorted(changed.items()):
        if not lines or path.endswith("_test.go") or path_matches(path, excluded):
            continue
        exception = active_exception(path, exceptions)
        if exception is not None:
            notices.append(f"EXCEPT {path}: {exception}")
            continue
        file_blocks = [block for block in blocks.values() if block.path == path]
        if not file_blocks:
            failures.append(f"{path}: changed production file has no coverage data")
            continue
        overlapping = [
            block
            for block in file_blocks
            if any(block.start_line <= line <= block.end_line for line in lines)
        ]
        for block in overlapping:
            result.statements += block.statements
            if block.covered:
                result.covered += block.statements

    if result.statements > 0 and result.percent + 1e-9 < minimum:
        failures.append(
            f"changed executable statements: {result.percent:.1f}% is below "
            f"the {minimum:.1f}% floor"
        )
    return result, failures, notices


def git_changed_lines(base: str) -> dict[str, set[int]]:
    command = [
        "git",
        "diff",
        "--unified=0",
        "--no-color",
        base,
        "--",
        "*.go",
    ]
    completed = subprocess.run(command, check=True, text=True, capture_output=True)
    return parse_changed_lines(completed.stdout)


def check_changed_lines(profile_path: Path, config_path: Path, base: str) -> list[str]:
    config = validate_config(
        json.loads(config_path.read_text(encoding="utf-8")),
        require_changed_lines=True,
    )
    settings = config["changed_line_coverage"]
    blocks = load_blocks(profile_path, config.get("module", ""))
    changed = git_changed_lines(base)
    coverage, failures, notices = evaluate_changed_lines(blocks, changed, settings)

    print("Changed-line coverage:")
    for notice in notices:
        print(f"  {notice}")
    if coverage.statements == 0:
        print("  PASS no changed executable statements require coverage")
    else:
        minimum = float(settings["minimum"])
        status = "PASS" if coverage.percent + 1e-9 >= minimum else "FAIL"
        print(
            f"  {status} {coverage.percent:.1f}% "
            f"(floor {minimum:.1f}%, {coverage.covered}/{coverage.statements} statements)"
        )
    for failure in failures:
        if "no coverage data" in failure:
            print(f"  FAIL {failure}")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("profile", type=Path)
    parser.add_argument(
        "--config",
        type=Path,
        default=Path(".config/coverage-ratchets.json"),
    )
    parser.add_argument(
        "--diff-base",
        help="also enforce changed-line coverage against BASE and the working tree",
    )
    args = parser.parse_args()

    try:
        failures = check(args.profile, args.config)
        if args.diff_base:
            failures.extend(
                check_changed_lines(args.profile, args.config, args.diff_base)
            )
    except (
        OSError,
        ValueError,
        KeyError,
        TypeError,
        json.JSONDecodeError,
        subprocess.CalledProcessError,
    ) as error:
        print(f"coverage ratchet error: {error}", file=sys.stderr)
        return 2
    if failures:
        print("\nCoverage ratchet failed:", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
