#!/usr/bin/env python3
"""
Batch test runner for the sageox OpenClaw skill.

Sends test prompts to the OpenClaw instance via SSH + `openclaw agent --message`,
captures responses, and saves results as CSV.

Usage:
    python3 claws/openclaw/sageox/test-skill.py

Requires:
    - SSH access to VPS (~/.ssh/ox-bot.pem)
    - OpenClaw gateway running on VPS
"""

import csv
import os
import subprocess
import sys
import time
from datetime import datetime
from pathlib import Path

# Force unbuffered stdout so output streams in real-time
sys.stdout.reconfigure(line_buffering=True)
sys.stderr.reconfigure(line_buffering=True)

# --- Config ---
VPS_KEY = Path.home() / ".ssh" / "ox-bot.pem"
VPS_HOST = "ubuntu@35.164.224.156"
OUTPUT_DIR = Path("/tmp/sageox-test-results")
TIMEOUT_SECONDS = 300  # max wait per prompt (agent responses can be slow)

# --- Test cases ---
# (name, prompt, expected_behavior)
TEST_CASES = [
    (
        "skill_routing",
        "what can you do with sageox",
        "Should describe the 8 capabilities",
    ),
    (
        "query",
        "search my team context for recent architecture decisions",
        "Should load references/query.md and run ox query",
    ),
    (
        "coworkers_list",
        "list my coworkers",
        "Should run ox coworker list",
    ),
    (
        "coworkers_load",
        "load the go-pro coworker",
        "Should read coworker prompt and embody it",
    ),
    (
        "distill",
        "distill this repo with daily layer and sonnet model",
        "Should run ox distill --layer daily --model sonnet",
    ),
    (
        "glance",
        "what are my AI coworkers doing right now",
        "Should run ox glance and synthesize output",
    ),
    (
        "catchup",
        "catch me up on the last 24 hours",
        "Should orchestrate distill history + session list + code insights",
    ),
    (
        "summary",
        "generate a cross-team summary",
        "Should run summary pipeline with select-new-entries.sh",
    ),
    (
        "import",
        "how do I import a document into sageox",
        "Should load references/import-export.md and explain ox import",
    ),
    (
        "manifest_management",
        "show my sageox configured repos",
        "Should read and display sageox-distill-repos.json manifest",
    ),
]


def send_prompt(prompt: str, session_id: str) -> dict:
    """Send a prompt to OpenClaw via SSH and return the response."""
    ssh_cmd = [
        "ssh",
        "-i", str(VPS_KEY),
        "-o", "StrictHostKeyChecking=no",
        "-o", "ConnectTimeout=10",
        VPS_HOST,
        f'openclaw agent --session-id {session_id} --message {repr(prompt)} 2>&1',
    ]

    try:
        result = subprocess.run(
            ssh_cmd,
            capture_output=True,
            text=True,
            timeout=TIMEOUT_SECONDS,
        )

        output = result.stdout.strip()
        if result.returncode != 0:
            error = result.stderr.strip() or output
            return {"status": "error", "response": f"exit {result.returncode}: {error}"}

        return {"status": "ok", "response": output}

    except subprocess.TimeoutExpired:
        return {"status": "timeout", "response": f"Timed out after {TIMEOUT_SECONDS}s"}
    except Exception as e:
        return {"status": "error", "response": str(e)}


def run_tests():
    timestamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    csv_path = OUTPUT_DIR / f"test-results-{timestamp}.csv"

    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)

    # Verify SSH connectivity first
    print("Verifying SSH connection...")
    check = subprocess.run(
        ["ssh", "-i", str(VPS_KEY), "-o", "ConnectTimeout=5", VPS_HOST, "echo ok"],
        capture_output=True, text=True, timeout=15,
    )
    if check.stdout.strip() != "ok":
        print(f"SSH connection failed: {check.stderr}", file=sys.stderr)
        sys.exit(1)
    print("SSH connection verified.\n")

    # Each test gets its own session so they don't share context
    session_prefix = f"sageox-test-{timestamp}"

    results = []
    total = len(TEST_CASES)

    for i, (name, prompt, expected) in enumerate(TEST_CASES, 1):
        session_id = f"{session_prefix}-{name}"
        print(f"[{i}/{total}] {name}")
        print(f"  Prompt: {prompt}")
        print(f"  Expected: {expected}")
        print(f"  Session: {session_id}")

        start = time.time()
        resp = send_prompt(prompt, session_id)
        elapsed = round(time.time() - start, 1)

        status = resp["status"]
        response_text = resp["response"]

        # Truncate for display
        preview = response_text[:300].replace("\n", " ")
        print(f"  Status: {status} ({elapsed}s)")
        print(f"  Response: {preview}")
        print()

        results.append({
            "test_name": name,
            "prompt": prompt,
            "expected": expected,
            "status": status,
            "response_time_s": elapsed,
            "response": response_text,
            "timestamp": datetime.now().isoformat(),
        })

        # Delay between tests so sessions don't collide
        if i < total:
            time.sleep(5)

    # Write CSV
    with open(csv_path, "w", newline="") as f:
        writer = csv.DictWriter(
            f,
            fieldnames=[
                "test_name", "prompt", "expected", "status",
                "response_time_s", "response", "timestamp",
            ],
        )
        writer.writeheader()
        writer.writerows(results)

    # Print summary
    print("=" * 60)
    print(f"Results saved to: {csv_path}")
    print(f"Total tests: {total}")
    ok = sum(1 for r in results if r["status"] == "ok")
    errors = sum(1 for r in results if r["status"] == "error")
    timeouts = sum(1 for r in results if r["status"] == "timeout")
    print(f"OK: {ok}  Errors: {errors}  Timeouts: {timeouts}")
    print("=" * 60)


if __name__ == "__main__":
    run_tests()
