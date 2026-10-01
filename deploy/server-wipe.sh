#!/usr/bin/env bash
# Wipe every trace of testing: an empty database, a new login-signing secret, no test backups. Then start clean.
set -euo pipefail
systemctl stop stockastic
sudo -u postgres dropdb --if-exists stockastic
sudo -u postgres createdb -O stockastic stockastic
rm -rf /var/lib/stockastic/data/*
rm -f /var/backups/stockastic/*.dump

# a new login-signing secret: every token issued before is now worthless
openssl rand -hex 32 > /root/stockastic-jwt-secret.new
chmod 600 /root/stockastic-jwt-secret.new
mv /root/stockastic-jwt-secret.new /root/stockastic-jwt-secret
NEW=$(cat /root/stockastic-jwt-secret)
sed -i "s/^JWT_SECRET=.*/JWT_SECRET=$NEW/" /etc/stockastic/env

systemctl start stockastic
for i in $(seq 1 120); do curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1 && break; sleep 0.5; done
echo "ready: $(curl -sS http://127.0.0.1:8080/readyz)"
echo "events in the database: $(sudo -u postgres psql -d stockastic -tAc 'select count(*) from events')"
echo "accounts: $(sudo -u postgres psql -d stockastic -tAc 'select count(*) from accounts') (only the organiser)"
sudo -u postgres /usr/local/bin/stockastic-backup && echo "fresh backup: $(ls /var/backups/stockastic)"
