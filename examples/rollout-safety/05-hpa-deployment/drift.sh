#!/usr/bin/env bash
# Case 05 — external drift via kubectl (NOT formae): an HPA takes ownership of
# spec.replicas and scales the Deployment. Run AFTER `formae apply create.pkl`.
#
#   bash examples/rollout-safety/05-hpa-deployment/drift.sh [context]
#
# Then have formae re-read/reconcile the rollout-hpa stack and observe whether
# it reports drift on spec.replicas.
set -euo pipefail
CTX="${1:-$(kubectl config current-context)}"
NS=default

kubectl --context "$CTX" -n "$NS" autoscale deployment hpa-demo --min=2 --max=5 --cpu-percent=50

# Simulate the HPA scaling under load; the scale subresource write is owned by a
# non-formae field manager, mimicking the HPA controller.
kubectl --context "$CTX" -n "$NS" scale deployment hpa-demo --replicas=4

echo
echo "Deployment replica ownership (managedFields):"
kubectl --context "$CTX" -n "$NS" get deployment hpa-demo -o json \
  | jq -r '.metadata.managedFields[] | select(.fieldsV1.["f:spec"].["f:replicas"]) | "  spec.replicas owned by: \(.manager)"'
echo "live spec.replicas: $(kubectl --context "$CTX" -n "$NS" get deployment hpa-demo -o jsonpath='{.spec.replicas}')"
echo
echo "Now re-reconcile the rollout-hpa stack with formae and check for drift on spec.replicas."
