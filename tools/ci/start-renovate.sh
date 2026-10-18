#!/bin/sh
# Starts the Renovate job (infra/renovate on the forge) right after this job has pushed a new image, so the compose
# repositories pick up our own images at once instead of at Renovate's next scheduled run. Renovate pins the new
# digest and merges it; the deploy job then brings the stack up (dv follows its own builds this way since
# 2026-10-15, instead of through Watchtower).
#
#   sh tools/ci/start-renovate.sh [<image>:<tag> ...]
#
# With an image and tag (ghcr.io/rousseauxy/spottr:0.7.1), it first waits until the registry LISTS that tag, which is
# what Renovate reads to find a new version: started seconds after the push, Renovate missed a fresh tag in 2 of 5
# Spottr releases (2026-10-17/18) and the bump waited for a hand-started run. It asks every 10 seconds, for at most
# TAG_WAIT_SECONDS (300); after that it starts Renovate anyway. Without arguments it starts Renovate at once, as before.
# For a private image it signs in with the registry login this job already made (docker login, ghcr-login.sh).
#
# The job must run with `enable-openid-connect: true`. The token is the bot renovate-dispatch's, kept in SecretHive
# as ci-dispatch/renovate-dispatch-token and read with this job's own forge token (group ci-forge); that bot may only
# start workflows in infra/renovate. It goes from SecretHive to curl on stdin and is never written to the log.
#
# Never fails the job: the image is already pushed, and Renovate's own schedule catches up if this does not work.
#
# Kept identical in every repository that uses it; TaricHive's copy is the one to change.
set -u
SECRETHIVE=${SECRETHIVE:-https://secrethive.xact.cloud}
SECRET=${DISPATCH_SECRET:-ci-dispatch/renovate-dispatch-token}
FORGE=${FORGE:-https://forge.xact.cloud}
TAG_WAIT_SECONDS=${TAG_WAIT_SECONDS:-300}

value() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(JSON.parse(s).value||"")}catch{}})'; }

# The basic-auth value docker login stored for a registry, from the job's docker config; "" when there is none.
registry_auth() {
  node -e '
    const fs = require("fs"), os = require("os"), path = require("path");
    const dir = process.env.DOCKER_CONFIG || path.join(os.homedir(), ".docker");
    try {
      const auths = JSON.parse(fs.readFileSync(path.join(dir, "config.json"), "utf8")).auths || {};
      const a = auths[process.argv[1]] || auths["https://" + process.argv[1]];
      if (a && a.auth) process.stdout.write(a.auth);
    } catch {}' "$1"
}

# Waits until a registry lists <image>:<tag>. Only ghcr.io-style registries (a token endpoint at /token).
wait_for_tag() {
  ref=$1
  image=${ref%:*}
  tag=${ref##*:}
  registry=${image%%/*}
  repo=${image#*/}
  [ "$image" != "$ref" ] && [ -n "$tag" ] || { echo "::warning::'$ref' names no tag; not waiting for it."; return; }

  auth=$(registry_auth "$registry")
  if [ -n "$auth" ]; then
    bearer=$(printf 'header = "Authorization: Basic %s"\n' "$auth" | curl -sS -K - \
      "https://$registry/token?scope=repository:$repo:pull&service=$registry" |
      node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(JSON.parse(s).token||"")}catch{}})')
  else
    bearer=$(curl -sS "https://$registry/token?scope=repository:$repo:pull&service=$registry" |
      node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(JSON.parse(s).token||"")}catch{}})')
  fi
  auth=""
  [ -n "$bearer" ] || { echo "::warning::$registry gave no token for $repo; not waiting for $tag."; return; }

  start=$(date +%s)
  while :; do
    # 0: listed; 1: a tag list without it (yet); 2: no tag list at all (no access, no such image).
    printf 'header = "Authorization: Bearer %s"\n' "$bearer" | curl -sS -K - "https://$registry/v2/$repo/tags/list?n=10000" |
      TAG="$tag" node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{let j;try{j=JSON.parse(s)}catch{process.exit(2)}if(!Array.isArray(j.tags))process.exit(2);process.exit(j.tags.includes(process.env.TAG)?0:1)})'
    case $? in
      0) echo "The registry lists $image:$tag (after $(( $(date +%s) - start )) s)."; break ;;
      2) echo "::warning::$registry gave no tag list for $repo; not waiting for $tag."; break ;;
    esac
    if [ $(( $(date +%s) - start )) -ge "$TAG_WAIT_SECONDS" ]; then
      echo "::warning::$image:$tag is not listed after ${TAG_WAIT_SECONDS} s; starting Renovate anyway."
      break
    fi
    sleep 10
  done
  bearer=""
}

for ref in "$@"; do
  wait_for_tag "$ref"
done

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
