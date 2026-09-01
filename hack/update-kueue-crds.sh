#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 CoreWeave, Inc.
# SPDX-License-Identifier: Apache-2.0
# SPDX-PackageName: kueue-hero-workload-controller

# Vendors the Kueue CRDs used by envtest into test/crds/.
#
# The CRDs are taken from the official kueue release manifests and the
# conversion-webhook stanza is stripped: envtest runs no kueue webhook
# server, and this controller only reads/writes the storage version
# (v1beta2), so conversion is never needed in tests.
set -euo pipefail

KUEUE_VERSION="${KUEUE_VERSION:?set KUEUE_VERSION, e.g. v0.16.9}"
OUT_DIR="$(cd "$(dirname "$0")/.." && pwd)/test/crds"

command -v yq >/dev/null || { echo "yq is required" >&2; exit 1; }

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

curl -fsSL "https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml" -o "$tmp"

mkdir -p "$OUT_DIR"
rm -f "$OUT_DIR"/kueue.x-k8s.io_*.yaml

for name in $(yq eval-all -N 'select(.kind == "CustomResourceDefinition") | .metadata.name' "$tmp"); do
  yq eval-all -N "select(.kind == \"CustomResourceDefinition\" and .metadata.name == \"${name}\") | del(.spec.conversion) | del(.metadata.annotations[\"cert-manager.io/inject-ca-from\"])" \
    "$tmp" > "$OUT_DIR/${name}.yaml"
done

echo "# Kueue CRDs vendored from ${KUEUE_VERSION} release manifests by hack/update-kueue-crds.sh" > "$OUT_DIR/VERSION"
echo "${KUEUE_VERSION}" >> "$OUT_DIR/VERSION"

ls "$OUT_DIR"
