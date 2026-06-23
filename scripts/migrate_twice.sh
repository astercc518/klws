#!/usr/bin/env bash
# Apply every migration twice against the given DSN; a non-idempotent migration
# fails on the second pass. Requires psql on PATH.
set -euo pipefail

DSN="${1:?usage: migrate_twice.sh <postgres-dsn>}"

shopt -s nullglob
files=(migrations/*.sql)
if [ ${#files[@]} -eq 0 ]; then
  echo "no migrations found under migrations/"; exit 1
fi

for pass in 1 2; do
  echo "== migration pass ${pass} =="
  for f in "${files[@]}"; do
    case "$f" in
      *.down.sql) continue ;;  # skip down-migrations
    esac
    echo "  applying ${f}"
    psql "$DSN" -v ON_ERROR_STOP=1 -q -f "$f"
  done
done

echo "OK: migrations are idempotent (applied twice with no error)"
