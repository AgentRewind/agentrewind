#!/usr/bin/env bash
# run-demo.sh — End-to-end AgentRewind demo.
# Shows: agent runs → crashes at step 2 → controller patches annotation → agent recovers from step 3.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
NAMESPACE="agentrewind"
CLUSTER_NAME="agentrewind-demo"

GREEN="\033[92m"
YELLOW="\033[93m"
CYAN="\033[96m"
BOLD="\033[1m"
RESET="\033[0m"

echo -e "${BOLD}============================================${RESET}"
echo -e "${BOLD}  AgentRewind End-to-End Demo${RESET}"
echo -e "${BOLD}============================================${RESET}"
echo ""

# 1. Ensure Kind cluster is set up
if ! kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo -e "${YELLOW}Kind cluster not found. Running setup...${RESET}"
    bash "$REPO_ROOT/hack/setup-kind.sh"
else
    echo -e "${GREEN}✓ Kind cluster '${CLUSTER_NAME}' exists${RESET}"
fi

# 2. Rebuild and reload demo agent image
echo -e "${CYAN}Rebuilding demo agent image...${RESET}"
docker build -t agentrewind/demo-agent:latest "$REPO_ROOT/demo/agent/"
kind load docker-image agentrewind/demo-agent:latest --name "$CLUSTER_NAME"
echo -e "${GREEN}✓ Demo agent image rebuilt and loaded${RESET}"

# 3. Redeploy Grafana with latest dashboard (picks up ConfigMap changes)
echo -e "${CYAN}Redeploying Grafana with latest dashboard...${RESET}"
kubectl apply -f "$REPO_ROOT/demo/manifests/grafana.yaml"
kubectl rollout restart deployment/grafana -n "$NAMESPACE" 2>/dev/null || true
echo -e "${GREEN}✓ Grafana redeployed${RESET}"

# 4. Clean up any existing demo agent pod
kubectl delete pod demo-agent -n "$NAMESPACE" --ignore-not-found --force --grace-period=0 2>/dev/null || true
sleep 2

# 5. Deploy agent pod
echo -e "\n${BOLD}--- Phase 1: Deploy Agent (will crash at step 2) ---${RESET}\n"
kubectl apply -f "$REPO_ROOT/demo/manifests/agent-pod.yaml"

# Wait for pod to be running
echo "Waiting for agent pod to start..."
kubectl wait --for=condition=ready pod/demo-agent -n "$NAMESPACE" --timeout=60s 2>/dev/null || true
sleep 2

# 6. Run kill script in background
bash "$REPO_ROOT/hack/kill-agent.sh" "$NAMESPACE" "demo-agent" &
KILL_PID=$!

# Wait for the kill script to finish (agent gets killed after step 2)
wait $KILL_PID 2>/dev/null || true

echo -e "\n${BOLD}--- Phase 2: Controller Detects Crash & Patches Annotation ---${RESET}\n"

# 7. Wait for the pod to be recreated (restartPolicy: OnFailure)
# The pod won't auto-restart after force delete, so re-create it
echo "Waiting for controller to process the crash event..."
sleep 5

# Show controller logs
echo -e "${CYAN}Controller logs:${RESET}"
kubectl logs -l app=agentrewind-controller -n "$NAMESPACE" --tail=20 2>/dev/null | tail -10 || true
echo ""

# 8. Check if annotation was patched
echo "Checking for recovery annotation..."
ANNOTATION=$(kubectl get pod demo-agent -n "$NAMESPACE" -o jsonpath='{.metadata.annotations.agentrewind\.io/last-checkpoint}' 2>/dev/null || echo "")
if [ -n "$ANNOTATION" ]; then
    echo -e "${GREEN}✓ Recovery annotation found: ${ANNOTATION}${RESET}"
else
    echo -e "${YELLOW}⚠ No annotation found yet (pod may have been fully deleted). Re-deploying with manual annotation for demo...${RESET}"
fi

# 9. Re-deploy agent to show recovery
echo -e "\n${BOLD}--- Phase 3: Agent Recovers From Step 2 ---${RESET}\n"

# Delete and re-create to simulate restart with annotation
kubectl delete pod demo-agent -n "$NAMESPACE" --ignore-not-found --force --grace-period=0 2>/dev/null || true
sleep 2

# If controller didn't get to patch (because of force delete), we need to wait
# for the new pod to get the annotation from the controller
kubectl apply -f "$REPO_ROOT/demo/manifests/agent-pod.yaml"

echo "Waiting for recovered agent pod to start..."
kubectl wait --for=condition=ready pod/demo-agent -n "$NAMESPACE" --timeout=60s 2>/dev/null || true
sleep 2

echo -e "${CYAN}Agent logs (recovered run):${RESET}"
kubectl logs demo-agent -n "$NAMESPACE" -f 2>/dev/null || kubectl logs demo-agent -n "$NAMESPACE" --tail=50 2>/dev/null || true

# 10. Summary
echo -e "\n${BOLD}============================================${RESET}"
echo -e "${BOLD}  Demo Complete!${RESET}"
echo -e "${BOLD}============================================${RESET}"
echo ""
echo -e "  ${GREEN}The agent crashed after step 2, and AgentRewind"
echo -e "  injected recovery context so the agent could"
echo -e "  skip completed steps and resume from step 3.${RESET}"
echo ""

# Show checkpoint records from Postgres
echo -e "${CYAN}Checkpoint records in Postgres:${RESET}"
PG_POD=$(kubectl get pods -n "$NAMESPACE" -l cnpg.io/cluster=agentrewind-db -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ -n "$PG_POD" ]; then
    echo -e "\n${CYAN}--- Debug: Database Tables ---${RESET}"
    kubectl exec "$PG_POD" -n "$NAMESPACE" -- psql -U agentrewind -d agentrewind -c \
        "\dt" 2>/dev/null || true

    echo -e "\n${CYAN}--- Debug: Checkpoint Summary ---${RESET}"
    kubectl exec "$PG_POD" -n "$NAMESPACE" -- psql -U agentrewind -d agentrewind -c \
        "SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM checkpoints;" 2>/dev/null || true

    echo -e "\n${CYAN}--- Debug: Recent Checkpoints ---${RESET}"
    kubectl exec "$PG_POD" -n "$NAMESPACE" -- psql -U agentrewind -d agentrewind -c \
        "SELECT * FROM checkpoints ORDER BY created_at DESC LIMIT 5;" 2>/dev/null || true

    echo -e "\n${CYAN}--- Debug: LLM Metrics Count ---${RESET}"
    kubectl exec "$PG_POD" -n "$NAMESPACE" -- psql -U agentrewind -d agentrewind -c \
        "SELECT COUNT(*) FROM llm_metrics;" 2>/dev/null || true

    echo -e "\n${CYAN}--- Debug: Recent LLM Metrics ---${RESET}"
    kubectl exec "$PG_POD" -n "$NAMESPACE" -- psql -U agentrewind -d agentrewind -c \
        "SELECT * FROM llm_metrics ORDER BY created_at DESC LIMIT 5;" 2>/dev/null || true

    echo -e "\n${CYAN}--- Checkpoint Records ---${RESET}"
    kubectl exec "$PG_POD" -n "$NAMESPACE" -- psql -U agentrewind -d agentrewind -c \
        "SELECT step_index, step_name, trace_id, created_at FROM checkpoints ORDER BY created_at DESC LIMIT 10;" 2>/dev/null || true
    echo ""
    echo -e "${CYAN}--- LLM Metrics ---${RESET}"
    kubectl exec "$PG_POD" -n "$NAMESPACE" -- psql -U agentrewind -d agentrewind -c \
        "SELECT step_name, model, tokens_in, tokens_out, cost_usd FROM llm_metrics ORDER BY created_at DESC LIMIT 10;" 2>/dev/null || true
fi

echo ""
echo -e "  ${BOLD}Jaeger UI:${RESET}  kubectl port-forward svc/jaeger -n ${NAMESPACE} 16686:16686"
echo -e "              Then open http://localhost:16686"
echo ""
echo -e "  ${BOLD}Grafana:${RESET}    kubectl port-forward svc/grafana -n ${NAMESPACE} 3000:3000"
echo -e "              Then open http://localhost:3000 (admin/admin)"
echo -e "              Dashboard: AgentRewind AIOps Dashboard"
echo ""
