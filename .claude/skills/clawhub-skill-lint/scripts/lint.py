#!/usr/bin/env python3
"""
clawhub-skill-lint — pre-publish validator for ClawHub skills.

Re-implements the rules from openclaw/clawhub's server-side static scanner
(convex/lib/moderationEngine.ts) and the skill format spec (docs/skill-format.md)
so you can catch publish-time failures locally without needing the clawhub CLI
or a network round-trip.

Usage:
    lint.py [--json] [--strict] <path>...

Where <path> is a single skill folder (containing SKILL.md) or a parent
directory containing skill subfolders.

Exit codes:
  0   clean (no critical findings; warnings allowed unless --strict)
  1   at least one critical finding (or warning + --strict)
  2   usage / I/O error

Source-of-truth references:
  https://github.com/openclaw/clawhub/blob/main/docs/skill-format.md
  https://github.com/openclaw/clawhub/blob/main/convex/lib/moderationEngine.ts
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field, asdict
from pathlib import Path
from typing import Iterable

# ============================================================================
# Constants from docs/skill-format.md
# ============================================================================

SLUG_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")
SEMVER_RE = re.compile(r"^\d+\.\d+\.\d+(?:-[\w.-]+)?(?:\+[\w.-]+)?$")
ALLOWED_INSTALL_KINDS = {"brew", "node", "go", "uv"}
ALLOWED_OS = {"macos", "linux", "windows"}
BUNDLE_BYTE_LIMIT = 50 * 1024 * 1024  # 50 MB

# Text-file extensions that ClawHub accepts. Subset; extend if needed.
TEXT_FILE_EXTS = {
    ".md", ".markdown", ".mdx", ".txt",
    ".json", ".json5", ".yaml", ".yml", ".toml",
    ".js", ".ts", ".mjs", ".cjs", ".mts", ".cts", ".jsx", ".tsx",
    ".py", ".sh", ".bash", ".zsh", ".rb", ".go",
    ".svg", ".html", ".css", ".clawhubignore", ".gitignore",
}

# ============================================================================
# Static scanner rules from convex/lib/moderationEngine.ts
# ============================================================================

MARKDOWN_EXT_RE = re.compile(r"\.(md|markdown|mdx)$", re.I)
CODE_EXT_RE = re.compile(r"\.(js|ts|mjs|cjs|mts|cts|jsx|tsx|py|sh|bash|zsh|rb|go)$", re.I)
MANIFEST_EXT_RE = re.compile(r"\.(json|yaml|yml|toml)$", re.I)

RAW_IP_URL_RE = re.compile(r"https?://\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?(?:/|[\"'])", re.I)
# Match both quoted and unquoted YAML/JSON forms:
#   installer-package: https://example.com/x
#   installer-package: "https://example.com/x"
#   installer-package: 'https://example.com/x'
INSTALL_PACKAGE_RE = re.compile(
    r"installer-package\s*:\s*[\"']?https?://[^\s\"'`]+[\"']?",
    re.I,
)
URL_SHORTENER_RE = re.compile(
    r"https?://(bit\.ly|tinyurl\.com|t\.co|goo\.gl|is\.gd)/", re.I
)

# hasMaliciousInstallPrompt preconditions
TI_PATTERNS = [
    re.compile(r"(?:copy|paste).{0,80}(?:command|snippet).{0,120}(?:terminal|shell)", re.I | re.S),
    re.compile(r"run\s+it\s+in\s+terminal", re.I),
    re.compile(r"open\s+terminal", re.I),
    re.compile(r"for\s+macos\s*:", re.I),
]
CURL_PIPE_SH_RE = re.compile(r"(?:curl|wget)\b[^\n|]{0,240}\|\s*(?:/bin/)?(?:ba)?sh\b", re.I)
BASE64_EXEC_RE = re.compile(
    r"(?:echo|printf)\s+[\"'][A-Za-z0-9+/=\s]{40,}[\"']\s*\|\s*base64\s+-?[dD]\b"
    r"[^\n|]{0,120}\|\s*(?:/bin/)?(?:ba)?sh\b",
    re.I,
)

# Other markdown rules
PROMPT_INJECTION_RE = re.compile(
    r"ignore\s+(all\s+)?previous\s+instructions|system\s*prompt\s*[:=]", re.I
)
GEN_SOURCE_PLACEHOLDER_RE = re.compile(
    r"^\s*[A-Za-z_][A-Za-z0-9_]*\s*=.*[\"']\$\{[A-Za-z_][A-Za-z0-9_-]*\}[\"']", re.M
)
GEN_SOURCE_CONTEXT_RE = re.compile(
    r"```(?:python|py|javascript|js|typescript|ts|shell|bash|sh)\b"
    r"|cat\s*(?:>|>>)?\s*[^`\n]*\.(?:py|js|ts|sh)\b"
    r"|python3?\b|node\b",
    re.I,
)
HARDCODED_CONNECTION_ID_RE = re.compile(
    r"[\"']connection_id[\"']\s*:\s*[\"'][0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}"
    r"-[89ab][0-9a-f]{3}-[0-9a-f]{12}[\"']",
    re.I,
)

# Code file rules (only fire on .js/.ts/.py/.sh/.rb/.go etc.)
CHILD_PROCESS_RE = re.compile(r"child_process")
EXEC_RE = re.compile(r"\b(exec|execSync|spawn|spawnSync|execFile|execFileSync)\s*\(")
EVAL_RE = re.compile(r"\beval\s*\(|new\s+Function\s*\(")
CRYPTO_MINING_RE = re.compile(r"stratum\+tcp|stratum\+ssl|coinhive|cryptonight|xmrig", re.I)
NETWORK_SEND_RE = re.compile(r"\bfetch\b|http\.request|\baxios\b")
PROCESS_ENV_RE = re.compile(r"process\.env")
FILE_READ_RE = re.compile(r"readFileSync|readFile")
OBFUSCATED_HEX_RE = re.compile(r"(\\x[0-9a-fA-F]{2}){6,}")
OBFUSCATED_B64_RE = re.compile(r"(?:atob|Buffer\.from)\s*\(\s*[\"'][A-Za-z0-9+/=]{200,}[\"']")

# ============================================================================
# Findings model
# ============================================================================


@dataclass
class Finding:
    rule_id: str
    severity: str  # "critical" | "warn" | "info"
    file: str
    line: int
    message: str
    evidence: str = ""

    def __post_init__(self):
        if len(self.evidence) > 120:
            self.evidence = self.evidence[:120] + "…"


@dataclass
class SkillReport:
    slug: str
    path: str
    findings: list[Finding] = field(default_factory=list)
    bundle_bytes: int = 0
    file_count: int = 0

    def add(self, f: Finding):
        self.findings.append(f)

    def by_severity(self, sev: str) -> list[Finding]:
        return [f for f in self.findings if f.severity == sev]

    def is_clean(self, strict: bool) -> bool:
        if any(f.severity == "critical" for f in self.findings):
            return False
        if strict and any(f.severity == "warn" for f in self.findings):
            return False
        return True


# ============================================================================
# Frontmatter parser (regex-based, no PyYAML dep)
# ============================================================================


def parse_frontmatter(text: str) -> dict | None:
    """Extract and parse the YAML frontmatter from a SKILL.md file.

    Accepts LF or CRLF line endings and tolerates a missing trailing
    newline after the closing `---`.
    """
    m = re.match(r"^---\r?\n(.*?)\r?\n---(?:\r?\n|$)", text, re.DOTALL)
    if not m:
        return None
    return _parse_yaml_subset(m.group(1))


def _parse_yaml_subset(text: str) -> dict:
    """Recursive-descent parser for the YAML subset SKILL.md frontmatter uses:
    - scalar keys (quoted or bare)
    - nested maps (block style, indented)
    - lists of scalars and lists of maps (`- key: val` form)
    - flow-style lists `[a, b, c]`
    - folded scalars `key: >` followed by indented continuation lines
    """
    raw_lines = text.split("\n")
    # Drop blank/comment lines but remember nothing — recursive descent handles indent.
    lines = [ln for ln in raw_lines if ln.strip() and not ln.lstrip().startswith("#")]
    pos = [0]

    def peek_indent() -> int | None:
        if pos[0] >= len(lines):
            return None
        ln = lines[pos[0]]
        return len(ln) - len(ln.lstrip(" "))

    def parse_scalar_value(val: str, parent_indent: int):
        """Convert an inline value (everything after `key:`) to a Python value."""
        val = val.strip()
        if val == ">" or val == ">-":
            return parse_folded(parent_indent)
        if val.startswith("[") and val.endswith("]"):
            inner = val[1:-1]
            return [_strip_quotes(x.strip()) for x in inner.split(",") if x.strip()]
        return _strip_quotes(val)

    def parse_value_after_key(parent_indent: int):
        """Parse the value that follows a `key:` with no inline content.
        The value lives on subsequent lines at indent > parent_indent."""
        ni = peek_indent()
        if ni is None or ni <= parent_indent:
            return None
        stripped = lines[pos[0]].strip()
        if stripped.startswith("- "):
            return parse_list(ni)
        return parse_map(ni)

    def parse_map(min_indent: int) -> dict:
        result: dict = {}
        while pos[0] < len(lines):
            ln = lines[pos[0]]
            indent = len(ln) - len(ln.lstrip(" "))
            stripped = ln.strip()
            if indent < min_indent:
                return result
            if stripped.startswith("- "):
                return result
            if indent != min_indent:
                pos[0] += 1
                continue
            if ":" not in stripped:
                pos[0] += 1
                continue
            key, _, val = stripped.partition(":")
            key = _strip_quotes(key.strip())
            val = val.strip()
            pos[0] += 1
            if not val:
                result[key] = parse_value_after_key(indent)
            else:
                result[key] = parse_scalar_value(val, indent)
        return result

    def parse_list(min_indent: int) -> list:
        result: list = []
        while pos[0] < len(lines):
            ln = lines[pos[0]]
            indent = len(ln) - len(ln.lstrip(" "))
            stripped = ln.strip()
            if indent < min_indent:
                return result
            if not stripped.startswith("- "):
                return result
            if indent != min_indent:
                return result
            item_content = stripped[2:].strip()
            pos[0] += 1
            if ":" in item_content and not item_content.startswith("["):
                # List item is a map. Its inner keys live at min_indent + 2
                # (the column where item content starts after `- `).
                inner_indent = min_indent + 2
                item: dict = {}
                key, _, val = item_content.partition(":")
                key = _strip_quotes(key.strip())
                val = val.strip()
                if not val:
                    item[key] = parse_value_after_key(inner_indent)
                else:
                    item[key] = parse_scalar_value(val, inner_indent)
                # Parse any additional keys for this same item at inner_indent.
                additional = parse_map(inner_indent)
                item.update(additional)
                result.append(item)
            else:
                result.append(_strip_quotes(item_content))
        return result

    def parse_folded(parent_indent: int) -> str:
        fold_lines: list[str] = []
        while pos[0] < len(lines):
            ln = lines[pos[0]]
            indent = len(ln) - len(ln.lstrip(" "))
            if indent <= parent_indent:
                break
            fold_lines.append(ln.strip())
            pos[0] += 1
        return " ".join(fold_lines).strip()

    return parse_map(0)


def _strip_quotes(s: str) -> str:
    if len(s) >= 2 and (
        (s[0] == '"' and s[-1] == '"') or (s[0] == "'" and s[-1] == "'")
    ):
        return s[1:-1]
    return s


# ============================================================================
# Skill discovery
# ============================================================================


def find_skills(path: Path) -> list[Path]:
    """Discover skill folders. A path containing SKILL.md is a single skill;
    otherwise scan child directories for SKILL.md."""
    if (path / "SKILL.md").is_file() or (path / "skill.md").is_file():
        return [path]
    if not path.is_dir():
        return []
    out: list[Path] = []
    for child in sorted(path.iterdir()):
        if child.is_dir() and not child.name.startswith("."):
            if (child / "SKILL.md").is_file() or (child / "skill.md").is_file():
                out.append(child)
    return out


def read_clawhubignore(skill_dir: Path) -> set[str]:
    """Return the set of basename patterns to ignore from .clawhubignore."""
    f = skill_dir / ".clawhubignore"
    if not f.is_file():
        return set()
    return {ln.strip() for ln in f.read_text().splitlines() if ln.strip() and not ln.startswith("#")}


SKILL_MD_NAMES = {"SKILL.md", "skill.md"}


def iter_skill_files(skill_dir: Path) -> Iterable[Path]:
    """Yield every file in the skill folder that would be included in the bundle.

    SKILL.md is always yielded — even if a maintainer adds it to .clawhubignore
    by mistake — because the publish would fail server-side without it. The
    linter separately reports the bad ignore pattern via check_clawhubignore().
    """
    ignore = read_clawhubignore(skill_dir)
    for p in skill_dir.rglob("*"):
        if not p.is_file():
            continue
        rel = p.relative_to(skill_dir)
        if any(part.startswith(".") and part not in (".clawhubignore", ".gitignore") for part in rel.parts):
            continue
        # SKILL.md at the root is always included regardless of .clawhubignore.
        if str(rel) in SKILL_MD_NAMES:
            yield p
            continue
        if rel.name in ignore:
            continue
        yield p


def check_clawhubignore(report: SkillReport, skill_dir: Path) -> None:
    """Report if .clawhubignore tries to exclude SKILL.md (which the publisher
    would silently override but is still a maintenance bug worth flagging)."""
    f = skill_dir / ".clawhubignore"
    if not f.is_file():
        return
    for ln_no, raw in enumerate(f.read_text().splitlines(), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line in SKILL_MD_NAMES:
            report.add(Finding(
                "bundle.skill_md_in_ignore", "critical",
                ".clawhubignore", ln_no,
                f"`.clawhubignore` lists `{line}`, which would exclude the required "
                "SKILL.md from the published bundle. Remove this entry — SKILL.md "
                "must always ship.",
                evidence=raw,
            ))


# ============================================================================
# Frontmatter checks
# ============================================================================


def check_frontmatter(report: SkillReport, skill_dir: Path) -> dict | None:
    skill_md = skill_dir / "SKILL.md"
    if not skill_md.is_file():
        skill_md = skill_dir / "skill.md"
    if not skill_md.is_file():
        report.add(Finding(
            "frontmatter.missing_skill_md", "critical",
            "SKILL.md", 1, "Skill folder has no SKILL.md (or skill.md) file.",
        ))
        return None

    text = skill_md.read_text()
    fm = parse_frontmatter(text)
    if fm is None:
        report.add(Finding(
            "frontmatter.no_frontmatter", "critical",
            "SKILL.md", 1, "SKILL.md has no YAML frontmatter (--- block at top of file).",
        ))
        return None

    # Required fields
    for field_name in ("name", "description"):
        if field_name not in fm or not fm[field_name]:
            report.add(Finding(
                "frontmatter.missing_required_field", "critical",
                "SKILL.md", 1,
                f"Required frontmatter field `{field_name}` is missing or empty.",
            ))

    # Version (optional in spec but required for `clawhub skill publish --version`,
    # and we want it for clarity)
    if "version" in fm and fm["version"]:
        if not SEMVER_RE.match(str(fm["version"])):
            report.add(Finding(
                "frontmatter.invalid_version", "critical",
                "SKILL.md", 1,
                f"`version` is not valid semver: {fm['version']!r}",
                evidence=f"version: {fm['version']}",
            ))
    else:
        report.add(Finding(
            "frontmatter.missing_version", "warn",
            "SKILL.md", 1,
            "No `version` field in frontmatter. Required for explicit `clawhub skill publish`; "
            "`clawhub sync --bump` can derive it from registry state.",
        ))

    # Slug == folder name
    folder_slug = skill_dir.name
    if not SLUG_RE.match(folder_slug):
        report.add(Finding(
            "frontmatter.invalid_slug", "critical",
            "SKILL.md", 1,
            f"Folder name `{folder_slug}` is not a valid ClawHub slug "
            f"(must match {SLUG_RE.pattern}).",
        ))
    if "name" in fm and fm["name"] != folder_slug:
        report.add(Finding(
            "frontmatter.name_slug_mismatch", "warn",
            "SKILL.md", 1,
            f"Frontmatter `name: {fm['name']}` does not match folder name "
            f"`{folder_slug}`. The folder name is used as the slug by default.",
        ))

    # metadata.openclaw checks. Validate the shape of every nested level
    # before calling .get() — a malformed manifest should produce a finding,
    # not an AttributeError that aborts the linter.
    metadata = fm.get("metadata")
    if metadata is None:
        oc: dict = {}
    elif not isinstance(metadata, dict):
        report.add(Finding(
            "frontmatter.invalid_metadata", "critical",
            "SKILL.md", 1,
            f"`metadata` must be a mapping, got {type(metadata).__name__}.",
        ))
        oc = {}
    else:
        openclaw = metadata.get("openclaw")
        if openclaw is None:
            oc = {}
        elif not isinstance(openclaw, dict):
            report.add(Finding(
                "frontmatter.invalid_openclaw", "critical",
                "SKILL.md", 1,
                f"`metadata.openclaw` must be a mapping, got {type(openclaw).__name__}.",
            ))
            oc = {}
        else:
            oc = openclaw

    # os
    os_field = oc.get("os")
    if os_field is not None:
        if not isinstance(os_field, list):
            report.add(Finding(
                "frontmatter.invalid_os", "critical",
                "SKILL.md", 1, f"`metadata.openclaw.os` must be a list, got {type(os_field).__name__}.",
            ))
        else:
            for entry in os_field:
                if entry not in ALLOWED_OS:
                    report.add(Finding(
                        "frontmatter.unknown_os", "warn",
                        "SKILL.md", 1,
                        f"Unknown OS `{entry}` in `metadata.openclaw.os`. "
                        f"Allowed: {sorted(ALLOWED_OS)}.",
                    ))

    # install kinds
    install = oc.get("install")
    if install is not None:
        if not isinstance(install, list):
            report.add(Finding(
                "frontmatter.invalid_install", "critical",
                "SKILL.md", 1,
                f"`metadata.openclaw.install` must be a list, got {type(install).__name__}.",
            ))
        else:
            for entry in install:
                if isinstance(entry, dict) and "kind" in entry:
                    if entry["kind"] not in ALLOWED_INSTALL_KINDS:
                        report.add(Finding(
                            "frontmatter.invalid_install_kind", "critical",
                            "SKILL.md", 1,
                            f"install kind `{entry['kind']}` is not in the allowed set "
                            f"{sorted(ALLOWED_INSTALL_KINDS)}. The skill format spec only "
                            f"supports these kinds; everything else is ignored.",
                        ))

    # primaryEnv must be in requires.env if both are set
    primary = oc.get("primaryEnv")
    requires = oc.get("requires")
    if requires is not None and not isinstance(requires, dict):
        report.add(Finding(
            "frontmatter.invalid_requires", "critical",
            "SKILL.md", 1,
            f"`metadata.openclaw.requires` must be a mapping, got {type(requires).__name__}.",
        ))
        requires_env: list = []
    elif requires is None:
        requires_env = []
    else:
        re_env = requires.get("env")
        if re_env is None:
            requires_env = []
        elif not isinstance(re_env, list):
            report.add(Finding(
                "frontmatter.invalid_requires_env", "critical",
                "SKILL.md", 1,
                f"`metadata.openclaw.requires.env` must be a list, got {type(re_env).__name__}.",
            ))
            requires_env = []
        else:
            requires_env = re_env
    if primary and requires_env and primary not in requires_env:
        report.add(Finding(
            "frontmatter.primary_env_not_in_requires", "warn",
            "SKILL.md", 1,
            f"`primaryEnv: {primary}` is not listed in `requires.env`. "
            f"Add it to keep the metadata internally consistent.",
        ))

    # always: true is privileged (scanner flags as warn)
    if oc.get("always") is True or oc.get("always") == "true":
        report.add(Finding(
            "frontmatter.privileged_always", "warn",
            "SKILL.md", 1,
            "`metadata.openclaw.always: true` makes the skill persistently invoked. "
            "ClawHub flags this as a privileged manifest.",
            evidence="always: true",
        ))

    return fm


# ============================================================================
# Bundle checks
# ============================================================================


def check_bundle(report: SkillReport, skill_dir: Path) -> None:
    total = 0
    count = 0
    for p in iter_skill_files(skill_dir):
        try:
            size = p.stat().st_size
        except OSError:
            continue
        total += size
        count += 1
        ext = p.suffix.lower()
        if ext not in TEXT_FILE_EXTS and p.name not in TEXT_FILE_EXTS:
            label = ext if ext else "(none)"
            report.add(Finding(
                "bundle.non_text_file", "warn",
                str(p.relative_to(skill_dir)), 1,
                f"File extension `{label}` is not in the ClawHub text-file allowlist. "
                f"Server may reject the bundle.",
            ))
    report.bundle_bytes = total
    report.file_count = count
    if total > BUNDLE_BYTE_LIMIT:
        report.add(Finding(
            "bundle.size_exceeded", "critical",
            "<bundle>", 1,
            f"Bundle size {total // 1024} KB exceeds 50 MB limit.",
        ))


# ============================================================================
# Static scanner — markdown rules
# ============================================================================


def has_terminal_instruction(content: str) -> bool:
    return any(p.search(content) for p in TI_PATTERNS)


def find_first_line(content: str, pattern: re.Pattern) -> tuple[int, str]:
    for i, line in enumerate(content.split("\n"), start=1):
        if pattern.search(line):
            return i, line
    m = pattern.search(content)
    if m:
        line_no = content[: m.start()].count("\n") + 1
        line_text = content.split("\n")[line_no - 1]
        return line_no, line_text
    return 1, content[:120]


def scan_markdown(report: SkillReport, rel_path: str, content: str) -> None:
    # hasMaliciousInstallPrompt
    if has_terminal_instruction(content):
        has_curl = bool(CURL_PIPE_SH_RE.search(content))
        has_b64 = bool(BASE64_EXEC_RE.search(content))
        has_rawip = bool(RAW_IP_URL_RE.search(content))
        has_instpkg = bool(INSTALL_PACKAGE_RE.search(content))
        if has_b64 or (has_curl and (has_rawip or has_instpkg)):
            line, evidence = find_first_line(
                content,
                re.compile(
                    r"installer-package\s*:|base64\s+-?[dD]|(?:curl|wget)\b|run\s+it\s+in\s+terminal",
                    re.I,
                ),
            )
            report.add(Finding(
                "static.malicious_install_prompt", "critical",
                rel_path, line,
                "Install prompt contains an obfuscated terminal payload "
                "(curl|sh + raw IP / installer-package, or base64-decoded pipe-to-sh).",
                evidence=evidence,
            ))

    # Prompt injection bait
    if PROMPT_INJECTION_RE.search(content):
        line, evidence = find_first_line(content, PROMPT_INJECTION_RE)
        report.add(Finding(
            "static.injection_instructions", "warn",
            rel_path, line,
            "Prompt-injection style instruction pattern detected ('ignore previous instructions' or 'system prompt:').",
            evidence=evidence,
        ))

    # Generated source placeholder + code context
    if GEN_SOURCE_PLACEHOLDER_RE.search(content) and GEN_SOURCE_CONTEXT_RE.search(content):
        line, evidence = find_first_line(content, GEN_SOURCE_PLACEHOLDER_RE)
        report.add(Finding(
            "static.generated_source_template", "critical",
            rel_path, line,
            "User-controlled placeholder is embedded directly into generated source code.",
            evidence=evidence,
        ))

    # Hardcoded connection_id UUID
    if HARDCODED_CONNECTION_ID_RE.search(content):
        line, evidence = find_first_line(content, HARDCODED_CONNECTION_ID_RE)
        report.add(Finding(
            "static.hardcoded_connection_id", "critical",
            rel_path, line,
            "Example code exposes a concrete connection_id UUID instead of a placeholder.",
            evidence=evidence,
        ))


# ============================================================================
# Static scanner — code file rules
# ============================================================================


def scan_code(report: SkillReport, rel_path: str, content: str) -> None:
    if CHILD_PROCESS_RE.search(content) and EXEC_RE.search(content):
        line, evidence = find_first_line(content, EXEC_RE)
        report.add(Finding(
            "static.dangerous_exec", "critical",
            rel_path, line,
            "Shell command execution detected (child_process exec/spawn).",
            evidence=evidence,
        ))

    if EVAL_RE.search(content):
        line, evidence = find_first_line(content, EVAL_RE)
        report.add(Finding(
            "static.dynamic_code_execution", "critical",
            rel_path, line,
            "Dynamic code execution detected (eval / new Function).",
            evidence=evidence,
        ))

    if CRYPTO_MINING_RE.search(content):
        line, evidence = find_first_line(content, CRYPTO_MINING_RE)
        report.add(Finding(
            "static.crypto_mining", "critical",
            rel_path, line,
            "Possible crypto mining behavior detected.",
            evidence=evidence,
        ))

    if FILE_READ_RE.search(content) and NETWORK_SEND_RE.search(content):
        line, evidence = find_first_line(content, FILE_READ_RE)
        report.add(Finding(
            "static.exfiltration", "warn",
            rel_path, line,
            "File read combined with network send (possible exfiltration).",
            evidence=evidence,
        ))

    if PROCESS_ENV_RE.search(content) and NETWORK_SEND_RE.search(content):
        line, evidence = find_first_line(content, PROCESS_ENV_RE)
        report.add(Finding(
            "static.credential_harvest", "critical",
            rel_path, line,
            "Environment variable access combined with network send.",
            evidence=evidence,
        ))

    if OBFUSCATED_HEX_RE.search(content) or OBFUSCATED_B64_RE.search(content):
        pat = OBFUSCATED_HEX_RE if OBFUSCATED_HEX_RE.search(content) else OBFUSCATED_B64_RE
        line, evidence = find_first_line(content, pat)
        report.add(Finding(
            "static.obfuscated_code", "warn",
            rel_path, line,
            "Potential obfuscated payload detected (hex or large base64).",
            evidence=evidence,
        ))


# ============================================================================
# Static scanner — manifest file rules
# ============================================================================


def scan_manifest(report: SkillReport, rel_path: str, content: str) -> None:
    if URL_SHORTENER_RE.search(content) or RAW_IP_URL_RE.search(content):
        pat = URL_SHORTENER_RE if URL_SHORTENER_RE.search(content) else RAW_IP_URL_RE
        line, evidence = find_first_line(content, pat)
        report.add(Finding(
            "static.suspicious_install_source", "warn",
            rel_path, line,
            "Install source points to URL shortener or raw IP.",
            evidence=evidence,
        ))


# ============================================================================
# Per-skill linter
# ============================================================================


def lint_skill(skill_dir: Path) -> SkillReport:
    report = SkillReport(slug=skill_dir.name, path=str(skill_dir))
    fm = check_frontmatter(report, skill_dir)
    check_bundle(report, skill_dir)
    check_clawhubignore(report, skill_dir)

    for p in iter_skill_files(skill_dir):
        rel = str(p.relative_to(skill_dir))
        try:
            content = p.read_text(errors="replace")
        except OSError:
            continue
        if MARKDOWN_EXT_RE.search(rel):
            scan_markdown(report, rel, content)
        if CODE_EXT_RE.search(rel):
            scan_code(report, rel, content)
        if MANIFEST_EXT_RE.search(rel):
            scan_manifest(report, rel, content)

    return report


# ============================================================================
# Output
# ============================================================================

ANSI_RED = "\033[31m"
ANSI_YELLOW = "\033[33m"
ANSI_GREEN = "\033[32m"
ANSI_DIM = "\033[2m"
ANSI_BOLD = "\033[1m"
ANSI_RESET = "\033[0m"

SEVERITY_COLOR = {
    "critical": ANSI_RED,
    "warn": ANSI_YELLOW,
    "info": ANSI_DIM,
}


def color(s: str, c: str, use_color: bool) -> str:
    if not use_color:
        return s
    return f"{c}{s}{ANSI_RESET}"


def print_text_report(reports: list[SkillReport], strict: bool, use_color: bool) -> None:
    total_critical = 0
    total_warn = 0
    for report in reports:
        critical = report.by_severity("critical")
        warns = report.by_severity("warn")
        infos = report.by_severity("info")
        total_critical += len(critical)
        total_warn += len(warns)

        clean = report.is_clean(strict)
        banner_color = ANSI_GREEN if clean else (ANSI_RED if critical else ANSI_YELLOW)
        status = "PASS" if clean else ("FAIL" if critical else "WARN")
        print()
        print(color(f"=== {status} {report.slug}", banner_color + ANSI_BOLD, use_color))
        print(color(f"    {report.path}", ANSI_DIM, use_color))
        print(color(
            f"    {report.file_count} files, {report.bundle_bytes // 1024} KB / 50 MB limit",
            ANSI_DIM, use_color,
        ))
        if not report.findings:
            print(color("    no findings", ANSI_DIM, use_color))
            continue
        for sev in ("critical", "warn", "info"):
            group = report.by_severity(sev)
            if not group:
                continue
            print(color(f"    [{sev}] {len(group)} finding(s)", SEVERITY_COLOR[sev], use_color))
            for f in group:
                print(f"      {f.rule_id}  {f.file}:{f.line}")
                print(f"        {f.message}")
                if f.evidence:
                    print(color(f"        evidence: {f.evidence}", ANSI_DIM, use_color))

    print()
    summary = (
        f"{len(reports)} skill(s) scanned, "
        f"{total_critical} critical, {total_warn} warning(s)"
    )
    if total_critical:
        print(color(f"FAIL — {summary}", ANSI_RED + ANSI_BOLD, use_color))
    elif total_warn and strict:
        print(color(f"FAIL (strict) — {summary}", ANSI_YELLOW + ANSI_BOLD, use_color))
    elif total_warn:
        print(color(f"PASS with warnings — {summary}", ANSI_YELLOW, use_color))
    else:
        print(color(f"PASS — {summary}", ANSI_GREEN + ANSI_BOLD, use_color))


def print_json_report(reports: list[SkillReport]) -> None:
    out = []
    for r in reports:
        out.append({
            "slug": r.slug,
            "path": r.path,
            "file_count": r.file_count,
            "bundle_bytes": r.bundle_bytes,
            "findings": [asdict(f) for f in r.findings],
        })
    json.dump(out, sys.stdout, indent=2)
    sys.stdout.write("\n")


# ============================================================================
# Main
# ============================================================================


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        description="Pre-publish validator for ClawHub skills.",
    )
    parser.add_argument("paths", nargs="+", type=Path, help="Skill folders or parent directories")
    parser.add_argument("--json", action="store_true", help="Machine-readable JSON output")
    parser.add_argument("--strict", action="store_true", help="Treat warnings as errors")
    parser.add_argument("--no-color", action="store_true", help="Disable ANSI colors")
    args = parser.parse_args(argv)

    skills: list[Path] = []
    for p in args.paths:
        if not p.exists():
            print(f"ERROR: path does not exist: {p}", file=sys.stderr)
            return 2
        found = find_skills(p)
        if not found:
            print(f"ERROR: no skill folders found under {p} (looked for SKILL.md / skill.md)",
                  file=sys.stderr)
            return 2
        skills.extend(found)

    reports = [lint_skill(s) for s in skills]
    use_color = sys.stdout.isatty() and not args.no_color and not args.json

    if args.json:
        print_json_report(reports)
    else:
        print_text_report(reports, args.strict, use_color)

    has_critical = any(r.by_severity("critical") for r in reports)
    has_warn = any(r.by_severity("warn") for r in reports)
    if has_critical:
        return 1
    if args.strict and has_warn:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
