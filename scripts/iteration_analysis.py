#!/usr/bin/env python3
"""
Analyze: does blast radius predict task difficulty?

If high-blast-radius changes are harder, then:
1. Tasks with high blast radius should have more failing tests
2. The combined model should be more valuable on high-blast tasks
3. More verification rounds should be needed for high-blast plans

This validates the Kelly criterion application: proportional effort
based on blast radius.

Usage:
    python3 iteration_analysis.py [repo-name]
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
    repos = {
        'gin': 'gin-gonic__gin',
        'cobra': 'spf13__cobra',
        'echo': 'labstack__echo',
        'roaring': 'RoaringBitmap__roaring',
        'gorm': 'go-gorm__gorm',
    }

    target = sys.argv[1] if len(sys.argv) > 1 else None
    if target:
        repos = {target: repos.get(target, target)}

    df = pd.read_parquet(os.path.expanduser(
        '~/.plancheck/datasets/swe-smith-go/data/train-00000-of-00001.parquet'))

    all_low = []  # blast radius ≤ 5
    all_high = [] # blast radius > 5

    for repo_name, pattern in repos.items():
        tasks = df[df['repo'].str.contains(pattern)]
        repo_path = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_name}')

        for _, t in tasks.head(100).iterrows():
            # Extract function from patch
            for line in t['patch'].split('\n'):
                m = re.match(r'^@@ .+? @@\s*(.*)', line)
                if not m:
                    continue
                ctx = m.group(1).strip()
                m2 = re.match(r'func\s+(?:\(\w+\s+\*?\w+\)\s+)?(\w+)', ctx)
                if not m2:
                    continue
                name = m2.group(1)

                # Get blast radius
                rows = defn_query(repo_path,
                    f"SELECT (SELECT COUNT(*) FROM `references` r WHERE r.to_def = d.id) as callers "
                    f"FROM definitions d WHERE d.name = '{name}' AND d.test = FALSE LIMIT 1")
                if not rows:
                    break
                callers = int(rows[0]['callers'])

                # Count failing tests
                fail_count = len(re.findall(r"'Test\w+'", str(t['FAIL_TO_PASS'])))

                entry = {
                    'repo': repo_name,
                    'name': name,
                    'callers': callers,
                    'failing_tests': fail_count,
                }

                if callers <= 5:
                    all_low.append(entry)
                else:
                    all_high.append(entry)
                break

    if not all_low and not all_high:
        print('No data')
        return

    print(f'=== Blast Radius vs Task Difficulty ===')
    print(f'Analyzed: {len(all_low) + len(all_high)} tasks across {len(repos)} repos')
    print()

    if all_low:
        avg_fail_low = sum(e['failing_tests'] for e in all_low) / len(all_low)
        avg_callers_low = sum(e['callers'] for e in all_low) / len(all_low)
        print(f'Low blast (≤5 callers): {len(all_low)} tasks')
        print(f'  Avg callers: {avg_callers_low:.1f}')
        print(f'  Avg failing tests: {avg_fail_low:.1f}')

    if all_high:
        avg_fail_high = sum(e['failing_tests'] for e in all_high) / len(all_high)
        avg_callers_high = sum(e['callers'] for e in all_high) / len(all_high)
        print(f'High blast (>5 callers): {len(all_high)} tasks')
        print(f'  Avg callers: {avg_callers_high:.1f}')
        print(f'  Avg failing tests: {avg_fail_high:.1f}')

    if all_low and all_high:
        ratio = avg_fail_high / avg_fail_low if avg_fail_low > 0 else float('inf')
        print()
        print(f'High-blast tasks have {ratio:.1f}x more failing tests')
        print()
        if ratio > 1.5:
            print('VALIDATES Kelly criterion: high-blast changes are harder,')
            print('so proportional effort (more verification rounds) is justified.')
        else:
            print('DOES NOT strongly validate Kelly: blast radius does not')
            print('predict significantly more failing tests.')

    # Bucket analysis
    print()
    print('=== Bucketed Analysis ===')
    all_entries = all_low + all_high
    buckets = [
        ('0 callers', lambda e: e['callers'] == 0),
        ('1-2 callers', lambda e: 1 <= e['callers'] <= 2),
        ('3-5 callers', lambda e: 3 <= e['callers'] <= 5),
        ('6-10 callers', lambda e: 6 <= e['callers'] <= 10),
        ('11-20 callers', lambda e: 11 <= e['callers'] <= 20),
        ('>20 callers', lambda e: e['callers'] > 20),
    ]
    for label, pred in buckets:
        bucket = [e for e in all_entries if pred(e)]
        if bucket:
            avg_fail = sum(e['failing_tests'] for e in bucket) / len(bucket)
            print(f'  {label:15s} {len(bucket):4d} tasks, avg {avg_fail:5.1f} failing tests')


if __name__ == '__main__':
    main()
