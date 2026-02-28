#!/usr/bin/env python3
"""
Combined model on real PRs: reference graph + git comod for file prediction.

For each PR with 2+ Go files:
1. Graph: definitions in seed file → callers' source_files
2. Comod: git log co-changes with seed file
3. Combined: union
4. Score against actual PR file set

Usage:
    python3 pr_combined.py [repo-name]
"""

import json
import os
import re
import subprocess
import sys


def defn_query(repo_path, sql):
    defn_dir = os.path.join(repo_path, '.defn')
    try:
        result = subprocess.run(
            ['dolt', 'sql', '-q', sql, '-r', 'json'],
            cwd=defn_dir, capture_output=True, text=True, timeout=10)
        if result.returncode != 0:
            return []
        return json.loads(result.stdout).get('rows', [])
    except:
        return []


def graph_predict_files(repo_path, seed_file):
    """Reference graph: source files containing callers of definitions in seed."""
    basename = os.path.basename(seed_file)
    rows = defn_query(repo_path,
        f"SELECT DISTINCT caller.source_file "
        f"FROM definitions d "
        f"JOIN `references` r ON r.to_def = d.id "
        f"JOIN definitions caller ON r.from_def = caller.id "
        f"WHERE d.source_file = '{basename}' AND d.test = FALSE "
        f"AND caller.test = FALSE AND caller.source_file != '' "
        f"AND caller.source_file != '{basename}'")
    return {r['source_file'] for r in rows if r.get('source_file')}


def comod_predict_files(repo_path, seed_file):
    """Git comod: files that historically co-change with seed."""
    basename = os.path.basename(seed_file)
    try:
        result = subprocess.run(
            ['git', '-C', repo_path, 'log', '--diff-filter=M',
             '--name-only', '--pretty=format:COMMIT', '-n', '200'],
            capture_output=True, text=True, timeout=10)
        if result.returncode != 0:
            return set()
    except:
        return set()

    commits = []
    current = None
    for line in result.stdout.split('\n'):
        trimmed = line.strip()
        if trimmed == 'COMMIT':
            current = set()
            commits.append(current)
        elif trimmed and current is not None:
            current.add(trimmed)

    # Find files that co-change with seed
    relevant = [c for c in commits if any(os.path.basename(f) == basename for f in c)]
    if not relevant:
        return set()

    co_counts = {}
    for commit in relevant:
        for f in commit:
            fb = os.path.basename(f)
            if fb == basename or '_test.go' in fb:
                continue
            if fb.endswith('.go'):
                co_counts[fb] = co_counts.get(fb, 0) + 1

    threshold = max(2, len(relevant) * 0.3)
    return {f for f, c in co_counts.items() if c >= threshold}


def score(predicted, actual):
    if not actual:
        return 0, 0, 0
    hits = len(predicted & actual)
    recall = hits / len(actual)
    precision = hits / len(predicted) if predicted else 0
    return recall, precision, hits


def main():
    repos = {
        'gin': 'gin-gonic__gin',
        'cobra': 'spf13__cobra',
        'echo': 'labstack__echo',
        'gorm': 'go-gorm__gorm',
    }

    target = sys.argv[1] if len(sys.argv) > 1 else None
    if target:
        repos = {target: repos.get(target, target)}

    all_graph, all_comod, all_combined = [], [], []
    all_g_hits, all_c_hits, all_x_hits = 0, 0, 0
    total_scored = 0

    for repo_name, pr_file in repos.items():
        repo_path = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_name}')
        pr_path = os.path.expanduser(f'~/.plancheck/datasets/pr-data/{pr_file}.json')
        if not os.path.exists(pr_path):
            continue

        with open(pr_path) as f:
            prs = json.load(f)

        go_prs = [p for p in prs
                  if p.get('files')
                  and len([f for f in p['files'] if f.endswith('.go') and '_test.go' not in f]) >= 2]

        print(f'=== {repo_name}: {len(go_prs)} PRs ===')

        for pr in go_prs:
            go_files = [f for f in pr['files'] if f.endswith('.go') and '_test.go' not in f]
            if len(go_files) < 2:
                continue

            seed = go_files[0]
            other_basenames = {os.path.basename(f) for f in go_files[1:]}

            g_pred = graph_predict_files(repo_path, seed)
            c_pred = comod_predict_files(repo_path, seed)
            x_pred = g_pred | c_pred

            if not g_pred and not c_pred:
                continue

            gr, _, gh = score(g_pred, other_basenames)
            cr, _, ch = score(c_pred, other_basenames)
            xr, _, xh = score(x_pred, other_basenames)

            all_graph.append(gr)
            all_comod.append(cr)
            all_combined.append(xr)
            all_g_hits += (1 if gh else 0)
            all_c_hits += (1 if ch else 0)
            all_x_hits += (1 if xh else 0)
            total_scored += 1

            icon = '✓' if xh else '✗'
            print(f'  {icon} PR #{pr["number"]}: G={gr:.2f} C={cr:.2f} X={xr:.2f} '
                  f'(graph={len(g_pred)}, comod={len(c_pred)}, other={len(other_basenames)})')

    if not total_scored:
        print('No scoreable PRs')
        return

    print()
    print('=' * 60)
    print(f'RESULTS ({total_scored} PRs scored)')
    print()
    avg_g = sum(all_graph) / total_scored
    avg_c = sum(all_comod) / total_scored
    avg_x = sum(all_combined) / total_scored
    hr_g = all_g_hits / total_scored
    hr_c = all_c_hits / total_scored
    hr_x = all_x_hits / total_scored

    print(f'  {"Metric":<20s} {"Graph":>10s} {"Comod":>10s} {"Combined":>10s}')
    print(f'  {"-"*20} {"-"*10} {"-"*10} {"-"*10}')
    print(f'  {"Avg recall":<20s} {avg_g:>10.3f} {avg_c:>10.3f} {avg_x:>10.3f}')
    print(f'  {"Hit rate":<20s} {hr_g:>9.0%} {hr_c:>9.0%} {hr_x:>9.0%}')

    if avg_x > avg_g and avg_x > avg_c:
        print(f'\n  Combined wins: +{(avg_x-avg_g)/avg_g*100:.0f}% over graph, +{(avg_x-avg_c)/avg_c*100:.0f}% over comod')


if __name__ == '__main__':
    main()
