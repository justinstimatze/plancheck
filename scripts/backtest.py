#!/usr/bin/env python3
"""
Backtest ripple simulation against SWE-smith-go gold patches.

For each task:
1. Parse the gold patch → identify changed files and functions
2. Pick the "seed" change (smallest/first changed definition)
3. Query the defn reference graph for blast radius of the seed
4. Compare predicted blast radius against actual changed definitions
5. Score: precision (predicted changes that were real) and recall (real changes that were predicted)

Usage:
    python3 backtest.py <repo-name> [--limit N]

Example:
    python3 backtest.py gin --limit 20
"""

import json
import os
import re
import subprocess
import sys
from pathlib import Path

import pandas as pd


def parse_patch_files(patch: str) -> list[dict]:
    """Extract changed files and function-level info from a unified diff."""
    changes = []
    current_file = None

    for line in patch.split('\n'):
        # Match diff header: diff --git a/file.go b/file.go
        m = re.match(r'^diff --git a/(.*?) b/(.*?)$', line)
        if m:
            current_file = m.group(2)
            continue

        # Match hunk header: @@ -67,7 +67,9 @@ func (w *responseWriter) WriteHeader(code int) {
        m = re.match(r'^@@ .+? @@\s*(.*)', line)
        if m and current_file:
            context = m.group(1).strip()
            func_name = extract_func_name(context)
            changes.append({
                'file': current_file,
                'func': func_name,
                'context': context,
            })

    return changes


def extract_func_name(context: str) -> str | None:
    """Extract Go function/method name from diff hunk context."""
    # func (w *responseWriter) WriteHeader(code int) {
    m = re.match(r'func\s+\((\w+)\s+(\*?\w+)\)\s+(\w+)', context)
    if m:
        receiver = m.group(2)
        name = m.group(3)
        return f'({receiver}).{name}'

    # func SomeFunction(args) {
    m = re.match(r'func\s+(\w+)', context)
    if m:
        return m.group(1)

    # type SomeType struct {
    m = re.match(r'type\s+(\w+)', context)
    if m:
        return m.group(1)

    return None


def query_defn(repo_path: str, sql: str) -> list[dict]:
    """Query a defn database via dolt CLI."""
    defn_dir = os.path.join(repo_path, '.defn')
    try:
        result = subprocess.run(
            ['dolt', 'sql', '-q', sql, '-r', 'json'],
            cwd=defn_dir,
            capture_output=True, text=True, timeout=10
        )
        if result.returncode != 0:
            return []
        data = json.loads(result.stdout)
        return data.get('rows', [])
    except (subprocess.TimeoutExpired, json.JSONDecodeError):
        return []


def find_definition_id(repo_path: str, name: str, receiver: str = '') -> int | None:
    """Find a definition ID by name and optional receiver."""
    if receiver:
        sql = f"SELECT id FROM definitions WHERE name = '{name}' AND receiver = '{receiver}' AND test = FALSE LIMIT 1"
    else:
        sql = f"SELECT id FROM definitions WHERE name = '{name}' AND receiver = '' AND test = FALSE LIMIT 1"

    rows = query_defn(repo_path, sql)
    return rows[0]['id'] if rows else None


def get_blast_radius(repo_path: str, def_id: int) -> set[str]:
    """Get all production definitions in the blast radius (depth 2)."""
    sql = f"""
    WITH direct AS (
        SELECT d.id, d.name, d.receiver
        FROM definitions d
        JOIN `references` r ON r.from_def = d.id
        WHERE r.to_def = {def_id} AND d.test = FALSE
    )
    SELECT DISTINCT d.name, d.receiver FROM (
        SELECT name, receiver FROM direct
        UNION
        SELECT d2.name, d2.receiver
        FROM definitions d2
        JOIN `references` r2 ON r2.from_def = d2.id
        JOIN direct ON r2.to_def = direct.id
        WHERE d2.test = FALSE AND d2.id NOT IN (SELECT id FROM direct)
    ) d
    """
    rows = query_defn(repo_path, sql)
    result = set()
    for r in rows:
        recv = r.get('receiver', '')
        name = r.get('name', '')
        if recv:
            result.add(f'({recv}).{name}')
        else:
            result.add(name)
    return result


def backtest_task(repo_path: str, patch: str) -> dict | None:
    """Run backtest for a single task. Returns scores or None if not applicable."""
    changes = parse_patch_files(patch)
    if not changes:
        return None

    # Get unique changed functions
    changed_funcs = set()
    for c in changes:
        if c['func']:
            changed_funcs.add(c['func'])

    if len(changed_funcs) < 2:
        return None  # Need at least 2 changed definitions to test prediction

    # Pick the first changed function as the "seed"
    seed_func = list(changed_funcs)[0]
    other_changes = changed_funcs - {seed_func}

    # Parse seed into name + receiver
    m = re.match(r'\((\*?\w+)\)\.(\w+)', seed_func)
    if m:
        receiver, name = m.group(1), m.group(2)
    else:
        receiver, name = '', seed_func

    # Find definition in defn
    def_id = find_definition_id(repo_path, name, receiver)
    if def_id is None:
        return None  # Definition not found in defn DB

    # Get blast radius
    predicted = get_blast_radius(repo_path, def_id)

    if not predicted:
        return {
            'seed': seed_func,
            'actual_changes': len(other_changes),
            'predicted': 0,
            'true_positives': 0,
            'precision': 0.0,
            'recall': 0.0,
        }

    # Score
    true_positives = predicted & other_changes
    precision = len(true_positives) / len(predicted) if predicted else 0
    recall = len(true_positives) / len(other_changes) if other_changes else 0

    return {
        'seed': seed_func,
        'actual_changes': len(other_changes),
        'predicted': len(predicted),
        'true_positives': len(true_positives),
        'precision': round(precision, 3),
        'recall': round(recall, 3),
        'hits': list(true_positives)[:5],
        'misses': list(other_changes - predicted)[:5],
    }


def main():
    repo_name = sys.argv[1] if len(sys.argv) > 1 else 'gin'
    limit = int(sys.argv[3]) if len(sys.argv) > 3 and sys.argv[2] == '--limit' else 50

    # Map repo name to SWE-smith format
    repo_map = {
        'gin': 'gin-gonic__gin',
        'caddy': 'caddyserver__caddy',
        'cobra': 'spf13__cobra',
        'echo': 'labstack__echo',
        'gorm': 'go-gorm__gorm',
        'bleve': 'blevesearch__bleve',
        'roaring': 'RoaringBitmap__roaring',
        'revive': 'mgechev__revive',
    }

    swe_pattern = repo_map.get(repo_name, repo_name)
    repo_path = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_name}')

    if not os.path.exists(os.path.join(repo_path, '.defn')):
        print(f'Error: {repo_path}/.defn not found. Run defn init first.')
        sys.exit(1)

    # Load tasks
    df = pd.read_parquet(os.path.expanduser(
        '~/.plancheck/datasets/swe-smith-go/data/train-00000-of-00001.parquet'))
    tasks = df[df['repo'].str.contains(swe_pattern)]

    print(f'Backtesting {repo_name}: {len(tasks)} tasks available, running {min(limit, len(tasks))}')
    print('=' * 60)

    results = []
    skipped = 0

    for i, (_, task) in enumerate(tasks.head(limit).iterrows()):
        result = backtest_task(repo_path, task['patch'])
        if result is None:
            skipped += 1
            continue
        results.append(result)

        # Print each result
        r = result
        status = '✓' if r['recall'] > 0 else '✗'
        print(f'  {status} seed={r["seed"]}: predicted={r["predicted"]}, '
              f'actual={r["actual_changes"]}, hits={r["true_positives"]}, '
              f'P={r["precision"]:.2f} R={r["recall"]:.2f}')

    if not results:
        print(f'\nNo scoreable tasks (skipped {skipped} — single-function patches)')
        return

    # Aggregate
    print()
    print('=' * 60)
    print(f'RESULTS: {len(results)} scored, {skipped} skipped (single-function patches)')

    avg_precision = sum(r['precision'] for r in results) / len(results)
    avg_recall = sum(r['recall'] for r in results) / len(results)
    hit_rate = sum(1 for r in results if r['true_positives'] > 0) / len(results)

    print(f'  Avg precision: {avg_precision:.3f}')
    print(f'  Avg recall:    {avg_recall:.3f}')
    print(f'  Hit rate:      {hit_rate:.1%} (tasks where ≥1 change was predicted)')
    print(f'  Avg predicted: {sum(r["predicted"] for r in results) / len(results):.1f}')
    print(f'  Avg actual:    {sum(r["actual_changes"] for r in results) / len(results):.1f}')


if __name__ == '__main__':
    main()
