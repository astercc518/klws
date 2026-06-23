#!/usr/bin/env bash
# Guard against high-cardinality Prometheus labels. jid / tenant_id / message_id /
# phone must never become metric label values (they would explode the series count).
# Single-account observability belongs in logs/traces, not metrics.
set -euo pipefail

pattern='WithLabelValues\([^)]*\b(jid|tenant_id|message_id|phone)\b'

if grep -rnE --include='*.go' "$pattern" . ; then
  echo "ERROR: high-cardinality label passed to a metric (see matches above)"
  exit 1
fi

echo "OK: no high-cardinality metric labels found"
