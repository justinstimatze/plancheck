#!/bin/bash
# Prototype: Dolt branch simulation on a defn-initialized repo
# Usage: ./simulate.sh <repo-path> <mutation-type> <definition-name> [receiver]
#
# Mutation types:
#   signature-change  - Function signature changes (breaks all callers)
#   behavior-change   - Internal logic changes (tests may break)
#   removal           - Definition removed (everything breaks)
#   addition          - New definition added (creates new edges)
#
# Example:
#   ./simulate.sh ~/.plancheck/datasets/repos/gin signature-change Render '*Context'

set -e

REPO_PATH="${1:?Usage: simulate.sh <repo-path> <mutation-type> <def-name> [receiver]}"
MUTATION="${2:?Mutation type required: signature-change|behavior-change|removal|addition}"
DEF_NAME="${3:?Definition name required}"
RECEIVER="${4:-}"

DEFN_DIR="$REPO_PATH/.defn"
if [ ! -d "$DEFN_DIR" ]; then
    echo "Error: no .defn/ directory in $REPO_PATH"
    exit 1
fi

cd "$DEFN_DIR"

# Find the target definition
if [ -n "$RECEIVER" ]; then
    WHERE_CLAUSE="name = '$DEF_NAME' AND receiver = '$RECEIVER'"
    DISPLAY="($RECEIVER).$DEF_NAME"
else
    WHERE_CLAUSE="name = '$DEF_NAME' AND receiver = ''"
    DISPLAY="$DEF_NAME"
fi

DEF_ID=$(dolt sql -q "SELECT id FROM definitions WHERE $WHERE_CLAUSE AND test = FALSE LIMIT 1" -r json 2>/dev/null | python3 -c "import json,sys; rows=json.load(sys.stdin)['rows']; print(rows[0]['id'])" 2>/dev/null)

if [ -z "$DEF_ID" ]; then
    echo "Error: definition '$DISPLAY' not found"
    exit 1
fi

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  RIPPLE REPORT: $MUTATION on $DISPLAY"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

# Direct callers
echo "─── Direct callers ───"
dolt sql -q "
SELECT d.name, d.receiver, d.kind, d.test, r.kind as ref_kind
FROM definitions d
JOIN \`references\` r ON r.from_def = d.id
WHERE r.to_def = $DEF_ID
ORDER BY d.test, d.name
" -r json 2>/dev/null | python3 -c "
import json, sys
data = json.load(sys.stdin)['rows']
prod = [d for d in data if not d['test']]
test = [d for d in data if d['test']]
print(f'  Production: {len(prod)}')
for d in prod:
    recv = f'({d[\"receiver\"]}).' if d['receiver'] else ''
    print(f'    {recv}{d[\"name\"]} [{d[\"ref_kind\"]}]')
print(f'  Test: {len(test)}')
"

# Transitive callers (depth 2)
echo ""
echo "─── Transitive callers (depth 2, production only) ───"
dolt sql -q "
WITH direct AS (
  SELECT d.id, d.name, d.receiver
  FROM definitions d
  JOIN \`references\` r ON r.from_def = d.id
  WHERE r.to_def = $DEF_ID AND d.test = FALSE
)
SELECT DISTINCT d2.name, d2.receiver, d2.kind
FROM definitions d2
JOIN \`references\` r2 ON r2.from_def = d2.id
JOIN direct ON r2.to_def = direct.id
WHERE d2.test = FALSE AND d2.id NOT IN (SELECT id FROM direct) AND d2.id != $DEF_ID
ORDER BY d2.name
" -r json 2>/dev/null | python3 -c "
import json, sys
data = json.load(sys.stdin)['rows']
print(f'  Count: {len(data)}')
for d in data[:15]:
    recv = f'({d[\"receiver\"]}).' if d['receiver'] else ''
    print(f'    {recv}{d[\"name\"]}')
if len(data) > 15:
    print(f'    ... and {len(data)-15} more')
"

# Test coverage
echo ""
echo "─── Test coverage ───"
dolt sql -q "
WITH RECURSIVE tc AS (
  SELECT d.id, d.name, d.test, 0 as depth
  FROM definitions d
  JOIN \`references\` r ON r.from_def = d.id
  WHERE r.to_def = $DEF_ID
  UNION
  SELECT d2.id, d2.name, d2.test, tc.depth + 1
  FROM definitions d2
  JOIN \`references\` r2 ON r2.from_def = d2.id
  JOIN tc ON r2.to_def = tc.id
  WHERE tc.depth < 3
)
SELECT
  COUNT(DISTINCT CASE WHEN test = TRUE THEN id END) as test_count,
  COUNT(DISTINCT CASE WHEN test = FALSE THEN id END) as prod_count
FROM tc
" -r json 2>/dev/null | python3 -c "
import json, sys
d = json.load(sys.stdin)['rows'][0]
print(f'  Tests covering (transitively, depth 3): {d[\"test_count\"]}')
print(f'  Production definitions in blast radius: {d[\"prod_count\"]}')
"

# Mutation-specific analysis
echo ""
case "$MUTATION" in
    signature-change)
        echo "─── Signature change impact ───"
        echo "  ALL direct callers will break (need signature update)"
        echo "  Transitive callers may break if they pass through modified params"
        ;;
    behavior-change)
        echo "─── Behavior change impact ───"
        echo "  Callers won't break syntactically"
        echo "  Tests may fail if they assert on output"
        ;;
    removal)
        echo "─── Removal impact ───"
        echo "  ALL direct callers will fail to compile"
        echo "  EVERY reference is a mandatory fix"
        ;;
    addition)
        echo "─── Addition impact ───"
        echo "  No existing code breaks"
        echo "  New test coverage needed"
        ;;
esac

echo ""
echo "════════════════════════════════════════════════════════════════"
