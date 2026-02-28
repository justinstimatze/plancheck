#!/usr/bin/env python3
"""
Extract cross-project base rates from backtesting data.

These become reference-class predictions:
  "A behavior-change to a method with 15 callers on a type with 50%
   test density historically has X% prediction accuracy."

Usage:
    python3 base_rates.py
"""

import json
import os
import re
import subprocess
import sys

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


def main():
    df = pd.read_parquet(os.path.expanduser(
        '~/.plancheck/datasets/swe-smith-go/data/train-00000-of-00001.parquet'))

    repos = {
        'gin': ('gin-gonic__gin', 56),
        'cobra': ('spf13__cobra', 50),
        'echo': ('labstack__echo', 48),
        'roaring': ('RoaringBitmap__roaring', 36),
        'gorm': ('go-gorm__gorm', 17),
        'bleve': ('blevesearch__bleve', 20),
    }

    all_entries = []

    for repo_name, (pattern, test_density) in repos.items():
        tasks = df[df['repo'].str.contains(pattern)]
        repo_path = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_name}')

        for _, t in tasks.head(80).iterrows():
            for line in t['patch'].split('\n'):
                m = re.match(r'^@@ .+? @@\s*(.*)', line)
                if not m:
                    continue
                ctx = m.group(1).strip()

                # Parse function info
                is_method = False
                m2 = re.match(r'func\s+\(\w+\s+(\*?\w+)\)\s+(\w+)', ctx)
                if m2:
                    recv, name = m2.group(1), m2.group(2)
                    is_method = True
                else:
                    m2 = re.match(r'func\s+(\w+)', ctx)
                    if not m2:
                        continue
                    recv, name = '', m2.group(1)

                # Get blast radius
                rows = defn_query(repo_path,
                    f"SELECT (SELECT COUNT(*) FROM `references` r WHERE r.to_def = d.id) as callers "
                    f"FROM definitions d WHERE d.name = '{name}' AND d.test = FALSE LIMIT 1")
                if not rows:
                    break
                callers = int(rows[0]['callers'])

                # Get test prediction recall
                actual = set(re.findall(r"'(Test\w+)'", str(t['FAIL_TO_PASS'])))
                if not actual:
                    break

                pred_rows = defn_query(repo_path,
                    f"WITH RECURSIVE tc AS ("
                    f"SELECT d.id, d.name, d.test, 0 as depth FROM definitions d "
                    f"JOIN `references` r ON r.from_def = d.id "
                    f"JOIN definitions target ON r.to_def = target.id "
                    f"WHERE target.name = '{name}' AND target.test = FALSE "
                    f"UNION SELECT d2.id, d2.name, d2.test, tc.depth+1 "
                    f"FROM definitions d2 JOIN `references` r2 ON r2.from_def = d2.id "
                    f"JOIN tc ON r2.to_def = tc.id WHERE tc.depth < 4"
                    f") SELECT DISTINCT name FROM tc WHERE test = TRUE")
                predicted = {r['name'] for r in pred_rows}
                hits = len(predicted & actual)
                recall = hits / len(actual) if actual else 0

                all_entries.append({
                    'repo': repo_name,
                    'name': name,
                    'is_method': is_method,
                    'callers': callers,
                    'test_density': test_density,
                    'failing_tests': len(actual),
                    'predicted_tests': len(predicted),
                    'recall': recall,
                    'hit': 1 if hits > 0 else 0,
                })
                break

    if not all_entries:
        print('No data')
        return

    edf = pd.DataFrame(all_entries)

    print(f'=== CROSS-PROJECT BASE RATES ===')
    print(f'Data: {len(edf)} tasks across {len(repos)} repos')
    print()

    # Base rate by definition kind
    print('By definition kind:')
    for kind, label in [(True, 'method'), (False, 'function')]:
        subset = edf[edf['is_method'] == kind]
        if len(subset) > 0:
            print(f'  {label:10s}: {len(subset):4d} tasks, '
                  f'recall={subset["recall"].mean():.3f}, '
                  f'hit_rate={subset["hit"].mean():.1%}')

    # Base rate by blast radius bucket
    print('\nBy blast radius:')
    buckets = [(0, 2, '0-2'), (3, 5, '3-5'), (6, 10, '6-10'),
               (11, 20, '11-20'), (21, 999, '>20')]
    for lo, hi, label in buckets:
        subset = edf[(edf['callers'] >= lo) & (edf['callers'] <= hi)]
        if len(subset) > 0:
            print(f'  {label:10s}: {len(subset):4d} tasks, '
                  f'recall={subset["recall"].mean():.3f}, '
                  f'hit_rate={subset["hit"].mean():.1%}')

    # Base rate by test density
    print('\nBy test density:')
    for lo, hi, label in [(0, 20, '<20%'), (20, 35, '20-35%'), (35, 100, '>35%')]:
        subset = edf[(edf['test_density'] >= lo) & (edf['test_density'] < hi)]
        if len(subset) > 0:
            print(f'  {label:10s}: {len(subset):4d} tasks, '
                  f'recall={subset["recall"].mean():.3f}, '
                  f'hit_rate={subset["hit"].mean():.1%}')

    # Combined: best and worst conditions
    print('\n=== REFERENCE CLASS PREDICTIONS ===')
    best = edf[(edf['is_method']) & (edf['callers'] > 5) & (edf['test_density'] > 35)]
    worst = edf[(~edf['is_method']) & (edf['callers'] <= 2) & (edf['test_density'] < 20)]

    if len(best) > 0:
        print(f'\nBest case (method, >5 callers, >35% test density):')
        print(f'  {len(best)} tasks, recall={best["recall"].mean():.3f}, hit_rate={best["hit"].mean():.1%}')

    if len(worst) > 0:
        print(f'\nWorst case (function, ≤2 callers, <20% test density):')
        print(f'  {len(worst)} tasks, recall={worst["recall"].mean():.3f}, hit_rate={worst["hit"].mean():.1%}')

    # Save as JSON for plancheck to consume
    base_rates = {
        'total_tasks': len(edf),
        'by_kind': {},
        'by_blast_radius': {},
        'by_test_density': {},
    }
    for kind, label in [(True, 'method'), (False, 'function')]:
        subset = edf[edf['is_method'] == kind]
        if len(subset) > 0:
            base_rates['by_kind'][label] = {
                'count': len(subset),
                'recall': round(subset['recall'].mean(), 3),
                'hit_rate': round(subset['hit'].mean(), 3),
            }
    for lo, hi, label in buckets:
        subset = edf[(edf['callers'] >= lo) & (edf['callers'] <= hi)]
        if len(subset) > 0:
            base_rates['by_blast_radius'][label] = {
                'count': len(subset),
                'recall': round(subset['recall'].mean(), 3),
                'hit_rate': round(subset['hit'].mean(), 3),
            }

    out_path = os.path.expanduser('~/.plancheck/base_rates.json')
    with open(out_path, 'w') as f:
        json.dump(base_rates, f, indent=2)
    print(f'\nSaved to {out_path}')


if __name__ == '__main__':
    main()
