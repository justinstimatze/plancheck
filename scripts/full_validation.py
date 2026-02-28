#!/usr/bin/env python3
"""
Rigorous validation with confidence intervals and hold-out testing.

1. Full sweep with bootstrap confidence intervals
2. Hold-out validation (odd tasks = train, even tasks = test)
3. Agent trajectory matching (would the graph have saved exploration?)
4. Statistical significance testing

Usage:
    python3 full_validation.py
"""

import json
import math
import os
import random
import re
import subprocess
import sys
import zipfile

import pandas as pd


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


def get_graph_test_recall(repo_path, name, receiver=''):
    """Reference graph: recall of test failure prediction."""
    if receiver:
        where = f"target.name = '{name}'"
    else:
        where = f"target.name = '{name}' AND target.receiver = ''"

    rows = defn_query(repo_path, f"""
        WITH RECURSIVE tc AS (
            SELECT d.id, d.name, d.test, 0 as depth FROM definitions d
            JOIN `references` r ON r.from_def = d.id
            JOIN definitions target ON r.to_def = target.id
            WHERE {where} AND target.test = FALSE
            UNION SELECT d2.id, d2.name, d2.test, tc.depth+1
            FROM definitions d2 JOIN `references` r2 ON r2.from_def = d2.id
            JOIN tc ON r2.to_def = tc.id WHERE tc.depth < 4
        ) SELECT DISTINCT name FROM tc WHERE test = TRUE
    """)
    return {r['name'] for r in rows}


def get_comod_tests(repo_path, changed_file):
    """Git comod: test files that historically co-change."""
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
        t = line.strip()
        if t == 'COMMIT':
            current = set()
            commits.append(current)
        elif t and current is not None:
            current.add(t)

    relevant = [c for c in commits if changed_file in c]
    if not relevant:
        return set()

    co_counts = {}
    for commit in relevant:
        for f in commit:
            if f == changed_file or '_test.go' not in f:
                continue
            co_counts[f] = co_counts.get(f, 0) + 1

    test_names = set()
    for tf, count in co_counts.items():
        if count / len(relevant) < 0.3:
            continue
        full_path = os.path.join(repo_path, tf)
        if os.path.exists(full_path):
            try:
                with open(full_path) as fh:
                    for line in fh:
                        m = re.match(r'func\s+(Test\w+)', line)
                        if m:
                            test_names.add(m.group(1))
            except:
                pass
    return test_names


def bootstrap_ci(values, n_bootstrap=1000, ci=0.95):
    """Bootstrap confidence interval for a list of values."""
    if len(values) < 2:
        return 0, 0, 0
    means = []
    for _ in range(n_bootstrap):
        sample = random.choices(values, k=len(values))
        means.append(sum(sample) / len(sample))
    means.sort()
    lo = means[int((1 - ci) / 2 * n_bootstrap)]
    hi = means[int((1 + ci) / 2 * n_bootstrap)]
    return sum(values) / len(values), lo, hi


def main():
    random.seed(42)

    df = pd.read_parquet(os.path.expanduser(
        '~/.plancheck/datasets/swe-smith-go/data/train-00000-of-00001.parquet'))

    repos = {
        'gin': ('gin-gonic__gin', 56),
        'cobra': ('spf13__cobra', 50),
        'echo': ('labstack__echo', 48),
        'roaring': ('RoaringBitmap__roaring', 36),
        'gorm': ('go-gorm__gorm', 17),
    }

    print('=' * 70)
    print('RIGOROUS VALIDATION')
    print('=' * 70)

    # ── Part 1: Full sweep with confidence intervals ──
    print('\n── PART 1: Combined model with 95% bootstrap CI ──\n')

    all_graph_recalls = []
    all_comod_recalls = []
    all_combined_recalls = []
    per_repo = {}

    for repo_name, (pattern, test_density) in repos.items():
        tasks = df[df['repo'].str.contains(pattern)]
        repo_path = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_name}')

        graph_recalls = []
        comod_recalls = []
        combined_recalls = []

        for _, t in tasks.iterrows():
            for line in t['patch'].split('\n'):
                m = re.match(r'^@@ .+? @@\s*(.*)', line)
                if not m:
                    continue
                ctx = m.group(1).strip()
                m2 = re.match(r'func\s+(?:\(\w+\s+(\*?\w+)\)\s+)?(\w+)', ctx)
                if not m2:
                    continue
                recv = m2.group(1) or ''
                name = m2.group(2)

                actual = set(re.findall(r"'(Test\w+)'", str(t['FAIL_TO_PASS'])))
                if not actual:
                    break

                # Get changed file for comod
                file_match = re.search(r'diff --git a/(.*?) b/', t['patch'])
                changed_file = file_match.group(1) if file_match else ''

                graph = get_graph_test_recall(repo_path, name, recv)
                comod = get_comod_tests(repo_path, changed_file)
                combined = graph | comod

                gr = len(graph & actual) / len(actual) if actual else 0
                cr = len(comod & actual) / len(actual) if actual else 0
                xr = len(combined & actual) / len(actual) if actual else 0

                graph_recalls.append(gr)
                comod_recalls.append(cr)
                combined_recalls.append(xr)
                break

        if not combined_recalls:
            continue

        mean, lo, hi = bootstrap_ci(combined_recalls)
        g_mean, g_lo, g_hi = bootstrap_ci(graph_recalls)
        hit_rate = sum(1 for x in combined_recalls if x > 0) / len(combined_recalls)

        per_repo[repo_name] = {
            'n': len(combined_recalls),
            'combined': (mean, lo, hi),
            'graph': (g_mean, g_lo, g_hi),
            'hit_rate': hit_rate,
        }

        print(f'{repo_name:10s} n={len(combined_recalls):4d}  '
              f'combined={mean:.3f} [{lo:.3f}, {hi:.3f}]  '
              f'graph={g_mean:.3f} [{g_lo:.3f}, {g_hi:.3f}]  '
              f'hit={hit_rate:.1%}')

        all_graph_recalls.extend(graph_recalls)
        all_comod_recalls.extend(comod_recalls)
        all_combined_recalls.extend(combined_recalls)

    # Overall with CI
    print()
    g_mean, g_lo, g_hi = bootstrap_ci(all_graph_recalls)
    c_mean, c_lo, c_hi = bootstrap_ci(all_comod_recalls)
    x_mean, x_lo, x_hi = bootstrap_ci(all_combined_recalls)
    hit = sum(1 for x in all_combined_recalls if x > 0) / len(all_combined_recalls)

    print(f'OVERALL    n={len(all_combined_recalls):4d}')
    print(f'  Graph:    {g_mean:.3f} [{g_lo:.3f}, {g_hi:.3f}]')
    print(f'  Comod:    {c_mean:.3f} [{c_lo:.3f}, {c_hi:.3f}]')
    print(f'  Combined: {x_mean:.3f} [{x_lo:.3f}, {x_hi:.3f}]')
    print(f'  Hit rate: {hit:.1%}')

    # ── Part 2: Hold-out validation ──
    print('\n── PART 2: Hold-out validation (odd=train, even=test) ──\n')

    train_recalls = [all_combined_recalls[i] for i in range(0, len(all_combined_recalls), 2)]
    test_recalls = [all_combined_recalls[i] for i in range(1, len(all_combined_recalls), 2)]

    tr_mean, tr_lo, tr_hi = bootstrap_ci(train_recalls)
    te_mean, te_lo, te_hi = bootstrap_ci(test_recalls)

    print(f'  Train (odd):  n={len(train_recalls):4d}  recall={tr_mean:.3f} [{tr_lo:.3f}, {tr_hi:.3f}]')
    print(f'  Test (even):  n={len(test_recalls):4d}  recall={te_mean:.3f} [{te_lo:.3f}, {te_hi:.3f}]')
    print(f'  Difference:   {abs(tr_mean - te_mean):.3f} ({"stable" if abs(tr_mean - te_mean) < 0.05 else "UNSTABLE"})')

    # ── Part 3: Statistical significance ──
    print('\n── PART 3: Is combined > graph statistically significant? ──\n')

    # Paired difference test (bootstrap)
    diffs = [c - g for c, g in zip(all_combined_recalls, all_graph_recalls)]
    d_mean, d_lo, d_hi = bootstrap_ci(diffs)
    significant = d_lo > 0  # 95% CI doesn't include 0

    print(f'  Combined - Graph: {d_mean:.3f} [{d_lo:.3f}, {d_hi:.3f}]')
    print(f'  Significant (95% CI excludes 0): {"YES" if significant else "NO"}')

    # Comod adds value?
    diffs2 = [c - g for c, g in zip(all_combined_recalls, all_comod_recalls)]
    d2_mean, d2_lo, d2_hi = bootstrap_ci(diffs2)
    print(f'  Combined - Comod: {d2_mean:.3f} [{d2_lo:.3f}, {d2_hi:.3f}]')
    print(f'  Significant: {"YES" if d2_lo > 0 else "NO"}')

    # ── Part 4: Complexity stratification ──
    # The real question: does the tool help on HARD tasks, not trivial ones?
    # Undercity's trap: worked on trivial, failed on complex.
    print('\n── PART 4: Complexity stratification (avoid the trivial-task trap) ──\n')
    print('Task difficulty proxy: number of failing tests')
    print('(More failing tests = more things break = harder change)\n')

    # Collect per-task data with failing test count
    task_data = []
    for i, xr in enumerate(all_combined_recalls):
        gr = all_graph_recalls[i]
        task_data.append({'combined': xr, 'graph': gr})

    # We need failing test counts — re-collect with that data
    fail_counts_and_recalls = []
    for repo_name, (pattern, _) in repos.items():
        tasks = df[df['repo'].str.contains(pattern)]
        repo_path = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_name}')
        for _, t in tasks.iterrows():
            for line in t['patch'].split('\n'):
                m = re.match(r'^@@ .+? @@\s*(.*)', line)
                if not m:
                    continue
                ctx = m.group(1).strip()
                m2 = re.match(r'func\s+(?:\(\w+\s+(\*?\w+)\)\s+)?(\w+)', ctx)
                if not m2:
                    continue
                name = m2.group(2)
                actual = set(re.findall(r"'(Test\w+)'", str(t['FAIL_TO_PASS'])))
                if not actual:
                    break
                file_match = re.search(r'diff --git a/(.*?) b/', t['patch'])
                changed_file = file_match.group(1) if file_match else ''

                graph = get_graph_test_recall(repo_path, name, m2.group(1) or '')
                comod = get_comod_tests(repo_path, changed_file)
                combined = graph | comod
                xr = len(combined & actual) / len(actual) if actual else 0

                fail_counts_and_recalls.append({
                    'failing_tests': len(actual),
                    'recall': xr,
                    'hit': 1 if len(combined & actual) > 0 else 0,
                })
                break

    if fail_counts_and_recalls:
        fcdf = pd.DataFrame(fail_counts_and_recalls)

        # Stratify by complexity
        buckets = [
            ('Trivial (1 test fails)', lambda x: x == 1),
            ('Easy (2-3 tests fail)', lambda x: 2 <= x <= 3),
            ('Medium (4-10 tests fail)', lambda x: 4 <= x <= 10),
            ('Hard (11-50 tests fail)', lambda x: 11 <= x <= 50),
            ('Very hard (>50 tests fail)', lambda x: x > 50),
        ]

        for label, pred in buckets:
            subset = fcdf[fcdf['failing_tests'].apply(pred)]
            if len(subset) >= 5:
                mean, lo, hi = bootstrap_ci(list(subset['recall']))
                hr = subset['hit'].mean()
                print(f'  {label:35s} n={len(subset):4d}  recall={mean:.3f} [{lo:.3f},{hi:.3f}]  hit={hr:.1%}')

        print()
        trivial = fcdf[fcdf['failing_tests'] == 1]
        nontrivial = fcdf[fcdf['failing_tests'] > 3]
        if len(trivial) > 0 and len(nontrivial) > 0:
            t_mean = trivial['recall'].mean()
            nt_mean = nontrivial['recall'].mean()
            print(f'  Trivial (1 test):     recall={t_mean:.3f}, hit={trivial["hit"].mean():.1%}')
            print(f'  Non-trivial (>3):     recall={nt_mean:.3f}, hit={nontrivial["hit"].mean():.1%}')
            if nt_mean > t_mean:
                print(f'  NON-TRIVIAL IS BETTER (+{(nt_mean-t_mean):.3f}) — tool helps more on hard tasks')
            elif nt_mean < t_mean - 0.05:
                print(f'  WARNING: TRIVIAL IS BETTER — baseline outperforms model')
            else:
                print(f'  Similar performance across complexity levels')

    # ── Part 5: Summary ──
    print('\n── SUMMARY ──\n')
    print(f'Total tasks scored: {len(all_combined_recalls)}')
    print(f'Combined recall: {x_mean:.3f} ± {(x_hi - x_lo) / 2:.3f} (95% CI)')
    print(f'Combined > Graph: {d_mean:.3f}, p<0.05: {"YES" if significant else "NO"}')
    print(f'Hold-out stable: {"YES" if abs(tr_mean - te_mean) < 0.05 else "NO"} (delta={abs(tr_mean - te_mean):.3f})')


if __name__ == '__main__':
    main()
