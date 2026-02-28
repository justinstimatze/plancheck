#!/usr/bin/env python3
"""
Backtest: do real-world GitHub PRs touch the files the reference graph predicts?

For each PR with file data:
1. Take the first changed file as the "seed"
2. Query defn reference graph for blast radius of definitions in that file
3. Compare predicted files against actual PR file set
4. Score recall: what fraction of PR files did the graph predict?

This tests on REAL development tasks, not synthetic SWE-smith patches.

Usage:
    python3 pr_backtest.py [repo-name]
"""

import json
import os
import re
import subprocess
import sys


def query_defn(repo_path: str, sql: str) -> list[dict]:
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


def get_callers_modules(repo_path: str, module_hint: str) -> set[str]:
    """Get module paths that contain callers of definitions in the given module."""
    # Find definitions in the module matching the hint
    sql = f"""
    SELECT DISTINCT m2.path
    FROM definitions d
    JOIN `references` r ON r.to_def = d.id
    JOIN definitions d2 ON r.from_def = d2.id
    JOIN modules m ON d.module_id = m.id
    JOIN modules m2 ON d2.module_id = m2.id
    WHERE m.path LIKE '%{module_hint}%'
    AND d.test = FALSE AND d2.test = FALSE
    AND m.id != m2.id
    """
    rows = query_defn(repo_path, sql)
    return {r['path'] for r in rows}


def module_to_files(module_path: str, all_files: set[str]) -> set[str]:
    """Map a module path to likely files in the PR."""
    # Module path: github.com/gin-gonic/gin/internal/bytesconv
    # File might be: internal/bytesconv/bytesconv.go
    parts = module_path.split('/')
    matches = set()
    for f in all_files:
        for i in range(len(parts)):
            suffix = '/'.join(parts[i:])
            if f.startswith(suffix) or suffix in f:
                matches.add(f)
                break
    return matches


def main():
    repos = {
        'gin': ('gin-gonic/gin', 'gin-gonic__gin'),
        'cobra': ('spf13/cobra', 'spf13__cobra'),
        'echo': ('labstack/echo', 'labstack__echo'),
        'gorm': ('go-gorm/gorm', 'go-gorm__gorm'),
    }

    target = sys.argv[1] if len(sys.argv) > 1 else None
    if target:
        repos = {target: repos[target]}

    for repo_name, (github_name, pr_file) in repos.items():
        repo_path = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_name}')
        pr_path = os.path.expanduser(f'~/.plancheck/datasets/pr-data/{pr_file}.json')

        if not os.path.exists(pr_path):
            continue

        with open(pr_path) as f:
            prs = json.load(f)

        prs_with_files = [p for p in prs if p.get('files') and len(p['files']) >= 2]
        go_prs = [p for p in prs_with_files if any(f.endswith('.go') for f in p['files'])]

        print(f'=== {repo_name}: {len(go_prs)} Go PRs with 2+ files ===')

        scored = 0
        total_recall = 0
        total_hits = 0

        for pr in go_prs:
            files = set(pr['files'])
            go_files = {f for f in files if f.endswith('.go') and '_test.go' not in f}
            if len(go_files) < 2:
                continue

            # Use first Go file as seed
            seed = sorted(go_files)[0]
            other_files = go_files - {seed}

            # Get module hint from seed file path
            # e.g., "internal/bytesconv/bytesconv.go" → "bytesconv"
            parts = seed.replace('.go', '').split('/')
            module_hint = parts[-1] if parts else seed

            # Query reference graph for related modules
            related_modules = get_callers_modules(repo_path, module_hint)

            if not related_modules:
                continue

            # Map modules back to files
            predicted_files = set()
            for mod in related_modules:
                predicted_files.update(module_to_files(mod, files))
            predicted_files.discard(seed)

            if not predicted_files:
                continue

            # Score
            hits = predicted_files & other_files
            recall = len(hits) / len(other_files) if other_files else 0

            scored += 1
            total_recall += recall
            total_hits += (1 if hits else 0)

            icon = '✓' if hits else '✗'
            print(f'  {icon} PR #{pr["number"]}: seed={seed}, '
                  f'other={len(other_files)}, predicted={len(predicted_files)}, '
                  f'hits={len(hits)}, R={recall:.2f}')

        if scored:
            print(f'\n  Scored: {scored}, Hit rate: {total_hits/scored:.0%}, '
                  f'Avg recall: {total_recall/scored:.3f}')
        else:
            print('  No scoreable PRs')
        print()


if __name__ == '__main__':
    main()
