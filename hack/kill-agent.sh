#!/usr/bin/env bash
# kill-agent.sh — Watches agent pod logs and kills it after step 2 completes.

set -euo pipefail

NAMESPACE="${1:-agentrewind}"
POD_NAME="${2:-demo-agent}"

echo "=== Kill Script ==="
echo "Watching pod '${POD_NAME}' in namespace '${NAMESPACE}' for step 2 completion..."

# Stream logs and watch for step 2 completion
kubectl logs -f "$POD_NAME" -n "$NAMESPACE" 2>/dev/null | while IFS= read -r line; do
    echo "  [agent] $line"
    if echo "$line" | grep -q "Step 1 complete"; then
        echo ""
        echo ">>> Step 1 detected — waiting for step 2..."
    fi
    if echo "$line" | grep -q "Step 2 complete"; then
        echo ""
        echo ">>> Step 2 completed! Waiting 1 second then killing pod..."
        sleep 1
        kubectl delete pod "$POD_NAME" -n "$NAMESPACE" --force --grace-period=0 2>/dev/null &
        echo ">>> Pod '${POD_NAME}' force-deleted (simulating crash)"
        exit 0
    fi
done
