#!/usr/bin/env bash
# Make CI's `test` job a required status check on main.
#
# Without this, `gh pr merge --auto` has nothing to wait for and merges
# immediately — the Dependabot auto-merge workflow would land PRs before CI
# reports. Run once.
#
# enforce_admins stays false, so direct pushes to main by the repo owner keep
# working exactly as before; this only gates merges through pull requests.
set -euo pipefail

REPO="${1:-justinstimatze/plancheck}"

echo "Setting required status check 'test' on $REPO main..."
gh api -X PUT "repos/$REPO/branches/main/protection" \
  --input - <<'JSON'
{
  "required_status_checks": { "strict": false, "contexts": ["test"] },
  "enforce_admins": false,
  "required_pull_request_reviews": null,
  "restrictions": null
}
JSON

echo
echo "Now required:"
gh api "repos/$REPO/branches/main/protection/required_status_checks" \
  --jq '"  contexts: \(.contexts | join(", "))  strict: \(.strict)"'
