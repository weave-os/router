# Self-hosted production router

This guide runs the router on your own host with the same router image,
classifier image, classifier package and `prod/stable` routing graph that the
managed service serves. It uses
[`deploy/selfhosted/docker-compose.yml`](../deploy/selfhosted/docker-compose.yml).

The repository root `docker-compose.yml` is a local-development stack with a stub
classifier and fixed credentials. Don't deploy it.

## What runs

| Service | Image | Role |
| --- | --- | --- |
| `router` | Managed router image, pinned by digest | Serves `/v1/*`. Its `/ui` dashboard is on standalone and off once linked to Weave; `ROUTER_UI_ENABLED` overrides. |
| `classifier` | The prod classifier-service image, pinned by digest, in `local` mode | Serves the v5 classifier API from the exported release, selection and package. Embeddings come from Gemini or your own Vertex AI project. The router reaches it at `http://classifier:8080`. |
| `policy` | Policy bundle | Copies the exported serving registry and the classifier package into the `router_policy` volume, then stays up. |
| `migrate` | Router migrations | Creates the `router` schema, applies every router migration in this release, then stays up. |
| `postgres` | `postgres:15-alpine` | Standalone only. |
| `pubsub-emulator` | Pub/Sub emulator | Standalone only. Carries cache invalidations between router replicas. |

`migrate` and `policy` keep running after they finish because Distr treats any
exited container as a failed deployment. The router starts only after `migrate`
and `policy` are healthy and the classifier's `/readyz` passes.

## Where the routing graph comes from

The release runs `policyctl serving export-selfhosted` against the managed
`prod/stable` target (see
[`SERVING_CONTROL.md`](SERVING_CONTROL.md#self-hosted-export)). The command
copies that activation's router candidate, classifier release and policies
unchanged. It writes a new selection set, serving state and classifier runtime
binding for a worker identified as `project=selfhosted`, `region=local` and
`revision=selfhosted-router`, then boots the result through the router's own
`PrepareWorker` before it prints anything.

The router runs the exact image digest the copied candidate names. Its boot
check rejects any other image, so a self-hosted router can't run a build that
production didn't serve.

## Classifier service and the local release

The classifier is the prod `classifier-service` image run with
`CLASSIFIER_SERVICE_MODE=local`. It reads the exported classifier files from the
policy volume:

- `CLASSIFIER_LOCAL_RELEASE_MANIFEST_PATH` (`classifier/release.json`): the
  classifier release manifest.
- `CLASSIFIER_LOCAL_SELECTION_PATH` (`classifier/selection.json`): the exact
  re-bound serving selection-set bytes. The router sends SHA-256 of these bytes as
  `classifier_selection_id`, so the two agree by construction.
- `CLASSIFIER_LOCAL_PACKAGE_PATH` and `CLASSIFIER_LOCAL_PACKAGE_SHA256`: the
  classifier package, checked against its digest at boot.

`CLASSIFIER_IMAGE_DIGEST` must equal the image the release pins. The router's
readiness check rejects a mismatch.

The release's `runtime_image_digest` names the classifier image prod serves. When
the customer runs that same image, the exporter keeps the release unchanged:
`classifier.rebound` is `false`. When the customer runs a different image (the
build-from-source fallback), the exporter re-binds the release to that digest and
says so in `classifier.rebound` and `classifier.rebind_reason`. Compare those
fields in the release notes before you deploy.

## Standalone

1. Copy [`deploy/selfhosted/.env.example`](../deploy/selfhosted/.env.example) to
   `.env` next to the compose file and keep `COMPOSE_PROFILES=standalone`.
2. Fill in the release block from the release notes. A Distr release fills it in
   for you. `CLASSIFIER_IMAGE` is the classifier-service image and
   `CLASSIFIER_IMAGE_DIGEST` the digest the release pins.
3. Set the secrets. Compose refuses to start until each one is set.
   - `ROUTER_POSTGRES_PASSWORD`
   - `DATABASE_URL`: `postgres://router:<password>@postgres:5432/router?sslmode=disable`, with the password URL-encoded.
   - `EXTERNAL_KEY_ENCRYPTION_KEY`
   - `ROUTER_ADMIN_PASSWORD`
4. Set at least one provider key, plus `GEMINI_API_KEY` or the three Vertex
   variables for classifier embeddings (the `classifier` service reads them).
   Off a GCP VM, Vertex also needs a service-account key mounted through
   `HMM_VERTEX_CREDENTIALS_DIR` and `HMM_VERTEX_CREDENTIALS_FILE`.
5. Create the shared network once: `docker network create weave-router-link`.
   Compose treats it as external, so a linked Weave app and this app can share it.
6. Run `docker compose up -d`.

The router listens on `127.0.0.1:8080` by default. Put a TLS proxy in front of it,
or set `ROUTER_BIND_ADDRESS` deliberately.

## Linked to a self-hosted Weave app

When the Weave app runs on the same host, it manages the router's keys, models
and policy, and router calls appear in Weave's reports. The router then uses
Weave's Postgres, as `router-user` with `search_path=router`, and Weave's Pub/Sub
emulator. The two apps share the `weave-router-link` Docker network, which the
Weave app creates.

1. Deploy or upgrade the Weave app with the router link enabled first.
2. In this app's `.env`:
   - Set `COMPOSE_PROFILES=` (empty), so the bundled Postgres and emulator don't start.
   - Leave `ROUTER_UI_ENABLED` unset. A self-hosted router with
     `ROUTER_INTERNAL_SERVICE_TOKEN` set turns `/ui` and `/admin/v1` off, so
     Weave is the only UI; `/v1/*`, `/internal/v1/*`, health and `/v1/version`
     stay mounted. Set it to `true` or `false` to override.
   - Set `DATABASE_URL=postgres://router-user:<ROUTER_POSTGRES_PASSWORD>@postgres:5432/<weave database>?sslmode=disable`.
   - Set `PUBSUB_EMULATOR_HOST` to the Weave emulator, for example
     `pubsub-emulator:8085`, and `PUBSUB_PROJECT_ID` to the Weave app's emulator
     project.
   - Copy `ROUTER_INTERNAL_SERVICE_TOKEN` from the Weave app's
     `WEAVE_ROUTER_INTERNAL_TOKEN`.
   - Copy `EXTERNAL_KEY_ENCRYPTION_KEY` from the Weave app's value.
   - Set `OTEL_EXPORTER_OTLP_ENDPOINT` to Weave's `/api/ingest`, and
     `OTEL_EXPORTER_OTLP_HEADERS=x-router-token=<ROUTER_INGEST_TOKEN>`.
3. Run `docker compose up -d`.

**The router app owns router-schema migrations.** Its `migrate` container
migrates the `router` schema forward to this release in both modes. The Weave
app migrates that schema only up to the floor its own migrations need, and
skips that step when the schema is already at or past that floor. A dirty
`router.schema_migrations` row stops both sides; neither forces a version.
Weave reads `router_schema_version` from `/v1/version` and refuses router
writes when the version is outside the range it supports.

Moving an existing standalone router database into a linked install isn't
supported.

## Verify

```bash
curl -s http://127.0.0.1:8080/readyz
curl -s http://127.0.0.1:8080/v1/version
docker compose logs router | grep -E 'Managed serving admission enabled|Serving self-hosted export'
```

`/v1/version` reports these fields:

- `router_schema_version`: the highest applied router migration.
- `serving_source`: the managed activation the routing graph came from, as
  `{target, activation_id, selection_set_sha256}`. It matches `source` in the
  release notes.

Then send one request through Claude Code or `curl` with a router key: a key
from `/ui` when standalone, or from Weave when linked. The router's
`strategy: hmm_embedding` decision log records the classifier label.

## Upgrade and rollback

Each release pins every image and the routing graph together, so upgrading or
rolling back means choosing a different release in Distr.

- **Upgrade.** `policy` swaps the volume contents only when the bundle changed.
  `migrate` applies the new release's migrations before the new router starts.
- **Rollback.** Router migrations add columns and tables rather than rewriting
  them, so an older router runs against a newer schema. `migrate` in the older
  release finds no pending migrations; it never migrates down. Undoing a
  migration is a manual `migrate down` against a backup.
- A release that fails `PrepareWorker` or the classifier's `/readyz` never
  becomes healthy, and Distr keeps reporting the deployment as failed.
- Back up Postgres before every upgrade, and the `router` schema at minimum when
  linked.
