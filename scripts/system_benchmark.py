#!/usr/bin/env python3 -u
"""
Full system benchmark: LLM generates plan → check_plan refines → score vs gold.

This tests the SYSTEM, not just the graph. The LLM reads the task
description, generates a plan, check_plan adds signals, the LLM revises,
and we score the final plan against the gold patch.

This is the A/B test:
  A: LLM generates plan WITHOUT plancheck
  B: LLM generates plan, check_plan provides signals, LLM revises

Both scored against the gold patch's actual file list.

Requires: PLANCHECK_API_KEY or ANTHROPIC_API_KEY

Usage:
    python3 system_benchmark.py [--limit N]
"""

import json
import os
import re
import subprocess
import sys
import tempfile
import time

import requests


PROJECT_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


# Auto-source .env from project root if API key not already set
def _load_dotenv():
    env_path = os.path.join(PROJECT_ROOT, '.env')
    if os.path.exists(env_path):
        with open(env_path) as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith('#') and '=' in line:
                    key, _, val = line.partition('=')
                    if key.strip() not in os.environ:
                        os.environ[key.strip()] = val.strip()

_load_dotenv()

DEFAULT_MODEL = os.environ.get('PLANCHECK_BENCH_MODEL', 'claude-sonnet-4-6')

# A-condition cache: keyed on instance_id + model.
# Eliminates A-condition variance AND saves ~50% of LLM cost.
# Use --no-cache to force fresh A-condition calls.
CACHE_DIR = os.path.expanduser('~/.plancheck/datasets/bench-cache')

def _cache_key(instance_id, model):
    import hashlib
    return hashlib.sha256(f"{instance_id}:{model}".encode()).hexdigest()[:16]

def cache_a_result(instance_id, model, response_text):
    os.makedirs(CACHE_DIR, exist_ok=True)
    key = _cache_key(instance_id, model)
    with open(os.path.join(CACHE_DIR, f"{key}.json"), 'w') as f:
        json.dump({"instance_id": instance_id, "model": model, "response": response_text}, f)

def load_a_cache(instance_id, model):
    key = _cache_key(instance_id, model)
    path = os.path.join(CACHE_DIR, f"{key}.json")
    if os.path.exists(path):
        try:
            with open(path) as f:
                data = json.load(f)
            if data.get("response"):
                return data["response"]
        except:
            pass
    return None

def call_claude(prompt, api_key, model=None):
    """Call Claude API and return the response text. Retries on transient failures."""
    if model is None:
        model = DEFAULT_MODEL
    for attempt in range(3):
        try:
            resp = requests.post(
                "https://api.anthropic.com/v1/messages",
                headers={
                    "x-api-key": api_key,
                    "anthropic-version": "2023-06-01",
                    "content-type": "application/json",
                },
                json={
                    "model": model,
                    "max_tokens": 2000,
                    "messages": [{"role": "user", "content": prompt}],
                },
                timeout=90,
            )
            if resp.status_code == 529 or resp.status_code >= 500:
                time.sleep(2 ** attempt)
                continue
            if resp.status_code != 200:
                return None
        except (requests.ConnectionError, requests.Timeout):
            time.sleep(2 ** attempt)
            continue
        break
    else:
        return None
    data = resp.json()
    if data.get("content"):
        return data["content"][0]["text"]
    return None


def run_suggest(files_touched, cwd, objective=""):
    """Run plancheck suggest and return parsed result text."""
    import json as json_mod
    files_json = json_mod.dumps(files_touched)
    mcp_request = json_mod.dumps({
        "jsonrpc": "2.0", "id": 1, "method": "tools/call",
        "params": {"name": "suggest", "arguments": {
            "files_touched": files_json,
            "cwd": os.path.abspath(cwd),
            "objective": objective,
        }}
    })
    try:
        result = subprocess.run(
            ['./plancheck', 'mcp'],
            input=mcp_request + '\n', capture_output=True, text=True, timeout=60,
            cwd=PROJECT_ROOT,
            env={**os.environ, 'PATH': '/usr/local/go/bin:' + os.environ.get('PATH', '')})
        for line in result.stdout.split('\n'):
            line = line.strip()
            if not line:
                continue
            try:
                d = json_mod.loads(line)
                if 'result' in d:
                    content = d['result'].get('content', [])
                    for c in content:
                        if c.get('type') == 'text':
                            return c['text']
            except:
                pass
    except:
        pass
    return ""


def run_check_plan(plan_json, cwd):
    """Run plancheck check and return both text output and parsed JSON result."""
    with tempfile.NamedTemporaryFile(mode='w', suffix='.json', delete=False) as f:
        f.write(plan_json)
        tmp = f.name
    try:
        result = subprocess.run(
            ['./plancheck', 'check', tmp, '--cwd', cwd],
            capture_output=True, text=True, timeout=120,
            cwd=PROJECT_ROOT,
            env={**os.environ, 'PATH': '/usr/local/go/bin:' + os.environ.get('PATH', '')})
        text_output = result.stdout

        # Find the correct project directory using SHA256 hash of cwd
        import hashlib
        abs_cwd = os.path.abspath(cwd)
        proj_hash = hashlib.sha256(abs_cwd.encode()).hexdigest()[:16]
        result_file = os.path.expanduser(f'~/.plancheck/projects/{proj_hash}/last-check-result.json')

        json_result = None
        if os.path.exists(result_file):
            try:
                with open(result_file) as rf:
                    json_result = json.load(rf)
            except:
                pass

        return text_output, json_result
    except:
        return "", None
    finally:
        os.unlink(tmp)


def compact_dir_tree(cwd, max_lines=25):
    """Build a compact directory tree showing Go package structure."""
    go_dirs = set()
    for root, dirs, files in os.walk(cwd):
        # Skip hidden dirs, vendor, testdata
        dirs[:] = [d for d in dirs if not d.startswith('.') and d not in ('vendor', 'testdata', 'node_modules')]
        for f in files:
            if f.endswith('.go') and not f.endswith('_test.go'):
                rel = os.path.relpath(root, cwd)
                if rel != '.':
                    go_dirs.add(rel.replace(os.sep, '/'))

    if not go_dirs:
        return ""

    # Group by top-level directory
    groups = {}
    for d in sorted(go_dirs):
        parts = d.split('/', 1)
        top = parts[0]
        sub = parts[1] if len(parts) > 1 else ""
        groups.setdefault(top, []).append(sub)

    lines = []
    for top in sorted(groups):
        subs = sorted(groups[top])
        if subs == [""]:
            lines.append(f"{top}/")
            continue

        # Compress: group by parent
        parents = {}
        for s in subs:
            if not s:
                continue
            parts = s.rsplit('/', 1)
            if len(parts) == 1:
                parents.setdefault("", []).append(parts[0])
            else:
                parents.setdefault(parts[0], []).append(parts[1])

        chunks = []
        for parent in sorted(parents):
            leaves = ','.join(sorted(parents[parent]))
            if parent:
                chunks.append(f"{parent}/{leaves}")
            else:
                chunks.append(leaves)

        # Build lines with wrapping
        current = f"{top}/"
        for chunk in chunks:
            if len(current) + len(chunk) + 2 > 80 and current != f"{top}/":
                lines.append(current)
                current = f"{top}/"
            current += f" {chunk}" if current == f"{top}/" else f"  {chunk}"
        if current != f"{top}/":
            lines.append(current)

    return '\n'.join(lines[:max_lines])


def extract_plan_files(text):
    """Extract file paths from LLM-generated plan text."""
    files = set()
    # Match Go file paths
    for m in re.finditer(r'[\w/]+\.go', text):
        f = m.group(0)
        if not f.startswith('//') and 'test' not in f.lower():
            files.add(f)
    return files


def score_plan(plan_files, gold_files):
    """Score a plan's file list against gold patch files.

    Uses path-aware matching: tries full path match first, falls back to
    basename only when the gold file has no directory prefix.
    Returns (recall, precision, f1, hit_details).
    hit_details is a dict: gold_file → matched_plan_file or None.
    """
    if not gold_files:
        return 0, 0, 0, {}

    # Normalize paths
    plan_paths = {f.strip('/') for f in plan_files}
    gold_paths = [f.strip('/') for f in gold_files]

    # Build plan index: full path → file, basename → [files]
    plan_by_path = {p: p for p in plan_paths}
    plan_by_base = {}
    for p in plan_paths:
        base = os.path.basename(p)
        plan_by_base.setdefault(base, []).append(p)

    hits = 0
    hit_details = {}
    for gf in gold_paths:
        gold_base = os.path.basename(gf)
        # 1. Exact path suffix match (handles "pkg/cmd/..." vs full paths)
        matched = None
        for pp in plan_paths:
            if pp == gf or pp.endswith('/' + gf) or gf.endswith('/' + pp):
                matched = pp
                break
        # 2. Basename match (unambiguous only — 1 plan file with that name)
        if not matched and gold_base in plan_by_base:
            candidates = plan_by_base[gold_base]
            if len(candidates) == 1:
                matched = candidates[0]
        if matched:
            hits += 1
            hit_details[gf] = matched
        else:
            hit_details[gf] = None

    recall = hits / len(gold_paths)
    precision = hits / len(plan_paths) if plan_paths else 0
    f1 = 2 * precision * recall / (precision + recall) if (precision + recall) > 0 else 0

    return recall, precision, f1, hit_details


def score_plancheck_precision(check_json, gold_files):
    """Measure plancheck's own precision: of the files it suggests, what % are gold?

    This separates plancheck signal quality from LLM adoption behavior.
    Returns (gold_in_suggestions, total_suggestions, precision).
    """
    if not check_json or not gold_files:
        return 0, 0, 0.0

    ranked = check_json.get('suggestedAdditions', {}).get('ranked', [])
    if not ranked:
        return 0, 0, 0.0

    gold_bases = {os.path.basename(gf) for gf in gold_files}
    gold_paths = {gf.strip('/') for gf in gold_files}

    gold_count = 0
    total = len(ranked)
    for r in ranked:
        p = (r.get('path') or r.get('file', '')).strip('/')
        base = os.path.basename(p)
        if p in gold_paths or base in gold_bases:
            gold_count += 1

    precision = gold_count / total if total > 0 else 0
    return gold_count, total, precision


def score_signal_quality(check_json, gold_files, plan_files_a):
    """Measure plancheck signal quality independent of LLM adoption.

    Of gold files the LLM missed in condition A, how many did plancheck
    suggest in its ranked list? This isolates signal quality from LLM compliance.
    Returns (suggested_of_missing, total_missing, suggestion_recall).
    """
    if not check_json or not gold_files:
        return 0, 0, 0.0

    # Find gold files the LLM missed
    _, _, _, hit_details = score_plan(plan_files_a, gold_files)
    missing_gold = [gf for gf, matched in hit_details.items() if matched is None]
    if not missing_gold:
        return 0, 0, 1.0  # nothing to suggest

    # Check plancheck suggestions
    ranked = check_json.get('suggestedAdditions', {}).get('ranked', [])
    suggested_paths = set()
    for r in ranked:
        p = r.get('path') or r.get('file', '')
        suggested_paths.add(p.strip('/'))
        suggested_paths.add(os.path.basename(p))

    suggested_count = 0
    for gf in missing_gold:
        gf_base = os.path.basename(gf)
        gf_norm = gf.strip('/')
        if gf_norm in suggested_paths or gf_base in suggested_paths:
            suggested_count += 1

    suggestion_recall = suggested_count / len(missing_gold) if missing_gold else 0
    return suggested_count, len(missing_gold), suggestion_recall


def main():
    api_key = os.environ.get('PLANCHECK_API_KEY') or os.environ.get('ANTHROPIC_API_KEY')
    if not api_key:
        print("Set PLANCHECK_API_KEY or ANTHROPIC_API_KEY")
        sys.exit(1)

    limit = 10
    if '--limit' in sys.argv:
        limit = int(sys.argv[sys.argv.index('--limit') + 1])

    # Dataset and cwd selection
    repo = 'cli'
    if '--repo' in sys.argv:
        repo = sys.argv[sys.argv.index('--repo') + 1]

    # Legacy dataset map (multi-swe-bench per-repo JSONL files)
    dataset_map = {
        'cli': ('cli__cli_dataset.jsonl', 'cli'),
        'grpc-go': ('grpc__grpc-go_dataset.jsonl', 'grpc-go'),
        'go-zero': ('zeromicro__go-zero_dataset.jsonl', 'go-zero'),
    }

    # Rebench-V2 support: --repo org/repo loads from the unified rebench-V2 file
    # e.g., --repo nats-io/nats-server, --repo mgechev/revive, --repo helm/helm
    use_rebench = '/' in repo

    if use_rebench:
        org, repo_name = repo.split('/', 1)
        rebench_path = os.path.expanduser('~/.plancheck/datasets/multi-swe-bench/go/swe-rebench-v2_go.jsonl')
        if not os.path.exists(rebench_path):
            print(f"Rebench-V2 dataset not found: {rebench_path}")
            sys.exit(1)
        tasks = []
        with open(rebench_path) as f:
            for line in f:
                t = json.loads(line)
                if t.get('org') == org and t.get('repo') == repo_name:
                    tasks.append(t)
        if not tasks:
            print(f"No tasks found for {repo} in rebench-V2 dataset")
            sys.exit(1)
        repo_dir = repo_name
        repo_display = f"{org}/{repo_name}"
    else:
        if repo not in dataset_map:
            print(f"Unknown repo: {repo}. Options: {', '.join(dataset_map)}")
            print(f"Or use org/repo format for rebench-V2: --repo nats-io/nats-server")
            sys.exit(1)
        dataset_file, repo_dir = dataset_map[repo]
        mswe_path = os.path.expanduser(f'~/.plancheck/datasets/multi-swe-bench/go/{dataset_file}')
        tasks = [json.loads(line) for line in open(mswe_path)]
        repo_display = "github.com/cli/cli" if repo == 'cli' else repo

    # Filter to multi-file Go tasks with descriptions
    multi_file = []
    for t in tasks:
        patch = t.get('fix_patch', '')
        files = [f for f in re.findall(r'diff --git a/(.*?) b/', patch)
                 if f.endswith('.go') and '_test.go' not in f]
        body = t.get('body', '') or ''
        title = t.get('title', '') or ''
        hints = t.get('hints', '') or ''
        # Rebench-V2 tasks often have no title — use body or hints
        description = title if title.strip() else (hints if hints.strip() else body)
        if len(files) >= 2 and len(description) > 10:
            multi_file.append({
                'title': description[:200],
                'body': body[:500],
                'gold_files': files,
                'instance_id': t.get('instance_id', ''),
                'test_patch': t.get('test_patch', ''),
            })

    use_cache = '--no-cache' not in sys.argv
    print(f'System benchmark: {min(limit, len(multi_file))} multi-file tasks ({repo_display})')
    print(f'A: LLM generates plan WITHOUT plancheck' + (' (cached)' if use_cache else ' (fresh)'))
    print(f'B: LLM generates plan WITH plancheck signals')
    print('=' * 70)

    a_recalls, b_recalls = [], []
    a_precisions, b_precisions = [], []
    a_f1s, b_f1s = [], []
    signal_recalls = []
    signal_suggested = 0
    signal_total_missing = 0
    plancheck_precisions = []
    plancheck_gold_total = 0
    plancheck_suggestions_total = 0
    cwd = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_dir}')

    for task in multi_file[:limit]:
        title = task['title'][:50]

        # ── Condition A: LLM plans WITHOUT plancheck ──
        # Cached by instance_id + model to eliminate variance and save cost.
        instance_id = task.get('instance_id', title)
        cached_a = load_a_cache(instance_id, DEFAULT_MODEL) if '--no-cache' not in sys.argv else None
        if cached_a:
            response_a = cached_a
        else:
            prompt_a = (
                f"You are planning a code change for the Go project {repo_display}.\n\n"
                f"Task: {task['title']}\n\n"
                f"Description: {task['body'][:300]}\n\n"
                f"List ONLY the Go source files (not test files) that need to be modified or created. "
                f"One file path per line, nothing else."
            )
            response_a = call_claude(prompt_a, api_key)
            if not response_a:
                continue
            cache_a_result(instance_id, DEFAULT_MODEL, response_a)

        plan_files_a = extract_plan_files(response_a)
        recall_a, prec_a, f1_a, _ = score_plan(plan_files_a, task['gold_files'])
        a_recalls.append(recall_a)
        a_precisions.append(prec_a)
        a_f1s.append(f1_a)

        # ── Condition B: LLM plans WITH plancheck ──
        if not plan_files_a:
            b_recalls.append(0)
            b_precisions.append(0)
            b_f1s.append(0)
            print(f'  ✗ {title:45s} A={recall_a:.2f} B=skip (no initial plan)')
            continue

        suggest_only = '--suggest-only' in sys.argv
        suggest_llm = '--suggest-llm' in sys.argv

        # SUGGEST+LLM MODE: run suggest() (free), then LLM revises with suggest output.
        # No spike — just structural signals + LLM judgment. Tests the sweet spot
        # between suggest-only ($0, +7.9pp) and full spike ($0.10, +17.7pp).
        if suggest_llm:
            suggest_text = run_suggest(sorted(plan_files_a)[:8], cwd, task['title'][:80])
            if suggest_text and suggest_text != "No additional files suggested. Your current file set looks complete.":
                prompt_b = f"""You are planning a code change for {repo_display} (Go project).

Task: {task['title']}
Description: {task['body'][:300]}

Your initial plan targets these files:
{chr(10).join('- ' + f for f in sorted(plan_files_a)[:8])}

Structural analysis of your plan found:

{suggest_text}

Based on the analysis above, revise your file list:
- KEEP all files from your initial plan
- ADD files marked MUST CHANGE — these will not compile without updates
- ADD files marked LIKELY NEEDED if they're relevant to this specific task
- Do NOT add files you're unsure about

List ALL Go source files (not test files) that need modification. One per line."""
                response_b = call_claude(prompt_b, api_key)
                if response_b:
                    plan_files_b = extract_plan_files(response_b)
                else:
                    plan_files_b = plan_files_a
            else:
                plan_files_b = plan_files_a

            recall_b, prec_b, f1_b, _ = score_plan(plan_files_b, task['gold_files'])
            b_recalls.append(recall_b)
            b_precisions.append(prec_b)
            b_f1s.append(f1_b)

            delta = recall_b - recall_a
            direction = '↑' if delta > 0 else ('↓' if delta < 0 else '=')
            icon_a = '✓' if recall_a > 0 else '✗'
            icon_b = '✓' if recall_b > 0 else '✗'
            sq_tag = f' S={len(suggest_text.split(chr(10)))}' if suggest_text else ''
            print(f'  {icon_a}→{icon_b} {title:40s} R={recall_a:.2f}→{recall_b:.2f} F1={f1_a:.2f}→{f1_b:.2f} {direction}{abs(delta):.2f}{sq_tag}', flush=True)
            time.sleep(0.5)
            continue

        # SUGGEST-ONLY MODE: skip the spike, just run suggest() on the A-condition files.
        # Tests whether structural + compiler signals alone are enough.
        if suggest_only:
            suggest_text = run_suggest(sorted(plan_files_a)[:8], cwd, task['title'][:80])
            # Extract file paths from suggest output
            suggest_files = set()
            for line in suggest_text.split('\n'):
                line = line.strip()
                # Line format: "path/to/file.go — reason"
                if ' — ' in line:
                    f = line.split(' — ')[0].strip()
                    if f.endswith('.go') and not f.endswith('_test.go'):
                        suggest_files.add(f)

            plan_files_b = plan_files_a | suggest_files
            recall_b, prec_b, f1_b, _ = score_plan(plan_files_b, task['gold_files'])
            b_recalls.append(recall_b)
            b_precisions.append(prec_b)
            b_f1s.append(f1_b)

            delta = recall_b - recall_a
            direction = '↑' if delta > 0 else ('↓' if delta < 0 else '=')
            icon_a = '✓' if recall_a > 0 else '✗'
            icon_b = '✓' if recall_b > 0 else '✗'
            print(f'  {icon_a}→{icon_b} {title:40s} R={recall_a:.2f}→{recall_b:.2f} F1={f1_a:.2f}→{f1_b:.2f} {direction}{abs(delta):.2f} S={len(suggest_files)}', flush=True)
            continue

        # Build COMPLETE plan with semantic suggestions + invariants
        # This triggers the full pipeline (analogies, backward scout, etc.)
        sorted_files = sorted(plan_files_a)[:8]
        plan_obj = {
            'objective': task['title'][:80],
            'filesToModify': sorted_files,
            'filesToCreate': [],
            'steps': [f'Fix: {task["title"][:60]}', task['body'][:300]] if task['body'] else [f'Fix: {task["title"][:60]}'],
            'invariants': [{'claim': 'existing tests pass', 'kind': 'tests'}],
            'semanticSuggestions': [
                {'file': f, 'confidence': 0.5, 'reason': 'initial plan candidate'}
                for f in sorted_files[:3]
            ],
        }
        # Add test patch for backward planning if available
        test_patch = task.get('test_patch', '')
        if test_patch:
            plan_obj['testPatch'] = test_patch[:3000]  # cap for prompt budget
        plan_json = json.dumps(plan_obj)

        # Run check_plan — get both text and structured JSON
        check_text, check_json = run_check_plan(plan_json, cwd)

        # Build structured analysis from JSON result
        # Order matters: critical errors first (Lost in the Middle effect),
        # then ranked suggestions, then informational signals.
        analysis_parts = []

        if check_json:
            # CRITICAL: Domain gaps FIRST — these are errors, not suggestions.
            # The task explicitly mentions domains the plan doesn't cover.
            kw_gaps = [sig['message'] for sig in check_json.get('signals', []) if sig.get('probe') == 'keyword-dir']
            if kw_gaps:
                gap_lines = []
                for g in kw_gaps:
                    gap_lines.append(f"  ✗ {g}")
                analysis_parts.append(
                    "INCOMPLETE PLAN — your plan is missing entire domains the task requires:\n"
                    + "\n".join(gap_lines)
                    + "\nYou MUST add files from each missing domain. This is not optional."
                )

            # Ranked suggestions (the most actionable signal)
            ranked = check_json.get('suggestedAdditions', {}).get('ranked', [])
            if ranked:
                ranked_lines = []
                for r in ranked[:7]:
                    # Use full path when available for unambiguous file identification
                    name = r.get('path') or r['file']
                    ranked_lines.append(f"  {name} — {r.get('reason', r['source'])}")
                analysis_parts.append("FILES YOU PROBABLY FORGOT (ranked by evidence):\n" + "\n".join(ranked_lines))

            # Simulation blast radius
            sim = check_json.get('simulation')
            if sim:
                analysis_parts.append(f"BLAST RADIUS: {sim.get('productionCallers', 0)} production callers, "
                    f"{sim.get('testCoverage', 0)} tests affected. "
                    f"High-impact definitions: {', '.join(sim.get('highImpactDefs', []))}")

            # Novelty assessment
            nov = check_json.get('novelty')
            if nov:
                analysis_parts.append(f"NOVELTY: {nov.get('label', '?')} ({nov.get('uncertainty', '?')} uncertainty). {nov.get('guidance', '')}")

            # Forecast
            fc = check_json.get('forecast')
            if fc:
                analysis_parts.append(f"FORECAST: {fc.get('pClean', 0)*100:.0f}% clean, "
                    f"{fc.get('pFailed', 0)*100:.0f}% risk of significant rework "
                    f"(based on {fc.get('basedOn', 0)} similar plans)")

        # Semantic validation: confirm/deny the model's own suggestions
        if check_json:
            for sig in check_json.get('signals', []):
                if sig.get('probe') == 'semantic-validation':
                    analysis_parts.append(f"YOUR SUGGESTION VALIDATED: {sig['message']}")

            # Invariant verification
            for sig in check_json.get('signals', []):
                if sig.get('probe') in ('invariant', 'invariant-risk'):
                    analysis_parts.append(f"INVARIANT: {sig['message']}")

            # Analogies (for novel work)
            analogies = [sig['message'] for sig in check_json.get('signals', []) if sig.get('probe') == 'analogy']
            if analogies:
                analysis_parts.append("CROSS-PROJECT PATTERNS:\n" + "\n".join(f"  {a}" for a in analogies[:3]))

            # Backward scout
            for sig in check_json.get('signals', []):
                if sig.get('probe') == 'backward-scout':
                    analysis_parts.append(f"PREREQUISITES: {sig['message']}")

        # Also include top comod findings
        top_findings = []
        for line in check_text.split('\n'):
            if 'co-changes with' in line.lower() or 'co-mod' in line.lower():
                top_findings.append(line.strip())
        if top_findings:
            analysis_parts.append("CO-MODIFICATION PATTERNS:\n" + "\n".join(top_findings[:5]))

        analysis = "\n\n".join(analysis_parts) if analysis_parts else check_text[:800]

        # Build a decision-framework prompt, not a data dump
        # Check if we got a judge recommendation (from the saved debug log)
        judge_rec = None
        debug_path = os.path.join(cwd, '.plancheck-debug.json')
        # The judge runs inside the MCP handler, but we're using CLI.
        # For the benchmark, run the judge separately if API key available.
        judge_text = ""
        api_key_env = os.environ.get('PLANCHECK_API_KEY', '')
        if api_key_env and check_json:
            # Build compact directory tree for semantic scope detection
            dir_tree = compact_dir_tree(cwd)

            # Build judge input from our signals + codebase structure
            judge_prompt = f"You are a plan verification judge for {repo_display}.\n\n"
            judge_prompt += f"PLAN: {task['title']}\n"
            judge_prompt += f"FILES: {', '.join(sorted(plan_files_a)[:8])}\n\n"
            if dir_tree:
                judge_prompt += f"CODEBASE STRUCTURE:\n{dir_tree}\n\n"
            judge_prompt += f"RAW SIGNALS:\n{analysis}\n\n"
            judge_prompt += "Your job: identify SPECIFIC FILES the plan is missing, with a task-specific reason for each.\n\n"
            judge_prompt += "Rules:\n"
            judge_prompt += "- Use the codebase structure to find directories the task mentions but the plan doesn't cover\n"
            judge_prompt += "- If the task mentions multiple commands/domains (e.g., 'Issues and PRs'), ALL domains must be in the plan\n"
            judge_prompt += "- For each missing file, explain WHY this task requires it (not generic — tied to the task)\n"
            judge_prompt += "- Don't list files already in the plan\n"
            judge_prompt += "- Combine structural signals with your understanding of the task\n\n"
            judge_prompt += 'Respond with JSON: {"filesToAdd": [{"file": "path/to/file.go", "reason": "task-specific reason"}], "recommendation": "one paragraph"}'

            judge_response = call_claude(judge_prompt, api_key_env, model="claude-haiku-4-5-20251001")
            if judge_response:
                # Parse structured response
                judge_files = []
                judge_rec_text = judge_response
                try:
                    jr = judge_response
                    if '{' in jr:
                        jr = jr[jr.index('{'):jr.rindex('}')+1]
                    parsed = json.loads(jr)
                    judge_rec_text = parsed.get('recommendation', judge_response)
                    judge_files = parsed.get('filesToAdd', [])
                except:
                    pass

                # Format judge file suggestions with task-specific reasons
                if judge_files:
                    file_lines = []
                    for jf in judge_files[:5]:
                        if isinstance(jf, dict):
                            file_lines.append(f"  ADD {jf.get('file', '?')} — {jf.get('reason', 'recommended by analysis')}")
                        elif isinstance(jf, str):
                            file_lines.append(f"  ADD {jf}")
                    judge_rec_text += "\n\nSPECIFIC FILES TO ADD:\n" + "\n".join(file_lines)

                judge_text = f"\n\nEXPERT JUDGE ASSESSMENT:\n{judge_rec_text}"

        # Build implementation preview from check_plan data.
        # Instead of a file checklist, present what the implementation looks like
        # and what could go wrong — let the LLM reason about what to include.
        preview_text = ""
        suggestion_files = []  # still needed for signal quality metrics
        seen_paths = set()

        if check_json:
            preview = check_json.get('preview')
            ranked = check_json.get('suggestedAdditions', {}).get('ranked', [])

            # Build confidence-tiered output.
            # Only explicitly name files when we have high confidence.
            # The qualitative preview covers everything else.
            if preview:
                changes = preview.get('fileChanges', [])
                obligations = preview.get('obligations', [])
                risks = preview.get('risks', [])

                if changes:
                    preview_text += "PROTOTYPE CHANGES:\n"
                    for ch in changes[:8]:
                        preview_text += f"  {ch['file']}: {ch['summary']}\n"

                # MUST ADD: compiler-verified obligations
                if obligations:
                    preview_text += "\nMUST ADD (compiler-verified — will not compile without these):\n"
                    for ob in obligations[:5]:
                        preview_text += f"  {ob['file']}: {ob['reason']}\n"
                        if ob['file'] not in seen_paths:
                            seen_paths.add(ob['file'])
                            suggestion_files.append((ob['file'], ob['reason']))

                # LIKELY: intersection files (spike + strong structural), max 2
                likely_count = 0
                likely_lines = []
                if ranked:
                    for r in ranked[:5]:
                        source = r.get('source', '')
                        # Only intersection files (contain both spike and structural source)
                        has_spike = 'spike' in source
                        has_structural = any(s in source for s in ['verified', 'structural', 'import-chain', 'cascade', 'comod'])
                        if has_spike and has_structural and likely_count < 2:
                            path = r.get('path') or r['file']
                            reason = r.get('reason', '')
                            if path not in seen_paths:
                                seen_paths.add(path)
                                suggestion_files.append((path, reason))
                                likely_lines.append(f"  {path} — {reason}" if reason else f"  {path}")
                                likely_count += 1
                if likely_lines:
                    preview_text += "\nLIKELY NEEDED:\n" + "\n".join(likely_lines) + "\n"

                # Spike-only files: NOT listed. The prototype description covers them.
                # This prevents over-inclusion from generic file lists.

                if risks:
                    preview_text += "\nRISKS:\n"
                    for r in risks[:5]:
                        preview_text += f"  {r}\n"

            elif not preview and ranked:
                # No preview but we have ranked files — low confidence
                preview_text += "No prototype available. Your plan may be complete.\n"
                # Still track for metrics but don't present to LLM
                for r in ranked[:3]:
                    path = r.get('path') or r['file']
                    reason = r.get('reason', '')
                    if path not in seen_paths:
                        seen_paths.add(path)
                        suggestion_files.append((path, reason))

        # Background analysis (contextual)
        background_parts = []
        for part in analysis_parts:
            if not part.startswith("INCOMPLETE PLAN") and not part.startswith("FILES YOU PROBABLY FORGOT"):
                background_parts.append(part)
            elif part.startswith("INCOMPLETE PLAN"):
                background_parts.append(part)
        background = "\n\n".join(background_parts) if background_parts else ""

        prompt_b = f"""You are planning a code change for {repo_display} (Go project).

Task: {task['title']}
Description: {task['body'][:300]}

Your initial plan targets these files:
{chr(10).join('- ' + f for f in sorted(plan_files_a)[:8])}

A senior engineer prototyped this change and found:

{preview_text if preview_text else '(no implementation preview available)'}

{background}

Based on the prototype results above, revise your file list:
- KEEP all files from your initial plan — do not remove any
- ADD files from the prototype that your plan missed, especially any that WILL BREAK
- The prototype may touch files beyond the minimal implementation — only add files you believe are necessary for this specific task

List ALL Go source files (not test files) that need modification. One per line."""

        response_b = call_claude(prompt_b, api_key)
        if not response_b:
            b_recalls.append(recall_a)  # fallback to A
            b_precisions.append(prec_a)
            b_f1s.append(f1_a)
            continue

        plan_files_b = extract_plan_files(response_b)
        recall_b, prec_b, f1_b, _ = score_plan(plan_files_b, task['gold_files'])
        b_recalls.append(recall_b)
        b_precisions.append(prec_b)
        b_f1s.append(f1_b)

        # Signal quality: of gold files LLM missed, how many did plancheck suggest?
        sq_suggested, sq_missing, sq_recall = score_signal_quality(
            check_json, task['gold_files'], plan_files_a)
        if sq_missing > 0:
            signal_recalls.append(sq_recall)
            signal_suggested += sq_suggested
            signal_total_missing += sq_missing

        # Plancheck precision: of files plancheck suggests, what % are gold?
        pp_gold, pp_total, pp_prec = score_plancheck_precision(
            check_json, task['gold_files'])
        if pp_total > 0:
            plancheck_precisions.append(pp_prec)
            plancheck_gold_total += pp_gold
            plancheck_suggestions_total += pp_total

        icon_a = '✓' if recall_a > 0 else '✗'
        icon_b = '✓' if recall_b > 0 else '✗'
        delta = recall_b - recall_a
        direction = '↑' if delta > 0 else ('↓' if delta < 0 else '=')

        # Show F1 alongside recall for granularity
        sq_tag = ''
        if sq_missing > 0:
            sq_tag = f' S={sq_suggested}/{sq_missing}'
        print(f'  {icon_a}→{icon_b} {title:40s} R={recall_a:.2f}→{recall_b:.2f} F1={f1_a:.2f}→{f1_b:.2f} {direction}{abs(delta):.2f}{sq_tag}', flush=True)

        time.sleep(0.5)  # rate limit

    if a_recalls and b_recalls:
        print()
        print('=' * 70)
        n = len(a_recalls)
        avg_a_r = sum(a_recalls) / n
        avg_b_r = sum(b_recalls) / n
        avg_a_p = sum(a_precisions) / n
        avg_b_p = sum(b_precisions) / n
        avg_a_f1 = sum(a_f1s) / n
        avg_b_f1 = sum(b_f1s) / n
        hit_a = sum(1 for r in a_recalls if r > 0) / n
        hit_b = sum(1 for r in b_recalls if r > 0) / n
        improved = sum(1 for a, b in zip(a_recalls, b_recalls) if b > a)
        worsened = sum(1 for a, b in zip(a_recalls, b_recalls) if b < a)

        print(f'RESULTS ({n} tasks)')
        print(f'  {"Metric":<25s} {"Without plancheck":>20s} {"With plancheck":>20s}')
        print(f'  {"-"*25} {"-"*20} {"-"*20}')
        print(f'  {"Recall":<25s} {avg_a_r:>20.3f} {avg_b_r:>20.3f}')
        print(f'  {"Precision":<25s} {avg_a_p:>20.3f} {avg_b_p:>20.3f}')
        print(f'  {"F1":<25s} {avg_a_f1:>20.3f} {avg_b_f1:>20.3f}')
        print(f'  {"Hit rate":<25s} {hit_a:>19.0%} {hit_b:>19.0%}')
        print()
        print(f'  Improved: {improved}/{n} tasks  (recall ↑)')
        print(f'  Worsened: {worsened}/{n} tasks  (recall ↓)')
        print(f'  Recall lift: {avg_b_r - avg_a_r:+.3f}')
        print(f'  F1 lift:     {avg_b_f1 - avg_a_f1:+.3f}')

        # Signal quality: how well does plancheck find what the LLM misses?
        if signal_total_missing > 0:
            avg_sq = sum(signal_recalls) / len(signal_recalls) if signal_recalls else 0
            print()
            print(f'  SIGNAL QUALITY (of gold files LLM missed, how many did plancheck suggest?)')
            print(f'    Suggestion recall: {signal_suggested}/{signal_total_missing} missing files ({signal_suggested/signal_total_missing:.0%})')
            print(f'    Avg task suggestion recall: {avg_sq:.0%}')

        # Plancheck precision: of files plancheck suggests, what % are gold?
        if plancheck_suggestions_total > 0:
            avg_pp = sum(plancheck_precisions) / len(plancheck_precisions) if plancheck_precisions else 0
            print()
            print(f'  PLANCHECK PRECISION (of files plancheck suggests, how many are gold?)')
            print(f'    Gold in suggestions: {plancheck_gold_total}/{plancheck_suggestions_total} ({plancheck_gold_total/plancheck_suggestions_total:.0%})')
            print(f'    Avg task precision: {avg_pp:.0%}')


if __name__ == '__main__':
    main()
