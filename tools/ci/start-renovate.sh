#!/bin/sh
# Starts the Renovate job (infra/renovate on the forge) right after this job has pushed a new image, so the compose
# repositories pick up our own images at once instead of at Renovate's next scheduled run. Renovate pins the new
# digest and merges it; the deploy job then brings the stack up (dv follows its own builds this way since
# 2026-10-15, instead of through Watchtower).
#
# The job must run with `enable-openid-connect: true`. The token is the bot renovate-dispatch's, kept in SecretHive
# as ci-dispatch/renovate-dispatch-token and read with this job's own forge token (group ci-forge); that bot may only
# start workflows in infra/renovate. It goes from SecretHive to curl on stdin and is never written to the log.
#
# Never fails the job: the image is already pushed, and Renovate's own schedule catches up if this does not work.
set -u
SECRETHIVE=${SECRETHIVE:-https://secrethive.xact.cloud}
SECRET=${DISPATCH_SECRET:-ci-dispatch/renovate-dispatch-token}
FORGE=${FORGE:-https://forge.xact.cloud}

value() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(JSON.parse(s).value||"")}catch{}})'; }

[ -n "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ] || { echo "::warning::This runner issues no job token; Renovate was not started."; exit 0; }
job=$(curl -sS -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$SECRETHIVE" | value)
[ -n "$job" ] || { echo "::warning::The forge gave no job token; Renovate was not started."; exit 0; }
token=$(curl -sS -H "Authorization: Bearer $job" "$SECRETHIVE/api/v1/vaults/${SECRET%%/*}/secrets/${SECRET#*/}/value" | value)
job=""
[ -n "$token" ] || { echo "::warning::SecretHive gave no $SECRET; Renovate was not started."; exit 0; }

status=$(printf 'header = "Authorization: token %s"\n' "$token" | curl -sS -o /dev/null -w '%{http_code}' -K - \
  -H 'Content-Type: application/json' -d '{"ref":"main"}' \
  "$FORGE/api/v1/repos/infra/renovate/actions/workflows/renovate.yml/dispatches" || true)
token=""
if [ "$status" = "204" ]; then
  echo "Renovate started: the compose repositories will pick up this image in a minute or two."
else
  echo "::warning::Starting Renovate answered $status; its schedule will pick up this image later."
fi
exit 0
