#!/usr/bin/env bash
# Local database backups every 5 minutes: the latest dump, plus one per hour kept for 24 hours.
set -euo pipefail
install -d -o postgres -g postgres -m 0700 /var/backups/stockastic
cat >/usr/local/bin/stockastic-backup <<'EOF'
#!/bin/sh
# Dumps the event database (a consistent snapshot while the server keeps running).
set -eu
D=/var/backups/stockastic
pg_dump -Fc -d stockastic -f "$D/latest.dump.tmp"
mv "$D/latest.dump.tmp" "$D/latest.dump"
cp "$D/latest.dump" "$D/hour-$(date +%H).dump"
EOF
chmod 0755 /usr/local/bin/stockastic-backup
echo '*/5 * * * * postgres /usr/local/bin/stockastic-backup' > /etc/cron.d/stockastic-backup
chmod 0644 /etc/cron.d/stockastic-backup
sudo -u postgres /usr/local/bin/stockastic-backup
ls -la /var/backups/stockastic
sudo -u postgres pg_restore --list /var/backups/stockastic/latest.dump | grep -c "TABLE DATA" | sed 's/^/tables in the dump: /'
