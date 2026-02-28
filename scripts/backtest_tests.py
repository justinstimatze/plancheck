#!/usr/bin/env python3
"""
Backtest: does the reference graph predict which tests fail?

For each SWE-smith-go task:
1. Parse gold patch → identify changed definition
2. Query defn reference graph for test coverage (transitive callers where test=TRUE)
3. Compare predicted test set against FAIL_TO_PASS ground truth
4. Score recall: what fraction of failing tests did the graph predict?

This is the right question for single-function patches. The reference graph
should predict which tests break when you modify a definition.

Usage:
    python3 backtest_tests.py <repo-name> [--limit N]
"""

import json
import os
import re
import subprocess
import sys

import pandas as pd


def extract_func_from_patch(patch: str) -> tuple[str, str] | None:
    """Extract the first changed function name and receiver from a patch."""
    for line in patch.split('\n'):
        m = re.match(r'^@@ .+? @@\s*(.*)', line)
        if not m:
            continue
        context = m.group(1).strip()

        # Method: func (w *responseWriter) WriteHeader(code int) {
        m2 = re.match(r'func\s+\(\w+\s+(\*?\w+)\)\s+(\w+)', context)
        if m2:
            return m2.group(2), m2.group(1)

        # Function: func SomeFunction(args) {
        m2 = re.match(r'func\s+(\w+)', context)
        if m2:
            return m2.group(1), ''

    return None


def parse_fail_to_pass(fail_str) -> set[str]:
    """Parse FAIL_TO_PASS into a set of test function names."""
    if isinstance(fail_str, str):
        # Format: "['TestFoo' 'TestBar']"
        return set(re.findall(r"'(Test\w+)'", fail_str))
    # numpy array
    tests = set()
    for item in fail_str:
        if isinstance(item, str) and item.startswith('Test'):
            # Strip subtest paths: TestFoo/subcase -> TestFoo
            base = item.split('/')[0]
            tests.add(base)
    return tests


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


def resolve_definition(repo_path: str, name: str, receiver: str) -> str | None:
    """Resolve a definition with fuzzy receiver matching.

    Handles cases where:
    - Diff says *Router but defn stores *DefaultRouter (type aliases/embeds)
    - Diff says *Echo but defn stores it as a standalone func (no receiver)
    - Diff says *node (unexported) which may be stored differently
    """
    # Try exact match first
    if receiver:
        where = f"name = '{name}' AND receiver = '{receiver}' AND test = FALSE"
    else:
        where = f"name = '{name}' AND receiver = '' AND test = FALSE"

    rows = query_defn(repo_path, f"SELECT id FROM definitions WHERE {where} LIMIT 1")
    if rows:
        return f"target.name = '{name}' AND target.receiver = '{receiver}'"

    # Try name-only match (ignore receiver)
    rows = query_defn(repo_path,
        f"SELECT id, receiver FROM definitions WHERE name = '{name}' AND test = FALSE")
    if len(rows) == 1:
        # Unambiguous name match
        actual_recv = rows[0]['receiver']
        return f"target.name = '{name}' AND target.receiver = '{actual_recv}'"
    elif len(rows) > 1 and receiver:
        # Multiple matches — try suffix match on receiver (e.g. *Router matches *DefaultRouter)
        stripped = receiver.lstrip('*')
        for r in rows:
            if r['receiver'].endswith(stripped):
                return f"target.name = '{name}' AND target.receiver = '{r['receiver']}'"
        # Fall back to highest-caller match (most connected = most likely target)
        best = query_defn(repo_path, f"""
            SELECT d.id, d.receiver, COUNT(r.from_def) as callers
            FROM definitions d
            LEFT JOIN `references` r ON r.to_def = d.id
            WHERE d.name = '{name}' AND d.test = FALSE
            GROUP BY d.id
            ORDER BY callers DESC
            LIMIT 1
        """)
        if best:
            return f"target.name = '{name}' AND target.receiver = '{best[0]['receiver']}'"

    return None


def get_test_coverage(repo_path: str, name: str, receiver: str) -> set[str]:
    """Get test function names that transitively reference this definition."""
    where = resolve_definition(repo_path, name, receiver)
    if where is None:
        return set()

    sql = f"""
    WITH RECURSIVE tc AS (
        SELECT d.id, d.name, d.test, 0 as depth
        FROM definitions d
        JOIN `references` r ON r.from_def = d.id
        JOIN definitions target ON r.to_def = target.id
        WHERE {where}
        UNION
        SELECT d2.id, d2.name, d2.test, tc.depth + 1
        FROM definitions d2
        JOIN `references` r2 ON r2.from_def = d2.id
        JOIN tc ON r2.to_def = tc.id
        WHERE tc.depth < 4
    )
    SELECT DISTINCT name FROM tc WHERE test = TRUE
    """
    rows = query_defn(repo_path, sql)
    return {r['name'] for r in rows}


def main():
    repo_name = sys.argv[1] if len(sys.argv) > 1 else 'gin'
    limit = int(sys.argv[3]) if len(sys.argv) > 3 and sys.argv[2] == '--limit' else 50

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
        print(f'Error: {repo_path}/.defn not found')
        sys.exit(1)

    df = pd.read_parquet(os.path.expanduser(
        '~/.plancheck/datasets/swe-smith-go/data/train-00000-of-00001.parquet'))
    tasks = df[df['repo'].str.contains(swe_pattern)]

    n = min(limit, len(tasks))
    print(f'Backtesting test prediction for {repo_name}: {len(tasks)} tasks, running {n}')
    print('=' * 70)

    results = []
    skipped = 0

    for i, (_, task) in enumerate(tasks.head(n).iterrows()):
        func_info = extract_func_from_patch(task['patch'])
        if func_info is None:
            skipped += 1
            continue

        name, receiver = func_info
        actual_failing = parse_fail_to_pass(task['FAIL_TO_PASS'])
        if not actual_failing:
            skipped += 1
            continue

        predicted_tests = get_test_coverage(repo_path, name, receiver)

        if not predicted_tests:
            results.append({
                'func': f'({receiver}).{name}' if receiver else name,
                'actual_failing': len(actual_failing),
                'predicted': 0,
                'hits': 0,
                'recall': 0.0,
                'precision': 0.0,
            })
            continue

        hits = predicted_tests & actual_failing
        recall = len(hits) / len(actual_failing) if actual_failing else 0
        precision = len(hits) / len(predicted_tests) if predicted_tests else 0

        results.append({
            'func': f'({receiver}).{name}' if receiver else name,
            'actual_failing': len(actual_failing),
            'predicted': len(predicted_tests),
            'hits': len(hits),
            'recall': round(recall, 3),
            'precision': round(precision, 3),
        })

        r = results[-1]
        icon = '✓' if r['hits'] > 0 else '✗'
        print(f'  {icon} {r["func"]}: failing={r["actual_failing"]}, '
              f'predicted={r["predicted"]}, hits={r["hits"]}, '
              f'R={r["recall"]:.2f} P={r["precision"]:.2f}')

        if (i + 1) % 10 == 0:
            sys.stdout.flush()

    if not results:
        print(f'\nNo scoreable tasks (skipped {skipped})')
        return

    print()
    print('=' * 70)
    print(f'RESULTS: {len(results)} scored, {skipped} skipped')

    avg_recall = sum(r['recall'] for r in results) / len(results)
    avg_precision = sum(r['precision'] for r in results) / len(results)
    hit_rate = sum(1 for r in results if r['hits'] > 0) / len(results)
    zero_predicted = sum(1 for r in results if r['predicted'] == 0)

    print(f'  Avg recall:     {avg_recall:.3f}  (fraction of failing tests predicted)')
    print(f'  Avg precision:  {avg_precision:.3f}  (fraction of predictions that were correct)')
    print(f'  Hit rate:       {hit_rate:.1%}  (tasks where ≥1 failing test was predicted)')
    print(f'  No prediction:  {zero_predicted}/{len(results)}  (definition not found or no test refs)')


if __name__ == '__main__':
    main()
