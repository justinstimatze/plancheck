#!/usr/bin/env python3
"""Test the MC forecast with real historical data."""

import json
import os
import random
import statistics

random.seed(42)

# Load historical outcomes
with open(os.path.expanduser('~/.plancheck/forecast_history.json')) as f:
    history = json.load(f)

def mc_forecast(props, history, n_sim=10000):
    """Pure Python MC forecast matching the Go implementation."""
    # Find similar historical plans
    similar = []
    for h in history:
        complexity_close = abs(h['complexity'] - props['complexity']) <= 3 or \
            (props['complexity'] > 0 and abs(h['complexity'] - props['complexity']) / props['complexity'] < 0.5)
        density_close = abs(h['test_density'] - props['test_density']) < 0.20

        if complexity_close and density_close:
            similar.append(h)

    if len(similar) < 3:
        similar = history  # fallback

    # Sample
    recalls = []
    clean, rework, failed = 0, 0, 0
    for _ in range(n_sim):
        sample = random.choice(similar)
        recalls.append(sample['recall'])
        if sample['recall'] >= 0.8:
            clean += 1
        elif sample['recall'] >= 0.4:
            rework += 1
        else:
            failed += 1

    recalls.sort()
    p50 = recalls[len(recalls) // 2]
    p85 = recalls[int(len(recalls) * 0.85)]
    p95 = recalls[int(len(recalls) * 0.95)]

    return {
        'matching': len(similar),
        'mean_recall': statistics.mean(recalls),
        'p50': p50, 'p85': p85, 'p95': p95,
        'p_clean': clean / n_sim,
        'p_rework': rework / n_sim,
        'p_failed': failed / n_sim,
    }


# Scenario 1: Easy plan on well-tested repo
print("=== Scenario 1: Simple change on well-tested repo ===")
print("   (e.g., fix a bug in gin's context.go)")
f1 = mc_forecast({
    'complexity': 2,
    'test_density': 0.56,
    'blast_radius': 5,
}, history)
print(f"   Matching historical: {f1['matching']}")
print(f"   P(clean): {f1['p_clean']:.0%}  P(rework): {f1['p_rework']:.0%}  P(failed): {f1['p_failed']:.0%}")
print(f"   Recall: mean={f1['mean_recall']:.2f}, P50={f1['p50']:.2f}, P85={f1['p85']:.2f}")
print()

# Scenario 2: Medium plan on moderately-tested repo
print("=== Scenario 2: Feature addition on moderate repo ===")
print("   (e.g., add new command to cobra)")
f2 = mc_forecast({
    'complexity': 8,
    'test_density': 0.36,
    'blast_radius': 15,
}, history)
print(f"   Matching historical: {f2['matching']}")
print(f"   P(clean): {f2['p_clean']:.0%}  P(rework): {f2['p_rework']:.0%}  P(failed): {f2['p_failed']:.0%}")
print(f"   Recall: mean={f2['mean_recall']:.2f}, P50={f2['p50']:.2f}, P85={f2['p85']:.2f}")
print()

# Scenario 3: Hard plan on poorly-tested repo
print("=== Scenario 3: Refactor on poorly-tested repo ===")
print("   (e.g., major change in caddy's internals)")
f3 = mc_forecast({
    'complexity': 20,
    'test_density': 0.16,
    'blast_radius': 50,
}, history)
print(f"   Matching historical: {f3['matching']}")
print(f"   P(clean): {f3['p_clean']:.0%}  P(rework): {f3['p_rework']:.0%}  P(failed): {f3['p_failed']:.0%}")
print(f"   Recall: mean={f3['mean_recall']:.2f}, P50={f3['p50']:.2f}, P85={f3['p85']:.2f}")
print()

# Scenario 4: Greenfield (no history, new project)
print("=== Scenario 4: Greenfield project (no project history) ===")
print("   Falls back to cross-project base rates")
f4 = mc_forecast({
    'complexity': 5,
    'test_density': 0.0,  # no tests yet
    'blast_radius': 0,
}, history)
print(f"   Matching historical: {f4['matching']}")
print(f"   P(clean): {f4['p_clean']:.0%}  P(rework): {f4['p_rework']:.0%}  P(failed): {f4['p_failed']:.0%}")
print(f"   Recall: mean={f4['mean_recall']:.2f}, P50={f4['p50']:.2f}, P85={f4['p85']:.2f}")
print()

print("=== INTERPRETATION ===")
print("The MC forecast tells you: based on similar plans in the past,")
print("here's the probability distribution of outcomes.")
print()
print("Unlike learned weights, this can't overfit — it just reports")
print("what actually happened. Unlike thresholds, it gives you a")
print("distribution, not a binary answer.")
