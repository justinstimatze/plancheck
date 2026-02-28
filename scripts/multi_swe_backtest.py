#!/usr/bin/env python3
"""
Backtest against Multi-SWE-bench Go: real multi-file bug fixes.

For each task with 2+ Go files in the fix patch:
1. Take the first changed file as seed
2. Query reference graph for files containing callers of definitions in seed
3. Compare predicted files against actual fix patch files
4. Score recall and hit rate

This is the definitive test: can the reference graph predict which files
need changing in a real multi-file bug fix?

Usage:
    python3 multi_swe_backtest.py [repo-name]
"""

import json
import os
import re
import subprocess
import sys


def defn_query(repo_path, sql):
    defn_dir = os.path.join(repo_path, '.defn')
    try:
        result = subprocess.run(['dolt', 'sql', '-q', sql, '-r', 'json'],
            cwd=defn_dir, capture_output=True, text=True, timeout=10)
        if result.returncode != 0:
            return []
        return json.loads(result.stdout).get('rows', [])
    except:
        return []


def predict_files(repo_path, seed_file):
    """Predict which other files would be affected by changing seed_file."""
    basename = os.path.basename(seed_file)

    # Get source files of callers of definitions in this file
    rows = defn_query(repo_path,
        f"SELECT DISTINCT caller.source_file "
        f"FROM definitions d "
        f"JOIN `references` r ON r.to_def = d.id "
        f"JOIN definitions caller ON r.from_def = caller.id "
        f"WHERE d.source_file = '{basename}' AND d.test = FALSE "
        f"AND caller.test = FALSE AND caller.source_file != '' "
        f"AND caller.source_file != '{basename}'")
    return {r['source_file'] for r in rows if r.get('source_file')}


def main():
    repo_map = {
        'cli': ('cli__cli_dataset.jsonl', os.path.expanduser('~/.plancheck/datasets/repos/cli')),
    }

    target = sys.argv[1] if len(sys.argv) > 1 else 'cli'
    if target not in repo_map:
        print(f'Unknown repo: {target}. Available: {list(repo_map.keys())}')
        sys.exit(1)

    dataset_file, repo_path = repo_map[target]
    dataset_path = os.path.expanduser(f'~/.plancheck/datasets/multi-swe-bench/go/{dataset_file}')

    if not os.path.isdir(os.path.join(repo_path, '.defn')):
        print(f'No .defn/ in {repo_path}. Run defn init first.')
        sys.exit(1)

    # Load tasks
    tasks = []
    with open(dataset_path) as f:
        for line in f:
            tasks.append(json.loads(line))

    # Filter to multi-file tasks
    multi_file = []
    for t in tasks:
        patch = t.get('fix_patch', '')
        files = set(re.findall(r'diff --git a/(.*?) b/', patch))
        go_files = sorted([f for f in files if f.endswith('.go') and '_test.go' not in f])
        if len(go_files) >= 2:
            multi_file.append({
                'instance_id': t.get('instance_id', '?'),
                'title': t.get('title', '?')[:50],
                'go_files': go_files,
            })

    print(f'Multi-SWE-bench {target}: {len(multi_file)} multi-file Go tasks')
    print(f'(out of {len(tasks)} total tasks)')
    print('=' * 70)

    scored = 0
    total_recall = 0
    total_hits = 0
    by_file_count = {}  # stratify by number of files

    for task in multi_file:
        go_files = task['go_files']
        seed = go_files[0]
        others = {os.path.basename(f) for f in go_files[1:]}

        predicted = predict_files(repo_path, seed)
        if not predicted and len(go_files) > 1:
            # Try second file as seed
            seed = go_files[1]
            others = {os.path.basename(f) for f in go_files if f != seed}
            predicted = predict_files(repo_path, seed)

        hits = predicted & others
        recall = len(hits) / len(others) if others else 0

        scored += 1
        total_recall += recall
        total_hits += (1 if hits else 0)

        # Stratify
        n = len(go_files)
        bucket = f'{n} files' if n <= 5 else '6+ files'
        if bucket not in by_file_count:
            by_file_count[bucket] = {'recalls': [], 'hits': 0, 'n': 0}
        by_file_count[bucket]['recalls'].append(recall)
        by_file_count[bucket]['hits'] += (1 if hits else 0)
        by_file_count[bucket]['n'] += 1

        icon = '✓' if hits else '✗'
        if scored <= 20 or hits:  # print first 20 + all hits
            print(f'  {icon} {task["title"]:45s} seed={os.path.basename(seed):20s} '
                  f'{len(others)}→{len(hits)}/{len(predicted)} R={recall:.2f}')

    if not scored:
        print('No scoreable tasks')
        return

    print()
    print('=' * 70)
    avg_recall = total_recall / scored
    hit_rate = total_hits / scored
    print(f'RESULTS: {scored} scored, hit rate={hit_rate:.0%}, avg recall={avg_recall:.3f}')
    print()

    # By file count
    print('By number of files in fix:')
    for bucket in sorted(by_file_count.keys()):
        b = by_file_count[bucket]
        br = sum(b['recalls']) / len(b['recalls'])
        bh = b['hits'] / b['n']
        print(f'  {bucket:10s} n={b["n"]:3d}  recall={br:.3f}  hit={bh:.0%}')


if __name__ == '__main__':
    main()
