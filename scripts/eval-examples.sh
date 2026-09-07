#!/usr/bin/env bash
# Evaluate every forma under examples/ so schema drift in a plugin dependency
# cannot break the examples unnoticed. Pure `pkl eval` — no cluster, no creds.
#
# Cloud entries throw on missing knobs by design; dummy values below only make
# them evaluable. They are never used to reach a provider.
set -uo pipefail

cd "$(dirname "$0")/.."

export AZURE_PRINCIPAL_ID="${AZURE_PRINCIPAL_ID:-00000000-0000-0000-0000-000000000000}"
export AZURE_SUBSCRIPTION_ID="${AZURE_SUBSCRIPTION_ID:-00000000-0000-0000-0000-000000000001}"
export GCP_PROJECT="${GCP_PROJECT:-example-project}"
export GCP_APPLY_AS="${GCP_APPLY_AS:-user:dev@example.com}"
export OCI_COMPARTMENT_ID="${OCI_COMPARTMENT_ID:-ocid1.compartment.oc1..example}"

# Resolve every PklProject under examples/ (nested projects included).
while IFS= read -r proj; do
  pkl project resolve "$(dirname "$proj")" >/dev/null || exit 1
done < <(find examples -name PklProject | sort)

# Nearest enclosing PklProject for a forma, so imports resolve as at apply time.
project_dir() {
  local dir; dir="$(dirname "$1")"
  while [ "$dir" != "." ]; do
    [ -f "$dir/PklProject" ] && { printf '%s\n' "$dir"; return; }
    dir="$(dirname "$dir")"
  done
  printf 'examples\n'
}

failed=0 checked=0
while IFS= read -r f; do
  checked=$((checked + 1))
  rel="${f#$(project_dir "$f")/}"
  if err=$(cd "$(project_dir "$f")" && pkl eval "$rel" 2>&1 >/dev/null); then
    printf 'ok   %s\n' "$f"
  else
    printf 'FAIL %s\n%s\n' "$f" "$err"
    failed=$((failed + 1))
  fi
done < <(grep -rl --include='*.pkl' -E '^forma\b|^forma \{' examples | sort)

printf '\n%d forma checked, %d failed\n' "$checked" "$failed"
[ "$failed" -eq 0 ]
