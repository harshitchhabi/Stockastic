#!/bin/sh
# Copies the PostgreSQL database to S3. Run it every few minutes during the event (see the cron line below).
#
# pg_dump takes a consistent snapshot while the server keeps writing, so this is always safe to run live.
# It uses the custom format (-Fc), which pg_restore can load into a brand new, empty database.
# Turn on S3 bucket versioning so every copy is kept, not just the newest.
#
#   */5 * * * * STOCKASTIC_BACKUP_BUCKET=s3://my-event-backups/stockastic DATABASE_URL=... /opt/stockastic/backup-postgres.sh
set -eu
BUCKET="${STOCKASTIC_BACKUP_BUCKET:?set STOCKASTIC_BACKUP_BUCKET, for example s3://my-event-backups/stockastic}"
DSN="${DATABASE_URL:?set DATABASE_URL}"
OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT
pg_dump -Fc --dbname="$DSN" -f "$OUT"
aws s3 cp --only-show-errors "$OUT" "$BUCKET/stockastic.dump"
