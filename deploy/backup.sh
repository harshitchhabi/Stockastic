#!/bin/sh
# Copies the durable log to S3. Run every minute during the event (see the cron line below).
#
# The log only ever grows at the end, so copying it while the server runs is safe: a half-written last line
# is discarded on the next start, and was never confirmed to any team. Turn on S3 bucket versioning so every
# copy is kept.
#
#   * * * * * /opt/stockastic/backup.sh
set -eu
DATA=/var/lib/stockastic/data/stockastic.wal
BUCKET="${STOCKASTIC_BACKUP_BUCKET:?set STOCKASTIC_BACKUP_BUCKET, for example s3://my-event-backups/stockastic}"
[ -f "$DATA" ] || exit 0
aws s3 cp --only-show-errors "$DATA" "$BUCKET/stockastic.wal"
