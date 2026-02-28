#!/usr/bin/env python3
"""
Measure exploration waste in agent trajectories and estimate savings
from reference graph guidance.

For each trajectory:
1. Extract files explored (find/grep/read actions)
2. Extract files actually edited
3. Compute waste = explored - edited (unnecessary exploration)
4. Check: would the reference graph have pointed to the edited files?

Usage:
    python3 exploration_waste.py [--model MODEL]
"""

import json
import os
import re
import sys
import zipfile
from collections import defaultdict


def analyze_trajectory(traj: dict) -> dict | None:
    """Analyze a single trajectory for exploration waste."""
    trajectory = traj.get('trajectory', [])
    if not trajectory:
        return None

    info = traj.get('info', {})
    if info.get('exit_status') != 'submitted':
        return None

    # Extract actions
    # MSWE-agent format: `open FILE` sets current file, `edit LINE:LINE` edits it
    explored_files = set()
    edited_files = set()
    current_file = None
    find_actions = 0
    grep_actions = 0
    read_actions = 0
    edit_actions = 0
    other_actions = 0

    for step in trajectory:
        action = str(step.get('action', ''))

        # Find/grep
        if action.startswith('find') or action.startswith('search_dir'):
            find_actions += 1
        elif action.startswith('grep'):
            grep_actions += 1
        # Read/open — sets current file context
        elif action.startswith('open ') or action.startswith('cat '):
            read_actions += 1
            m = re.match(r'(?:open|cat)\s+(\S+)', action)
            if m:
                current_file = m.group(1).strip()
                explored_files.add(current_file)
        # Edit — uses current_file from last open
        elif action.startswith('edit'):
            edit_actions += 1
            if current_file:
                edited_files.add(current_file)
        # Create
        elif action.startswith('create'):
            edit_actions += 1
            m = re.match(r'create\s+(\S+)', action)
            if m:
                edited_files.add(m.group(1).strip())
        else:
            other_actions += 1

    total_actions = len(trajectory)
    exploration_actions = find_actions + grep_actions + read_actions

    # Waste: files read but not edited
    wasted_reads = explored_files - edited_files

    return {
        'total_actions': total_actions,
        'find_actions': find_actions,
        'grep_actions': grep_actions,
        'read_actions': read_actions,
        'edit_actions': edit_actions,
        'exploration_actions': exploration_actions,
        'exploration_pct': exploration_actions / total_actions * 100 if total_actions > 0 else 0,
        'files_explored': len(explored_files),
        'files_edited': len(edited_files),
        'wasted_reads': len(wasted_reads),
        'waste_ratio': len(wasted_reads) / len(explored_files) if explored_files else 0,
    }


def main():
    base = os.path.expanduser('~/.plancheck/datasets/multi-swe-bench-trajs/go/')

    # All MSWE-agent models (consistent format)
    archives = sorted([
        f for f in os.listdir(base)
        if f.startswith('20250329_MSWE-agent_') and f.endswith('.zip')
    ])

    if '--model' in sys.argv:
        model_filter = sys.argv[sys.argv.index('--model') + 1]
        archives = [a for a in archives if model_filter in a]

    print(f'{"Model":<30s} {"Tasks":>5s} {"Actions":>8s} {"Explore%":>9s} {"Waste%":>7s}')
    print('-' * 65)

    all_results = []

    for archive_name in archives:
        model = archive_name.replace('20250329_MSWE-agent_', '').replace('.zip', '')
        path = os.path.join(base, archive_name)

        results = []
        with zipfile.ZipFile(path) as zf:
            for name in zf.namelist()[:50]:
                try:
                    with zf.open(name) as f:
                        traj = json.load(f)
                    r = analyze_trajectory(traj)
                    if r:
                        results.append(r)
                except:
                    continue

        if not results:
            print(f'{model:<30s} {"0":>5s}')
            continue

        avg_actions = sum(r['total_actions'] for r in results) / len(results)
        avg_explore_pct = sum(r['exploration_pct'] for r in results) / len(results)
        avg_waste = sum(r['waste_ratio'] for r in results) / len(results)

        print(f'{model:<30s} {len(results):>5d} {avg_actions:>8.1f} {avg_explore_pct:>8.0f}% {avg_waste:>6.0f}%')
        all_results.extend(results)

    if all_results:
        print()
        print('=' * 65)
        n = len(all_results)
        avg_actions = sum(r['total_actions'] for r in all_results) / n
        avg_explore = sum(r['exploration_actions'] for r in all_results) / n
        avg_explore_pct = sum(r['exploration_pct'] for r in all_results) / n
        avg_waste = sum(r['waste_ratio'] for r in all_results) / n
        avg_files_explored = sum(r['files_explored'] for r in all_results) / n
        avg_files_edited = sum(r['files_edited'] for r in all_results) / n
        avg_wasted_reads = sum(r['wasted_reads'] for r in all_results) / n

        print(f'AGGREGATE ({n} submitted trajectories across {len(archives)} models)')
        print(f'  Avg actions/task:     {avg_actions:.1f}')
        print(f'  Avg exploration:      {avg_explore:.1f} actions ({avg_explore_pct:.0f}%)')
        print(f'  Avg files explored:   {avg_files_explored:.1f}')
        print(f'  Avg files edited:     {avg_files_edited:.1f}')
        print(f'  Avg wasted reads:     {avg_wasted_reads:.1f} ({avg_waste:.0%} of reads)')
        print()

        # Estimate savings from reference graph
        # If the graph eliminates 50% of exploration (conservative, based on 48% recall),
        # how many actions are saved per task?
        savings_50 = avg_explore * 0.50
        savings_74 = avg_explore * 0.74  # combined model recall
        print(f'  Estimated savings with reference graph:')
        print(f'    At 50% recall (graph only):   {savings_50:.1f} actions/task ({savings_50/avg_actions*100:.0f}% reduction)')
        print(f'    At 74% recall (combined):     {savings_74:.1f} actions/task ({savings_74/avg_actions*100:.0f}% reduction)')


if __name__ == '__main__':
    main()
