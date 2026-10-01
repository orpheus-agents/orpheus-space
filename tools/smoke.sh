#!/bin/sh
set -eu
smoke_prefix="orpheus-space-smoke-$$"
smoke_tmp=$(mktemp -d)
smoke_db=$(docker compose --profile test ps -q test-db)
smoke_network=$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' "$smoke_db")
cleanup() {
  docker rm -f "$smoke_prefix" >/dev/null 2>&1 || true
  rm -rf "$smoke_tmp"
}
trap cleanup EXIT INT TERM
cp orpheus-space.toml.dist "$smoke_tmp/space.toml"
chmod 755 "$smoke_tmp"
chmod 644 "$smoke_tmp/space.toml"
cat > "$smoke_tmp/app.env" <<'ENV'
DATABASE_URL=postgres://orpheus:orpheus@test-db:5432/orpheus_space_test
ORPHEUS_BROWSER_AUTH=api_only
PUBLIC_API_KEYS=["fixture-only-key"]
HARNESS_ENV_ALLOWLIST=["MATTERMOST_BOT_TOKEN"]
ORPHEUS_CONFIG_FILE=/etc/space/space.toml
ENV
[ "$(docker image inspect -f '{{.Config.User}}' orpheus-space:local)" = '65532:65532' ]
docker run --rm --network "$smoke_network" --env-file "$smoke_tmp/app.env" orpheus-space:local migrate up
docker run -d --name "$smoke_prefix" --network "$smoke_network" --read-only --cap-drop ALL --security-opt no-new-privileges --env-file "$smoke_tmp/app.env" -v "$smoke_tmp/space.toml:/etc/space/space.toml:ro" orpheus-space:local >/dev/null
smoke_attempt=0
until docker exec "$smoke_prefix" /orpheus-space healthcheck >/dev/null 2>&1; do
  smoke_attempt=$((smoke_attempt+1))
  if [ "$smoke_attempt" -ge 20 ]; then docker logs "$smoke_prefix"; exit 1; fi
  sleep 1
done
docker exec "$smoke_prefix" sleep 1
docker stop -t 10 "$smoke_prefix" >/dev/null
[ "$(docker inspect -f '{{.State.ExitCode}}' "$smoke_prefix")" = 0 ]
echo 'Production image: migration, readiness and graceful SIGTERM passed.'
