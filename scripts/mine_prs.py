#!/usr/bin/env python3
"""
Mine GitHub PRs for plan→outcome data.

For each repo, pulls merged PRs with descriptions and diffs.
PR description ≈ plan (intent), diff ≈ outcome (what changed), CI ≈ success/failure.

Usage:
    python3 mine_prs.py <owner/repo> [--limit N]

Requires: gh CLI authenticated
"""

import json
import os
import subprocess
import sys


def gh_api(endpoint: str) -> list | dict:
    """Call GitHub API via gh CLI."""
    result = subprocess.run(
        ['gh', 'api', endpoint, '--paginate'],
        capture_output=True, text=True, timeout=30
    )
    if result.returncode != 0:
        return []
    # gh --paginate concatenates JSON arrays
    text = result.stdout.strip()
    if text.startswith('['):
        return json.loads(text)
    return json.loads(f'[{text}]')


def mine_repo(owner_repo: str, limit: int = 50):
    """Mine merged PRs with descriptions from a GitHub repo."""
    print(f'Mining {owner_repo} (limit {limit})')

    # Get merged PRs with body text
    prs = gh_api(
        f'repos/{owner_repo}/pulls?state=closed&sort=updated&direction=desc&per_page={min(limit, 100)}'
    )

    results = []
    for pr in prs[:limit]:
        if not pr.get('merged_at'):
            continue  # not merged
        body = pr.get('body', '') or ''
        if len(body.strip()) < 20:
            continue  # no meaningful description

        title = pr.get('title', '')
        number = pr.get('number')
        files_changed = pr.get('changed_files', 0)

        # Get the diff stat
        diff_result = subprocess.run(
            ['gh', 'api', f'repos/{owner_repo}/pulls/{number}/files'],
            capture_output=True, text=True, timeout=15
        )
        files = []
        if diff_result.returncode == 0:
            file_data = json.loads(diff_result.stdout)
            files = [f['filename'] for f in file_data]

        results.append({
            'repo': owner_repo,
            'pr': number,
            'title': title,
            'body_length': len(body),
            'body_preview': body[:200],
            'files': files,
            'files_count': len(files),
        })

    print(f'  Found {len(results)} merged PRs with descriptions')

    # Stats
    if results:
        avg_body = sum(r['body_length'] for r in results) / len(results)
        avg_files = sum(r['files_count'] for r in results) / len(results)
        print(f'  Avg description length: {avg_body:.0f} chars')
        print(f'  Avg files changed: {avg_files:.1f}')

        # Show a few examples
        print(f'\n  Examples:')
        for r in results[:3]:
            print(f'    PR #{r["pr"]}: {r["title"]}')
            print(f'      {r["files_count"]} files, {r["body_length"]} char description')
            print(f'      Preview: {r["body_preview"][:80]}...')
            print()

    # Save
    out_dir = os.path.expanduser('~/.plancheck/datasets/pr-data')
    os.makedirs(out_dir, exist_ok=True)
    out_file = os.path.join(out_dir, f'{owner_repo.replace("/", "__")}.json')
    with open(out_file, 'w') as f:
        json.dump(results, f, indent=2)
    print(f'  Saved to {out_file}')

    return results


def main():
    repos = [
        'gin-gonic/gin',
        'spf13/cobra',
        'labstack/echo',
        'go-gorm/gorm',
    ]

    if len(sys.argv) > 1:
        repos = [sys.argv[1]]

    limit = 30
    if '--limit' in sys.argv:
        limit = int(sys.argv[sys.argv.index('--limit') + 1])

    all_results = []
    for repo in repos:
        try:
            results = mine_repo(repo, limit)
            all_results.extend(results)
        except Exception as e:
            print(f'  Error: {e}')
        print()

    if all_results:
        print(f'Total: {len(all_results)} PRs across {len(repos)} repos')


if __name__ == '__main__':
    main()
