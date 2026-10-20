#!/bin/sh
# Prints the alert relay's token, read from SecretHive with this job's own forge token, or nothing.
#
# The job must run with `enable-openid-connect: true`. SecretHive trusts the forge's tokens for what
# the group linked to this repository may read (ci-tarichive). Nothing is stored on the forge, the
# value never reaches a file that outlives this script, and every refusal is said on stderr so the
# run's log shows why it failed (relay.mjs stops a CI job that has no token).
SECRETHIVE=${SECRETHIVE:-https://secrethive.xact.cloud}
RELAY_SECRET=${RELAY_SECRET:-ci-tarichive/alert-relay-token}

value() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(JSON.parse(s).value||"")}catch{}})'; }

[ -n "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ] || { echo "This runner issues no job token." >&2; exit 0; }
job=$(curl -sS -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$SECRETHIVE" | value)
[ -n "$job" ] || { echo "The forge gave no job token." >&2; exit 0; }
answer=$(mktemp)
status=$(curl -sS -o "$answer" -w '%{http_code}' -H "Authorization: Bearer $job" \
  "$SECRETHIVE/api/v1/vaults/${RELAY_SECRET%%/*}/secrets/${RELAY_SECRET#*/}/value" || true)
if [ "$status" = "200" ]; then value < "$answer"; else echo "SecretHive answered $status for $RELAY_SECRET." >&2; fi
rm -f "$answer"
