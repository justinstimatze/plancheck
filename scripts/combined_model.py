#!/usr/bin/env python3
"""
Combined model: reference graph + git comod for test failure prediction.

Benter's insight: combine structural analysis (reference graph) with
statistical analysis (git co-modification) for better predictions than either alone.

For each SWE-smith-go task:
1. Reference graph: which tests transitively reference the changed definition?
2. Git comod: which test files historically co-change with the modified file?
3. Combined: union of both signals
4. Score each against FAIL_TO_PASS ground truth

Usage:
    python3 combined_model.py <repo-name> [--limit N]
"""

import json
import os
import re
import subprocess
import sys

import pandas as pd


def extract_func_from_patch(patch: str) -> tuple[str, str, str] | None:
    """Extract function name, receiver, and file from patch."""
    current_file = None
    for line in patch.split('\n'):
        m = re.match(r'^diff --git a/(.*?) b/(.*?)$', line)
        if m:
            current_file = m.group(2)
            continue
        m = re.match(r'^@@ .+? @@\s*(.*)', line)
        if not m:
            continue
        ctx = m.group(1).strip()
        m2 = re.match(r'func\s+\(\w+\s+(\*?\w+)\)\s+(\w+)', ctx)
        if m2:
            return m2.group(2), m2.group(1), current_file
        m2 = re.match(r'func\s+(\w+)', ctx)
        if m2:
            return m2.group(1), '', current_file
    return None


def parse_fail_to_pass(fail_str) -> set[str]:
    if isinstance(fail_str, str):
        return set(re.findall(r"'(Test\w+)'", fail_str))
    tests = set()
    for item in fail_str:
        if isinstance(item, str) and item.startswith('Test'):
            tests.add(item.split('/')[0])
    return tests


def query_defn(repo_path: str, sql: str) -> list[dict]:
    defn_dir = os.path.join(repo_path, '.defn')
    try:
        result = subprocess.run(
            ['dolt', 'sql', '-q', sql, '-r', 'json'],
            cwd=defn_dir, capture_output=True, text=True, timeout=10)
        if result.returncode != 0:
            return []
        return json.loads(result.stdout).get('rows', [])
    except (subprocess.TimeoutExpired, json.JSONDecodeError):
        return []


def get_graph_tests(repo_path: str, name: str, receiver: str) -> set[str]:
    """Reference graph: test functions that transitively call this definition."""
    # Fuzzy resolve
    if receiver:
        where = f"name = '{name}' AND receiver = '{receiver}' AND test = FALSE"
    else:
        where = f"name = '{name}' AND receiver = '' AND test = FALSE"

    rows = query_defn(repo_path, f"SELECT id FROM definitions WHERE {where} LIMIT 1")
    if not rows:
        # Try name-only
        rows = query_defn(repo_path,
            f"SELECT id FROM definitions WHERE name = '{name}' AND test = FALSE LIMIT 1")
    if not rows:
        return set()

    def_id = rows[0]['id']
    sql = f"""
    WITH RECURSIVE tc AS (
        SELECT d.id, d.name, d.test, 0 as depth
        FROM definitions d
        JOIN `references` r ON r.from_def = d.id
        WHERE r.to_def = {def_id}
        UNION
        SELECT d2.id, d2.name, d2.test, tc.depth + 1
        FROM definitions d2
        JOIN `references` r2 ON r2.from_def = d2.id
        JOIN tc ON r2.to_def = tc.id
        WHERE tc.depth < 4
    )
    SELECT DISTINCT name FROM tc WHERE test = TRUE
    """
    return {r['name'] for r in query_defn(repo_path, sql)}


def get_comod_tests(repo_path: str, changed_file: str) -> set[str]:
    """Git comod: test files that historically co-change with the modified file."""
    try:
        # Get git log of co-changes
        result = subprocess.run(
            ['git', '-C', repo_path, 'log', '--diff-filter=M',
             '--name-only', '--pretty=format:COMMIT', '-n', '200'],
            capture_output=True, text=True, timeout=10)
        if result.returncode != 0:
            return set()
    except subprocess.TimeoutExpired:
        return set()

    # Parse commits
    commits = []
    current = None
    for line in result.stdout.split('\n'):
        trimmed = line.strip()
        if trimmed == 'COMMIT':
            current = set()
            commits.append(current)
        elif trimmed and current is not None:
            current.add(trimmed)

    # Find test files that co-change with changed_file
    relevant = [c for c in commits if changed_file in c]
    if not relevant:
        return set()

    co_counts = {}
    for commit in relevant:
        for f in commit:
            if f == changed_file:
                continue
            if '_test.go' in f:
                co_counts[f] = co_counts.get(f, 0) + 1

    # Extract test function names from co-changing test files
    test_names = set()
    for test_file, count in co_counts.items():
        freq = count / len(relevant)
        if freq < 0.3:  # Lower threshold for test files
            continue
        # Read the test file to extract Test function names
        full_path = os.path.join(repo_path, test_file)
        if os.path.exists(full_path):
            try:
                with open(full_path) as f:
                    for line in f:
                        m = re.match(r'func\s+(Test\w+)', line)
                        if m:
                            test_names.add(m.group(1))
            except (IOError, UnicodeDecodeError):
                pass

    return test_names


def score(predicted: set, actual: set) -> tuple[float, float, int]:
    """Return (recall, precision, hits)."""
    if not actual:
        return 0, 0, 0
    hits = len(predicted & actual)
    recall = hits / len(actual) if actual else 0
    precision = hits / len(predicted) if predicted else 0
    return recall, precision, hits


def main():
    repo_name = sys.argv[1] if len(sys.argv) > 1 else 'gin'
    limit = int(sys.argv[3]) if len(sys.argv) > 3 and sys.argv[2] == '--limit' else 50

    repo_map = {
        'gin': 'gin-gonic__gin', 'caddy': 'caddyserver__caddy',
        'cobra': 'spf13__cobra', 'echo': 'labstack__echo',
        'gorm': 'go-gorm__gorm', 'bleve': 'blevesearch__bleve',
        'roaring': 'RoaringBitmap__roaring', 'revive': 'mgechev__revive',
    }
    swe_pattern = repo_map.get(repo_name, repo_name)
    repo_path = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_name}')

    df = pd.read_parquet(os.path.expanduser(
        '~/.plancheck/datasets/swe-smith-go/data/train-00000-of-00001.parquet'))
    tasks = df[df['repo'].str.contains(swe_pattern)]
    n = min(limit, len(tasks))

    print(f'Combined model backtest: {repo_name} ({n} tasks)')
    print('=' * 70)
    print(f'{"":30s} {"Graph":>10s} {"Comod":>10s} {"Combined":>10s}')
    print('-' * 70)

    graph_recalls, comod_recalls, combined_recalls = [], [], []
    graph_hits_total, comod_hits_total, combined_hits_total = 0, 0, 0
    scored = 0

    for _, task in tasks.head(n).iterrows():
        info = extract_func_from_patch(task['patch'])
        if info is None:
            continue
        name, receiver, changed_file = info
        actual = parse_fail_to_pass(task['FAIL_TO_PASS'])
        if not actual:
            continue

        graph_tests = get_graph_tests(repo_path, name, receiver)
        comod_tests = get_comod_tests(repo_path, changed_file)
        combined_tests = graph_tests | comod_tests

        gr, gp, gh = score(graph_tests, actual)
        cr, cp, ch = score(comod_tests, actual)
        xr, xp, xh = score(combined_tests, actual)

        graph_recalls.append(gr)
        comod_recalls.append(cr)
        combined_recalls.append(xr)
        graph_hits_total += (1 if gh > 0 else 0)
        comod_hits_total += (1 if ch > 0 else 0)
        combined_hits_total += (1 if xh > 0 else 0)
        scored += 1

    if not scored:
        print('No scoreable tasks')
        return

    print()
    print('=' * 70)
    print(f'RESULTS ({scored} tasks scored)')
    print()
    print(f'  {"Metric":<20s} {"Graph":>10s} {"Comod":>10s} {"Combined":>10s}')
    print(f'  {"-"*20} {"-"*10} {"-"*10} {"-"*10}')

    avg_g = sum(graph_recalls) / scored
    avg_c = sum(comod_recalls) / scored
    avg_x = sum(combined_recalls) / scored
    print(f'  {"Avg recall":<20s} {avg_g:>10.3f} {avg_c:>10.3f} {avg_x:>10.3f}')

    hr_g = graph_hits_total / scored
    hr_c = comod_hits_total / scored
    hr_x = combined_hits_total / scored
    print(f'  {"Hit rate":<20s} {hr_g:>9.1%} {hr_c:>9.1%} {hr_x:>9.1%}')

    # Did combined beat both?
    print()
    if avg_x > avg_g and avg_x > avg_c:
        lift_g = (avg_x - avg_g) / avg_g * 100 if avg_g > 0 else float('inf')
        lift_c = (avg_x - avg_c) / avg_c * 100 if avg_c > 0 else float('inf')
        print(f'  Combined wins: +{lift_g:.1f}% over graph, +{lift_c:.1f}% over comod')
    elif avg_g >= avg_x:
        print(f'  Graph alone is sufficient (combined adds no lift)')
    else:
        print(f'  Comod alone is sufficient (combined adds no lift)')


if __name__ == '__main__':
    main()
