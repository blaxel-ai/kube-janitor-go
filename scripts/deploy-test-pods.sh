#!/usr/bin/env bash
#
# Deploy test pods with various TTL values to test kube-janitor
# Pods are created in Blaxel sandbox format
#
# Usage:
#   ./scripts/deploy-test-pods.sh [OPTIONS]
#
# Options:
#   -n, --namespace NAMESPACE  Namespace to deploy to (default: $POD_NAMESPACE or $KUBE_NAMESPACE or "default")
#   -c, --count N              Number of pods per TTL tier (default: 1)
#   --cleanup                  Delete all test pods
#   -h, --help                 Show this help message
#

set -euo pipefail

NAMESPACE="${NAMESPACE:-${POD_NAMESPACE:-${KUBE_NAMESPACE:-default}}}"
COUNT="${COUNT:-1}"
CLEANUP="${CLEANUP:-false}"

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[OK]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }

show_help() {
    sed -n '2,15p' "$0" | sed 's/^# //' | sed 's/^#$//'
}

# Generate a random 6-char suffix
random_suffix() {
    # Use openssl for reliable random string generation (avoids SIGPIPE issues)
    openssl rand -hex 3
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -n|--namespace)
                NAMESPACE="$2"
                shift 2
                ;;
            -c|--count)
                COUNT="$2"
                shift 2
                ;;
            --cleanup)
                CLEANUP=true
                shift
                ;;
            -h|--help)
                show_help
                exit 0
                ;;
            *)
                echo "Unknown option: $1"
                show_help
                exit 1
                ;;
        esac
    done
}

cleanup_pods() {
    log_info "Cleaning up test pods in namespace: ${NAMESPACE}"
    kubectl delete pods -n "${NAMESPACE}" -l blaxel-type=sandboxes,janitor-test=true --ignore-not-found=true
    log_success "Cleanup complete"
}

deploy_pod() {
    local ttl="$1"
    local description="$2"
    local suffix
    suffix=$(random_suffix)
    local name="sbx-janitor-test-${description}-${suffix}"
    local workspace_id
    workspace_id=$(random_suffix)

    log_info "Deploying ${name} with TTL=${ttl} (${description})"

    kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  namespace: ${NAMESPACE}
  labels:
    app: "${name}"
    blaxel-account-id: "test-account-id"
    blaxel-memory: "512"
    blaxel-name: "janitor-test"
    blaxel-s-customer-id: "test-customer-id"
    blaxel-service-domain: "execution"
    blaxel-service-group: "executionplane"
    blaxel-type: "sandboxes"
    blaxel-workspace: "janitor-test"
    blaxel-workspace-id: "${workspace_id}"
    blaxel.ai/revision-id: "test-revision"
    blaxel.ai/revision-type: "active"
    janitor-test: "true"
    ttl-tier: "${description}"
  annotations:
    janitor/ttl: "${ttl}"
    blaxel.ai/scale-to-zero-policy: "off"
    blaxel.ai/scale-to-zero-stateful: "false"
    description: "Test sandbox pod - will be deleted after ${ttl}"
spec:
  containers:
    - name: sandbox
      image: busybox:latest
      command: ["sleep", "infinity"]
      ports:
        - name: sandbox-api
          containerPort: 8080
          protocol: TCP
      env:
        - name: BL_ENV
          value: "test"
        - name: BL_TYPE
          value: "sandbox"
        - name: BL_NAME
          value: "janitor-test"
        - name: BL_WORKSPACE
          value: "janitor-test"
        - name: BL_WORKSPACE_ID
          value: "${workspace_id}"
      resources:
        limits:
          cpu: "10m"
          memory: "16Mi"
        requests:
          cpu: "5m"
          memory: "8Mi"
  restartPolicy: Never
  tolerations:
    - key: runtime.blaxel.ai/provider
      value: blaxel
      effect: NoSchedule
    - key: runtime.blaxel.ai/generation
      value: mk3.1
      effect: NoSchedule
    - key: runtime.blaxel.ai/workload-type
      value: job
      effect: NoSchedule
EOF
}

deploy_test_pods() {
    log_info "Deploying Blaxel-style test sandbox pods to namespace: ${NAMESPACE}"
    log_info "Count per TTL tier: ${COUNT}"
    echo ""

    # Ensure namespace exists
    kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f - > /dev/null 2>&1 || true

    # Define TTL tiers
    declare -a TTLS=("10s" "30s" "1m" "2m" "10m")
    declare -a DESCRIPTIONS=("10s" "30s" "1m" "2m" "10m")

    local total=0
    local pids=()

    log_info "Spawning parallel deployments..."

    for i in "${!TTLS[@]}"; do
        ttl="${TTLS[$i]}"
        desc="${DESCRIPTIONS[$i]}"

        for j in $(seq 1 "${COUNT}"); do
            deploy_pod "${ttl}" "${desc}" &
            pids+=($!)
            ((total++))
        done
    done

    log_info "Waiting for ${total} deployments to complete..."

    # Wait for all background jobs
    local failed=0
    for pid in "${pids[@]}"; do
        if ! wait "$pid"; then
            ((failed++))
        fi
    done

    echo ""
    if [[ $failed -gt 0 ]]; then
        log_warn "${failed} deployment(s) failed"
    fi
    log_success "Deployed $((total - failed))/${total} test sandbox pods"
    echo ""

    # Show summary
    echo -e "${BLUE}╔════════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║              BLAXEL SANDBOX TEST PODS SUMMARY                  ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "  ${YELLOW}Namespace:${NC} ${NAMESPACE}"
    echo -e "  ${YELLOW}Total Pods:${NC} ${total}"
    echo -e "  ${YELLOW}Pod Format:${NC} sbx-janitor-test-<ttl>-<random>"
    echo ""
    echo -e "  ${YELLOW}Labels:${NC}"
    echo "    - blaxel-type: sandboxes"
    echo "    - blaxel-service-group: executionplane"
    echo "    - janitor-test: true"
    echo ""
    echo -e "  ${YELLOW}TTL Tiers:${NC}"
    echo "    - 10s  : ${COUNT} pod(s) - deleted ~10 seconds after creation"
    echo "    - 30s  : ${COUNT} pod(s) - deleted ~30 seconds after creation"
    echo "    - 1m   : ${COUNT} pod(s) - deleted ~1 minute after creation"
    echo "    - 2m   : ${COUNT} pod(s) - deleted ~2 minutes after creation"
    echo "    - 10m  : ${COUNT} pod(s) - deleted ~10 minutes after creation"
    echo ""
    echo -e "  ${YELLOW}Monitor pods:${NC}"
    echo "    kubectl get pods -n ${NAMESPACE} -l blaxel-type=sandboxes,janitor-test=true -w"
    echo ""
    echo -e "  ${YELLOW}View janitor logs:${NC}"
    echo "    kubectl logs -n kube-system -l app.kubernetes.io/name=kube-janitor-go -f"
    echo ""
    echo -e "  ${YELLOW}View events:${NC}"
    echo "    kubectl get events -n ${NAMESPACE} --field-selector reason=ResourceDeleted -w"
    echo ""
    echo -e "  ${YELLOW}Cleanup:${NC}"
    echo "    ./scripts/deploy-test-pods.sh --cleanup -n ${NAMESPACE}"
}

main() {
    parse_args "$@"

    if [[ "${CLEANUP}" == "true" ]]; then
        cleanup_pods
    else
        deploy_test_pods
    fi
}

main "$@"
