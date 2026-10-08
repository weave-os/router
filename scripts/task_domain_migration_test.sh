#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
container_id="$(docker run --detach --rm --publish 127.0.0.1::5432 \
  --env POSTGRES_USER=router --env POSTGRES_PASSWORD=router \
  --env POSTGRES_DB=router postgres:15-alpine)"
trap 'docker rm --force "$container_id" >/dev/null' EXIT
port="$(docker port "$container_id" 5432/tcp | sed 's/.*://')"
export PGPASSWORD=router
for attempt in {1..30}; do
  if pg_isready -h 127.0.0.1 -p "$port" -U router -d router >/dev/null; then break; fi
  sleep 1
done
psql_args=(-X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p "$port" -U router -d router)
database_url="postgres://router:router@127.0.0.1:$port/router?sslmode=disable&search_path=router"
psql "${psql_args[@]}" -c 'CREATE SCHEMA router;' >/dev/null

# A historical branch occupied 119 without creating task_domain_profiles.
# Force is fixture construction only, against the container owned above.
migrate -path db/migrations -database "$database_url" goto 118
migrate -path db/migrations -database "$database_url" force 119
migrate -path db/migrations -database "$database_url" goto 123
columns="$(psql "${psql_args[@]}" -Atc "SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='router' AND table_name='task_domain_profiles';")"
[[ "$columns" == conversation_key,root_sha256,release_sha256,evidence_sha256,outcome,expires_at,retry_after ]]
[[ "$(psql "${psql_args[@]}" -Atc "SELECT count(*) FROM pg_indexes WHERE schemaname='router' AND tablename='task_domain_profiles';")" == 2 ]]
psql "${psql_args[@]}" -c "INSERT INTO router.task_domain_profiles (conversation_key,root_sha256,release_sha256,evidence_sha256,outcome) VALUES ('synthetic-conversation','synthetic-root','synthetic-release','synthetic-evidence','{\"status\":\"ok\"}');" >/dev/null

# Existing rows must survive the normal 123 rollback/reapply path.
migrate -path db/migrations -database "$database_url" down 1
migrate -path db/migrations -database "$database_url" up
[[ "$(psql "${psql_args[@]}" -Atc "SELECT outcome->>'status' FROM router.task_domain_profiles WHERE conversation_key='synthetic-conversation';")" == ok ]]

# Fresh databases must still support the complete migration roundtrip.
migrate -path db/migrations -database "$database_url" down -all
migrate -path db/migrations -database "$database_url" up
migrate -path db/migrations -database "$database_url" down -all
migrate -path db/migrations -database "$database_url" up
echo 'Task-domain legacy upgrade, row preservation and fresh roundtrip passed'
