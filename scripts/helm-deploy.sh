#!/usr/bin/env bash
#
# Deploy kube-janitor-go Helm chart to a Kubernetes cluster
#
# Usage:
#   ./scripts/helm-deploy.sh [OPTIONS]
#
# Options:
#   -v, --version VERSION     Docker image tag to deploy (default: git describe)
#   -n, --namespace NAMESPACE Kubernetes namespace (default: kube-system)
#   -r, --release RELEASE     Helm release name (default: kube-janitor)
#   -f, --values FILE         Additional values file
#   --dry-run                 Perform a dry-run (template only)
#   --upgrade                 Upgrade existing release (default: install)
#   --sharding                Enable sharding mode
#   --replicas N              Number of replicas (default: 1, or 3 with sharding)
#   -y, --yes                 Skip confirmation prompt
#   -h, --help                Show this help message
#
# Examples:
#   ./scripts/helm-deploy.sh --version v1.0.0
#   ./scripts/helm-deploy.sh --version latest --namespace janitor --dry-run
#   ./scripts/helm-deploy.sh --sharding --replicas 3 --version v1.0.0
#

set -euo pipefail

# Default values
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "latest")}"
NAMESPACE="kube-janitor"
RELEASE_NAME="kube-janitor"
VALUES_FILE=""
DRY_RUN=false
UPGRADE=false
SHARDING=false
REPLICAS=""
YES=false

# Chart location (relative to script directory)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/../helm/kube-janitor-go"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[OK]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

show_help() {
    sed -n '3,23p' "$0" | sed 's/^# //' | sed 's/^#$//'
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -v|--version)
                VERSION="$2"
                shift 2
                ;;
            -n|--namespace)
                NAMESPACE="$2"
                shift 2
                ;;
            -r|--release)
                RELEASE_NAME="$2"
                shift 2
                ;;
            -f|--values)
                VALUES_FILE="$2"
                shift 2
                ;;
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            --upgrade)
                UPGRADE=true
                shift
                ;;
            --sharding)
                SHARDING=true
                shift
                ;;
            --replicas)
                REPLICAS="$2"
                shift 2
                ;;
            -y|--yes)
                YES=true
                shift
                ;;
            -h|--help)
                show_help
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                show_help
                exit 1
                ;;
        esac
    done
}

check_prerequisites() {
    log_info "Checking prerequisites..."

    if ! command -v helm &> /dev/null; then
        log_error "helm is not installed. Please install Helm first."
        exit 1
    fi

    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi

    if [[ ! -d "$CHART_DIR" ]]; then
        log_error "Chart directory not found: $CHART_DIR"
        exit 1
    fi

    # Check cluster connectivity (unless dry-run)
    if [[ "$DRY_RUN" == "false" ]]; then
        if ! kubectl cluster-info &> /dev/null; then
            log_error "Cannot connect to Kubernetes cluster. Check your kubeconfig."
            exit 1
        fi
        log_success "Connected to cluster: $(kubectl config current-context)"
    fi

    log_success "Prerequisites check passed"
}

build_helm_args() {
    local args=()

    # Release name and chart
    args+=("$RELEASE_NAME" "$CHART_DIR")

    # Namespace
    args+=("--namespace" "$NAMESPACE")
    args+=("--create-namespace")

    # Image tag from VERSION
    args+=("--set" "image.tag=${VERSION}")

    # Sharding configuration
    if [[ "$SHARDING" == "true" ]]; then
        args+=("--set" "sharding.enabled=true")
        # Default to 3 replicas for sharding if not specified
        if [[ -z "$REPLICAS" ]]; then
            REPLICAS="3"
        fi
    fi

    # Replicas
    if [[ -n "$REPLICAS" ]]; then
        args+=("--set" "replicaCount=${REPLICAS}")
    fi

    # Additional values file
    if [[ -n "$VALUES_FILE" ]]; then
        if [[ ! -f "$VALUES_FILE" ]]; then
            log_error "Values file not found: $VALUES_FILE"
            exit 1
        fi
        args+=("--values" "$VALUES_FILE")
    fi

    echo "${args[@]}"
}

confirm_deployment() {
    local context="$1"
    local action="$2"

    echo ""
    echo -e "${YELLOW}╔════════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${YELLOW}║                    DEPLOYMENT CONFIRMATION                      ║${NC}"
    echo -e "${YELLOW}╚════════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "  ${BLUE}Kubernetes Context:${NC} ${RED}${context}${NC}"
    echo -e "  ${BLUE}Action:${NC}             ${action}"
    echo -e "  ${BLUE}Release:${NC}            ${RELEASE_NAME}"
    echo -e "  ${BLUE}Namespace:${NC}          ${NAMESPACE}"
    echo -e "  ${BLUE}Image Tag:${NC}          ${VERSION}"
    echo -e "  ${BLUE}Sharding:${NC}           ${SHARDING}"
    if [[ -n "$REPLICAS" ]]; then
        echo -e "  ${BLUE}Replicas:${NC}           ${REPLICAS}"
    fi
    if [[ -n "$VALUES_FILE" ]]; then
        echo -e "  ${BLUE}Values File:${NC}        ${VALUES_FILE}"
    fi
    echo ""
    echo -e "${YELLOW}────────────────────────────────────────────────────────────────${NC}"
    echo ""

    if [[ "$YES" == "true" ]]; then
        log_info "Skipping confirmation (--yes flag provided)"
        return 0
    fi

    read -r -p "Do you want to proceed with the deployment? [y/N] " response
    case "$response" in
        [yY][eE][sS]|[yY])
            return 0
            ;;
        *)
            log_warn "Deployment cancelled by user"
            exit 0
            ;;
    esac
}

deploy() {
    local helm_args
    helm_args=$(build_helm_args)

    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "Running in dry-run mode (template only)..."
        log_info "  Version:   ${VERSION}"
        log_info "  Namespace: ${NAMESPACE}"
        log_info "  Release:   ${RELEASE_NAME}"
        log_info "  Sharding:  ${SHARDING}"
        if [[ -n "$REPLICAS" ]]; then
            log_info "  Replicas:  ${REPLICAS}"
        fi
        echo ""
        # shellcheck disable=SC2086
        helm template $helm_args
        log_success "Dry-run completed successfully"
    else
        local action="install"
        local helm_cmd="install"
        local context
        context=$(kubectl config current-context)

        # Check if release already exists
        if helm status "$RELEASE_NAME" --namespace "$NAMESPACE" &> /dev/null; then
            if [[ "$UPGRADE" == "true" ]]; then
                helm_cmd="upgrade"
                action="upgrade"
            else
                log_warn "Release '$RELEASE_NAME' already exists in namespace '$NAMESPACE'"
                log_info "Use --upgrade flag to upgrade the existing release"
                exit 1
            fi
        fi

        # Ask for confirmation
        confirm_deployment "$context" "$action"

        log_info "Running helm ${action}..."
        # shellcheck disable=SC2086
        helm $helm_cmd $helm_args --wait --timeout 5m

        log_success "Deployment completed successfully!"
        echo ""
        log_info "To check the status:"
        echo "  kubectl get pods -n ${NAMESPACE} -l app.kubernetes.io/name=kube-janitor-go"
        echo ""
        log_info "To view logs:"
        echo "  kubectl logs -n ${NAMESPACE} -l app.kubernetes.io/name=kube-janitor-go -f"
    fi
}

main() {
    parse_args "$@"
    check_prerequisites
    deploy
}

main "$@"

