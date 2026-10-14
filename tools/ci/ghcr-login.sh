#!/bin/sh
# Signs the job's Docker in to ghcr.io with the token kept in SecretHive, read with this job's own
# forge token. Replaces the GHCR_TOKEN secret that every workflow used to read from the forge.
#
# The job must run with `enable-openid-connect: true`; SecretHive trusts the forge's job tokens for
# what the group linked to this repository may read (ci-forge). The value goes from SecretHive to
# `docker login` on stdin and is never written to the log or to a file of its own.
#
# GHCR_FALLBACK, when the job passes the forge secret in, is used only if SecretHive gives nothing,
# with a warning in the log. It is there for the move and stops mattering the moment the forge
# secret is deleted: then a SecretHive that answers nothing fails the login, and says why.
set -eu
SECRETHIVE=${SECRETHIVE:-https://secrethive.xact.cloud}
REGISTRY_SECRET=${REGISTRY_SECRET:-ci-forge/gh-token}
REGISTRY=${REGISTRY:-ghcr.io}

value() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(JSON.parse(s).value||"")}catch{}})'; }

from_secrethive() {
  [ -n "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ] || { echo "This runner issues no job token." >&2; return 0; }
  job=$(curl -sS -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$SECRETHIVE" | value)
  [ -n "$job" ] || { echo "The forge gave no job token." >&2; return 0; }
  answer=$(mktemp)
  status=$(curl -sS -o "$answer" -w '%{http_code}' -H "Authorization: Bearer $job" \
    "$SECRETHIVE/api/v1/vaults/${REGISTRY_SECRET%%/*}/secrets/${REGISTRY_SECRET#*/}/value" || true)
  if [ "$status" = "200" ]; then value < "$answer"; else echo "SecretHive answered $status for $REGISTRY_SECRET." >&2; fi
  rm -f "$answer"
}

token=$(from_secrethive)
if [ -n "$token" ]; then
  echo "Registry token read from SecretHive ($REGISTRY_SECRET)."
elif [ -n "${GHCR_FALLBACK:-}" ]; then
  echo "::warning::SecretHive gave no registry token; using the forge secret GHCR_TOKEN instead."
  token=$GHCR_FALLBACK
else
  echo "::error::No registry token: SecretHive gave none for $REGISTRY_SECRET and there is no forge secret to fall back on." >&2
  exit 1
fi

echo "::add-mask::$token"
printf '%s' "$token" | docker login "$REGISTRY" -u "${GITHUB_ACTOR:-rousseauxy}" --password-stdin
