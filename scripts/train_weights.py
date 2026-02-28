#!/usr/bin/env python3
"""
Actually train the prediction market weights and compare against grep.

If this doesn't beat grep, we've been building an expensive linter.

1. For each SWE-smith-go task, compute structural + comod signals
2. Grid search for optimal weights on train split
3. Test on held-out split
4. Compare: optimal weights vs default vs naive union vs grep
"""

import json
import os
import random
import re
import subprocess
import sys
from collections import defaultdict

import pandas as pd


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


def get_structural_tests(repo_path, name):
    """Reference graph: test names that transitively call this definition."""
    rows = defn_query(repo_path,
        f"WITH RECURSIVE tc AS ("
        f"SELECT d.id, d.name, d.test, 0 as dep FROM definitions d "
        f"JOIN `references` r ON r.from_def = d.id "
        f"JOIN definitions target ON r.to_def = target.id "
        f"WHERE target.name = '{name}' AND target.test = FALSE "
        f"UNION SELECT d2.id, d2.name, d2.test, tc.dep+1 "
        f"FROM definitions d2 JOIN `references` r2 ON r2.from_def = d2.id "
        f"JOIN tc ON r2.to_def = tc.id WHERE tc.dep < 4"
        f") SELECT DISTINCT name FROM tc WHERE test = TRUE")
    return {r['name'] for r in rows}


def get_comod_tests(repo_path, changed_file):
    """Git comod: test files that historically co-change."""
    try:
        result = subprocess.run(
            ['git', '-C', repo_path, 'log', '--diff-filter=M',
             '--name-only', '--pretty=format:COMMIT', '-n', '200'],
            capture_output=True, text=True, timeout=10)
        if result.returncode != 0:
            return {}
    except:
        return {}

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
        return {}

    co_counts = {}
    for commit in relevant:
        for f in commit:
            if f == changed_file or '_test.go' not in f:
                continue
            co_counts[f] = co_counts.get(f, 0) + 1

    # Return test names with frequencies
    test_names = {}
    for tf, count in co_counts.items():
        freq = count / len(relevant)
        if freq < 0.2:
            continue
        full_path = os.path.join(repo_path, tf)
        if os.path.exists(full_path):
            try:
                with open(full_path) as fh:
                    for line in fh:
                        m = re.match(r'func\s+(Test\w+)', line)
                        if m:
                            test_names[m.group(1)] = freq
            except:
                pass
    return test_names


def get_grep_tests(repo_path, name):
    """Simple grep: find test files that mention this name."""
    try:
        result = subprocess.run(
            ['grep', '-rl', name, repo_path, '--include=*_test.go'],
            capture_output=True, text=True, timeout=10)
        if result.returncode != 0:
            return set()
    except:
        return set()

    test_names = set()
    for filepath in result.stdout.strip().split('\n'):
        if not filepath:
            continue
        try:
            with open(filepath) as f:
                for line in f:
                    m = re.match(r'func\s+(Test\w+)', line)
                    if m:
                        test_names.add(m.group(1))
        except:
            pass
    return test_names


def weighted_combine(struct_tests, comod_tests, actual_tests, w_struct, w_comod):
    """Combine signals with weights and compute recall."""
    # Each test gets a score
    all_tests = struct_tests | set(comod_tests.keys())
    if not all_tests or not actual_tests:
        return 0, 0

    predicted = set()
    threshold = 0.3  # predict if weighted score > threshold

    for test in all_tests:
        s_score = 1.0 if test in struct_tests else 0.0
        c_score = comod_tests.get(test, 0.0)
        combined = w_struct * s_score + w_comod * c_score
        total_w = w_struct + w_comod
        if total_w > 0:
            combined /= total_w
        if combined > threshold:
            predicted.add(test)

    hits = predicted & actual_tests
    recall = len(hits) / len(actual_tests) if actual_tests else 0
    hit = 1 if hits else 0
    return recall, hit


def main():
    random.seed(42)

    df = pd.read_parquet(os.path.expanduser(
        '~/.plancheck/datasets/swe-smith-go/data/train-00000-of-00001.parquet'))

    repos = {
        'gin': 'gin-gonic__gin',
        'cobra': 'spf13__cobra',
        'echo': 'labstack__echo',
        'roaring': 'RoaringBitmap__roaring',
        'gorm': 'go-gorm__gorm',
    }

    # Collect all task data
    print('Collecting signals for all tasks...')
    task_data = []

    for repo_name, pattern in repos.items():
        tasks = df[df['repo'].str.contains(pattern)]
        repo_path = os.path.expanduser(f'~/.plancheck/datasets/repos/{repo_name}')

        for idx, (_, t) in enumerate(tasks.iterrows()):
            if idx >= 100:  # limit per repo for speed
                break

            for line in t['patch'].split('\n'):
                m = re.match(r'^@@ .+? @@\s*(.*)', line)
                if not m:
                    continue
                ctx = m.group(1).strip()
                m2 = re.match(r'func\s+(?:\(\w+\s+\*?\w+\)\s+)?(\w+)', ctx)
                if not m2:
                    continue
                name = m2.group(1)

                actual = set(re.findall(r"'(Test\w+)'", str(t['FAIL_TO_PASS'])))
                if not actual:
                    break

                file_match = re.search(r'diff --git a/(.*?) b/', t['patch'])
                changed_file = file_match.group(1) if file_match else ''

                struct_tests = get_structural_tests(repo_path, name)
                comod_tests = get_comod_tests(repo_path, changed_file)
                grep_tests = get_grep_tests(repo_path, name)

                task_data.append({
                    'repo': repo_name,
                    'name': name,
                    'actual': actual,
                    'struct_tests': struct_tests,
                    'comod_tests': comod_tests,
                    'grep_tests': grep_tests,
                })
                break

        print(f'  {repo_name}: {sum(1 for t in task_data if t["repo"] == repo_name)} tasks')

    print(f'\nTotal: {len(task_data)} tasks')

    # Split train/test
    random.shuffle(task_data)
    split = len(task_data) // 2
    train = task_data[:split]
    test = task_data[split:]

    print(f'Train: {len(train)}, Test: {len(test)}')
    print()

    # Grid search on train set
    print('=== GRID SEARCH FOR OPTIMAL WEIGHTS (train set) ===')
    best_weights = (0.5, 0.5)
    best_recall = 0

    for w_s in [0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9]:
        w_c = 1.0 - w_s
        recalls = []
        for t in train:
            r, _ = weighted_combine(t['struct_tests'], t['comod_tests'],
                                   t['actual'], w_s, w_c)
            recalls.append(r)
        avg = sum(recalls) / len(recalls)
        if avg > best_recall:
            best_recall = avg
            best_weights = (w_s, w_c)

    print(f'Best weights: structural={best_weights[0]:.1f}, comod={best_weights[1]:.1f}')
    print(f'Best train recall: {best_recall:.3f}')
    print()

    # Evaluate ALL methods on test set
    print('=== EVALUATION ON HELD-OUT TEST SET ===\n')

    methods = {
        'grep': lambda t: (
            len(t['grep_tests'] & t['actual']) / len(t['actual']) if t['actual'] else 0,
            1 if t['grep_tests'] & t['actual'] else 0
        ),
        'struct_only': lambda t: (
            len(t['struct_tests'] & t['actual']) / len(t['actual']) if t['actual'] else 0,
            1 if t['struct_tests'] & t['actual'] else 0
        ),
        'comod_only': lambda t: (
            len(set(t['comod_tests'].keys()) & t['actual']) / len(t['actual']) if t['actual'] else 0,
            1 if set(t['comod_tests'].keys()) & t['actual'] else 0
        ),
        'naive_union': lambda t: (
            len((t['struct_tests'] | set(t['comod_tests'].keys())) & t['actual']) / len(t['actual']) if t['actual'] else 0,
            1 if (t['struct_tests'] | set(t['comod_tests'].keys())) & t['actual'] else 0
        ),
        'optimal_weights': lambda t: weighted_combine(
            t['struct_tests'], t['comod_tests'], t['actual'],
            best_weights[0], best_weights[1]
        ),
        'default_weights': lambda t: weighted_combine(
            t['struct_tests'], t['comod_tests'], t['actual'], 0.5, 0.35
        ),
    }

    print(f'{"Method":<20s} {"Recall":>8s} {"Hit Rate":>10s}')
    print('-' * 40)

    for method_name, method_fn in methods.items():
        recalls = []
        hits = []
        for t in test:
            r, h = method_fn(t)
            recalls.append(r)
            hits.append(h)
        avg_recall = sum(recalls) / len(recalls)
        hit_rate = sum(hits) / len(hits)
        marker = ' ←' if method_name in ('optimal_weights', 'grep') else ''
        print(f'{method_name:<20s} {avg_recall:>8.3f} {hit_rate:>9.1%}{marker}')

    # The brutal question
    print()
    grep_recall = sum(
        len(t['grep_tests'] & t['actual']) / len(t['actual']) if t['actual'] else 0
        for t in test
    ) / len(test)
    opt_recall = sum(
        weighted_combine(t['struct_tests'], t['comod_tests'], t['actual'],
                        best_weights[0], best_weights[1])[0]
        for t in test
    ) / len(test)

    if opt_recall > grep_recall:
        print(f'PREDICTION MARKET BEATS GREP: +{(opt_recall - grep_recall):.3f} recall')
    elif opt_recall < grep_recall - 0.01:
        print(f'GREP WINS. We have been huffing our own farts.')
    else:
        print(f'ROUGHLY EQUAL. The prediction market adds marginal value.')


if __name__ == '__main__':
    main()
