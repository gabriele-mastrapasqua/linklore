#!/usr/bin/env bash
#
# scripts/seed-demo.sh
# --------------------
# Populate a SEPARATE demo SQLite DB with curated public links so the
# UI can be screenshotted / shown off without leaking anyone's private
# library.
#
# What this script does:
#   1. Defensive backup of ./data/linklore.db (your real library) —
#      even though the script never writes to it, we copy it to
#      .bak-<timestamp> first as a belt-and-suspenders. The script
#      operates on ./data/linklore-demo.db, a separate file.
#   2. Builds a fresh binary via `make build`.
#   3. Stops any running linklore server.
#   4. Starts a server pointed at the demo DB (LINKLORE_DB_PATH override).
#   5. Ingests ~10 well-known public URLs across 3 collections via the
#      `linklore add` CLI. The worker then fetches/extracts/summarises
#      each one in the background (using whatever LLM your .env points
#      at — or none, in which case the rows stay BM25-searchable).
#   6. Waits a configurable amount of time for the worker to drain.
#   7. Leaves the demo server running on http://127.0.0.1:8080 so you
#      can take screenshots, then prints how to switch back to your
#      real DB.
#
# Usage:
#   make seed-demo
#   # or:
#   bash scripts/seed-demo.sh
#
# Env vars:
#   DEMO_WAIT_SECS   how long to sleep waiting for ingest to land (default: 60)
#   DEMO_DB_PATH     override the demo DB path
#                    (default: ./data/linklore-demo.db)

set -euo pipefail

REAL_DB="${LINKLORE_DB_PATH_REAL:-./data/linklore.db}"
DEMO_DB="${DEMO_DB_PATH:-./data/linklore-demo.db}"
ADDR="127.0.0.1:8080"
WAIT_SECS="${DEMO_WAIT_SECS:-60}"

cd "$(dirname "$0")/.."

# --- 1. defensive backup of the real DB ----------------------------
if [[ -f "$REAL_DB" ]]; then
    BAK="${REAL_DB}.bak-$(date +%Y%m%d-%H%M%S)"
    cp "$REAL_DB" "$BAK"
    echo "→ backed up real DB:  $REAL_DB → $BAK"
else
    echo "→ no real DB at $REAL_DB (skipping backup)"
fi

# --- 2. wipe stale demo DB ----------------------------------------
rm -f "$DEMO_DB" "${DEMO_DB}-shm" "${DEMO_DB}-wal"
echo "→ fresh demo DB:      $DEMO_DB"

# --- 3. build ------------------------------------------------------
make build > /dev/null
echo "→ built ./bin/linklore"

# --- 4. stop any existing server, start one pointed at demo DB -----
pkill -f 'bin/linklore serve' 2>/dev/null || true
sleep 1

export LINKLORE_DB_PATH="$DEMO_DB"
nohup ./bin/linklore serve --config ./configs/config.yaml \
    > /tmp/linklore-demo.log 2>&1 &
SERVER_PID=$!
echo "→ server started: pid=$SERVER_PID, db=$DEMO_DB, log=/tmp/linklore-demo.log"

# Wait for /healthz to come up.
for _ in $(seq 1 20); do
    if curl -s -o /dev/null -w '%{http_code}' "http://$ADDR/healthz" | grep -q '^200$'; then
        break
    fi
    sleep 0.5
done

# --- 5. ingest curated public links -------------------------------
# Format: "<collection-slug>|<url>". Every URL here is publicly
# accessible content so a screenshot of the UI is safe to publish.
links=(
    # Reading — well-known long-form articles
    "reading|https://en.wikipedia.org/wiki/Local-first_software"
    "reading|https://martinfowler.com/articles/microservices.html"
    "reading|https://stripe.com/blog/payment-api-design"
    "reading|https://paulgraham.com/airbnbs.html"
    "reading|https://go.dev/blog/error-handling-and-go"

    # Tools — projects + docs
    "tools|https://github.com/bigskysoftware/htmx"
    "tools|https://github.com/sqlite/sqlite"
    "tools|https://github.com/ollama/ollama"
    "tools|https://sqlite.org/wal.html"

    # Videos — single public talk
    "videos|https://www.youtube.com/watch?v=PAAkCSZUG1c"
)

echo "→ ingesting ${#links[@]} curated links…"
for entry in "${links[@]}"; do
    slug="${entry%%|*}"
    url="${entry##*|}"
    if ./bin/linklore add -c "$slug" "$url" >/dev/null 2>&1; then
        printf "    ✓ %-8s %s\n" "[$slug]" "$url"
    else
        printf "    ✗ %-8s %s\n" "[$slug]" "$url"
    fi
done

# --- 6. wait for worker -------------------------------------------
echo "→ waiting ${WAIT_SECS}s for fetch + summary to land…"
sleep "$WAIT_SECS"

# --- 7. summary ---------------------------------------------------
echo
echo "Demo DB ready."
echo "  Path:    $DEMO_DB"
echo "  URL:     http://$ADDR"
echo "  Server:  pid $SERVER_PID (logs: /tmp/linklore-demo.log)"
echo
echo "Screenshot the UI now, then switch back to your real DB:"
echo "    pkill -f 'bin/linklore serve'"
echo "    ./bin/linklore serve --config ./configs/config.yaml &"
