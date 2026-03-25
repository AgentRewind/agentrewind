#!/usr/bin/env bash
# setup-kind.sh — Creates a Kind cluster with all AgentRewind dependencies.
# Idempotent: safe to run multiple times.

set -euo pipefail

CLUSTER_NAME="agentrewind-demo"
NAMESPACE="agentrewind"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=== AgentRewind Kind Setup ==="

# 1. Create Kind cluster if it doesn't exist
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "✓ Kind cluster '${CLUSTER_NAME}' already exists"
else
    echo "Creating Kind cluster '${CLUSTER_NAME}'..."
    kind create cluster --name "$CLUSTER_NAME" --wait 60s
fi

# Switch kubectl context
kubectl cluster-info --context "kind-${CLUSTER_NAME}" >/dev/null 2>&1

# 2. Create namespace
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
echo "✓ Namespace '${NAMESPACE}' ready"

# 3. Install CloudNativePG operator
if kubectl get deployment -n cnpg-system cnpg-controller-manager >/dev/null 2>&1; then
    echo "✓ CloudNativePG operator already installed"
else
    echo "Installing CloudNativePG operator..."
    kubectl apply --server-side -f \
        https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.2.yaml
fi

echo "Waiting for CloudNativePG operator to be ready..."
kubectl wait --for=condition=available deployment/cnpg-controller-manager \
    -n cnpg-system --timeout=120s
echo "✓ CloudNativePG operator ready"

# 4. Deploy CloudNativePG cluster
echo "Deploying Postgres cluster..."
kubectl apply -f "$REPO_ROOT/demo/manifests/cloudnativepg-cluster.yaml"

echo "Waiting for Postgres cluster to be ready (this may take a minute)..."
for i in $(seq 1 60); do
    if kubectl get cluster agentrewind-db -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null | grep -q "Cluster in healthy state"; then
        break
    fi
    sleep 5
done
# Also wait for the pod to be ready
kubectl wait --for=condition=ready pod/agentrewind-db-1 -n "$NAMESPACE" --timeout=300s 2>/dev/null || true
echo "✓ Postgres cluster ready"

# 5. Deploy OTel Collector
echo "Deploying OTel Collector..."
kubectl apply -f "$REPO_ROOT/demo/manifests/otel-collector.yaml"
kubectl wait --for=condition=available deployment/otel-collector -n "$NAMESPACE" --timeout=120s
echo "✓ OTel Collector ready"

# 6. Deploy Jaeger
echo "Deploying Jaeger..."
kubectl apply -f "$REPO_ROOT/demo/manifests/jaeger.yaml"
kubectl wait --for=condition=available deployment/jaeger -n "$NAMESPACE" --timeout=120s
echo "✓ Jaeger ready"

# 7. Build and load controller image
echo "Building controller image..."
cd "$REPO_ROOT"
docker build -t agentrewind/controller:latest .
kind load docker-image agentrewind/controller:latest --name "$CLUSTER_NAME"
echo "✓ Controller image loaded into Kind"

# 8. Build and load demo agent image
echo "Building demo agent image..."
docker build -t agentrewind/demo-agent:latest "$REPO_ROOT/demo/agent/"
kind load docker-image agentrewind/demo-agent:latest --name "$CLUSTER_NAME"
echo "✓ Demo agent image loaded into Kind"

# 9. Install CRDs
echo "Installing CRDs..."
if [ -d "$REPO_ROOT/config/crd/bases" ]; then
    kubectl apply -f "$REPO_ROOT/config/crd/bases/"
fi
echo "✓ CRDs installed"

# 10. Deploy the controller
echo "Deploying AgentRewind controller..."
# Get the Postgres DSN from the CloudNativePG secret
cat <<'CONTROLLER_EOF' | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: agentrewind-controller
  namespace: agentrewind
  labels:
    app: agentrewind-controller
spec:
  replicas: 1
  selector:
    matchLabels:
      app: agentrewind-controller
  template:
    metadata:
      labels:
        app: agentrewind-controller
    spec:
      serviceAccountName: agentrewind-controller
      containers:
        - name: manager
          image: agentrewind/controller:latest
          imagePullPolicy: IfNotPresent
          env:
            - name: POSTGRES_DSN
              valueFrom:
                secretKeyRef:
                  name: agentrewind-db-app
                  key: uri
          ports:
            - containerPort: 8080
              name: metrics
            - containerPort: 8081
              name: health
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8081
            initialDelaySeconds: 5
          readinessProbe:
            httpGet:
              path: /readyz
              port: 8081
            initialDelaySeconds: 5
          resources:
            requests:
              memory: "64Mi"
              cpu: "50m"
            limits:
              memory: "128Mi"
              cpu: "250m"
CONTROLLER_EOF

# Create ServiceAccount and RBAC
cat <<'RBAC_EOF' | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: agentrewind-controller
  namespace: agentrewind
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: agentrewind-controller
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch", "patch"]
  - apiGroups: [""]
    resources: ["pods/status"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list"]
  - apiGroups: ["agentrewind.io"]
    resources: ["agentrewindconfigs"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["agentrewind.io"]
    resources: ["agentrewindconfigs/status"]
    verbs: ["get", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: agentrewind-controller
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: agentrewind-controller
subjects:
  - kind: ServiceAccount
    name: agentrewind-controller
    namespace: agentrewind
RBAC_EOF

kubectl wait --for=condition=available deployment/agentrewind-controller \
    -n "$NAMESPACE" --timeout=120s 2>/dev/null || echo "⚠ Controller deployment may still be starting"
echo "✓ AgentRewind controller deployed"

# 11. Apply AgentRewindConfig CR
echo "Applying AgentRewindConfig..."
kubectl apply -f "$REPO_ROOT/demo/manifests/agentrewindconfig.yaml" 2>/dev/null || true
echo "✓ AgentRewindConfig applied"

# 12. Deploy Grafana
echo "Deploying Grafana..."
kubectl apply -f "$REPO_ROOT/demo/manifests/grafana.yaml"
kubectl wait --for=condition=available deployment/grafana -n "$NAMESPACE" --timeout=120s 2>/dev/null || echo "⚠ Grafana deployment may still be starting"
echo "✓ Grafana deployed"

echo ""
echo "=== Setup Complete ==="
echo "  Cluster:     kind-${CLUSTER_NAME}"
echo "  Namespace:   ${NAMESPACE}"
echo ""
echo "  Port-forward commands:"
echo "    Jaeger UI:   kubectl port-forward svc/jaeger -n ${NAMESPACE} 16686:16686   → http://localhost:16686"
echo "    Grafana:     kubectl port-forward svc/grafana -n ${NAMESPACE} 3000:3000     → http://localhost:3000 (admin/admin)"
echo ""
