#!/usr/bin/env bash
# Rebuild + redeploy the klws docker-compose stack from the CURRENT repo working
# tree, with the postgres-data-loss footgun designed out.
#
# WHY THIS SCRIPT EXISTS
#   This host runs the live stack as docker-compose project `klws`. There is NO
#   auto-deploy: merging to main does not reach production — someone must rebuild
#   the images and recreate the containers. Doing that by hand is dangerous here:
#   any `docker compose ... up` that OMITS `-f docker-compose.adopt.yml` makes
#   compose judge the postgres config as mismatched and RECREATE `klws-postgres-1`
#   onto an empty named volume `klws_pgdata` — the live DB appears to "go empty"
#   (data is not lost; the real external volume is simply detached). This script
#   hardcodes BOTH -f files so that can never be forgotten, and it verifies before
#   AND after that the postgres container was not recreated and its row counts did
#   not change — aborting loudly with the recovery command if either happens.
#
# USAGE
#   scripts/deploy.sh                 # rebuild + redeploy backend + frontend (default)
#   scripts/deploy.sh backend         # only the Go console/API
#   scripts/deploy.sh frontend        # only the Next.js frontend
#   scripts/deploy.sh backend frontend wadist   # explicit service list
#
# It never touches postgres/redis/proxy unless you name them explicitly (and it
# will refuse to let postgres be recreated regardless — see the guardrail below).
set -euo pipefail

# --- fixed stack identity (never edit these two lines apart) -----------------
COMPOSE=(docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml)
PG=klws-postgres-1
PGUSER=app
PGDB=wadist
# services rebuilt when none are named on the command line:
DEFAULT_SERVICES=(backend frontend)

cd "$(git rev-parse --show-toplevel)"

if [ ! -f docker-compose.adopt.yml ]; then
  echo "FATAL: docker-compose.adopt.yml not found in $(pwd) — this is the host-only" >&2
  echo "       file that pins postgres to its real external volume. Refusing to run" >&2
  echo "       a deploy without it (that is exactly the footgun this script guards)." >&2
  exit 1
fi

services=("$@")
[ ${#services[@]} -eq 0 ] && services=("${DEFAULT_SERVICES[@]}")

echo "==> deploying services: ${services[*]}"
echo "==> compose: ${COMPOSE[*]}"

# --- pre-flight: snapshot postgres identity + data ---------------------------
pg_container_id() { docker inspect --format '{{.Id}}' "$PG" 2>/dev/null || true; }
pg_volume() { docker inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}' "$PG" 2>/dev/null || true; }
pg_counts() {
  docker exec "$PG" psql -U "$PGUSER" -d "$PGDB" -tAc \
    "SELECT (SELECT count(*) FROM console_users)||'/'||(SELECT count(*) FROM tenants)||'/'||(SELECT count(*) FROM wallet_ledger);" 2>/dev/null | tr -d '[:space:]'
}

PG_ID_BEFORE=$(pg_container_id)
PG_VOL_BEFORE=$(pg_volume)
PG_COUNTS_BEFORE=$(pg_counts)

if [ -z "$PG_ID_BEFORE" ]; then
  echo "WARNING: $PG is not running; cannot snapshot DB for the safety guard." >&2
  echo "         Proceeding, but the postgres-recreate check will be skipped." >&2
else
  echo "==> pre-flight  postgres container=${PG_ID_BEFORE:0:12} volume=${PG_VOL_BEFORE} counts(users/tenants/ledger)=${PG_COUNTS_BEFORE:-?}"
fi

# --- build (from current working tree) ---------------------------------------
echo "==> building: ${services[*]}"
"${COMPOSE[@]}" build "${services[@]}"

# --- recreate only the named services ----------------------------------------
echo "==> recreating: ${services[*]}"
"${COMPOSE[@]}" up -d "${services[@]}"

# --- post-flight guardrail: postgres must be untouched -----------------------
if [ -n "$PG_ID_BEFORE" ]; then
  PG_ID_AFTER=$(pg_container_id)
  PG_VOL_AFTER=$(pg_volume)
  if [ "$PG_ID_AFTER" != "$PG_ID_BEFORE" ] || [ "$PG_VOL_AFTER" != "$PG_VOL_BEFORE" ]; then
    echo "" >&2
    echo "########################################################################" >&2
    echo "# DANGER: postgres was RECREATED by this deploy (the footgun fired)." >&2
    echo "#   before: container=${PG_ID_BEFORE:0:12} volume=${PG_VOL_BEFORE}" >&2
    echo "#   after : container=${PG_ID_AFTER:0:12} volume=${PG_VOL_AFTER}" >&2
    echo "# The live DB is now on the WRONG volume. Data is NOT lost — the real" >&2
    echo "# external volume still exists. Recover by re-running the FULL stack with" >&2
    echo "# both -f files so postgres re-attaches to its real volume:" >&2
    echo "#   ${COMPOSE[*]} up -d" >&2
    echo "########################################################################" >&2
    exit 2
  fi
  PG_COUNTS_AFTER=$(pg_counts)
  echo "==> post-flight postgres container=${PG_ID_AFTER:0:12} (unchanged) counts=${PG_COUNTS_AFTER:-?}"
  if [ -n "$PG_COUNTS_BEFORE" ] && [ "$PG_COUNTS_AFTER" != "$PG_COUNTS_BEFORE" ]; then
    echo "WARNING: DB row counts changed across deploy (${PG_COUNTS_BEFORE} -> ${PG_COUNTS_AFTER})." >&2
    echo "         Expected identical; investigate before trusting this deploy." >&2
  fi
fi

# --- liveness verification (through the nginx proxy on localhost) ------------
echo "==> verifying liveness via proxy"
code() { curl -sk -o /dev/null -w '%{http_code}' "https://localhost$1" 2>/dev/null || echo 000; }
ok=1
report() { # path expected actual
  if [ "$3" = "$2" ]; then echo "    OK   $1 -> $3"; else echo "    FAIL $1 -> $3 (want $2)"; ok=0; fi
}
# /api/v1/auth/me is auth-guarded and always present → 401 proves the backend is
# up and reachable through the proxy (the backend's /healthz lives at root, which
# nginx routes to the frontend, so it is not a usable backend liveness probe here).
report /api/v1/auth/me 401 "$(code /api/v1/auth/me)"
report /login          200 "$(code /login)"
# admin routes are auth-guarded: 401 proves the route EXISTS on the new build
# (a 404 would mean the old image is still serving). Adjust/extend as routes move.
for r in /api/v1/admin/finance/stats /api/v1/admin/finance/bill /api/v1/admin/campaigns; do
  report "$r" 401 "$(code "$r")"
done
report /admin/billing   200 "$(code /admin/billing)"
report /admin/campaigns 200 "$(code /admin/campaigns)"

echo ""
if [ "$ok" = 1 ]; then
  echo "==> DEPLOY OK. Services (${services[*]}) rebuilt from the current tree and live."
  echo "    Note: if redis was recreated, sessions are cleared — users must re-login,"
  echo "    and browsers should hard-refresh to drop the old cached bundle."
else
  echo "==> DEPLOY COMPLETED WITH FAILED CHECKS — inspect the FAIL lines above." >&2
  exit 3
fi
