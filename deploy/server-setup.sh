#!/usr/bin/env bash
# Installs PostgreSQL and Caddy, the service user and folders, and the database. Prints the database password once,
# into a root-only file, never to the screen.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# ---- PostgreSQL (listens on this machine only) ----
apt-get install -y postgresql postgresql-contrib >/dev/null
PGCONF=$(ls /etc/postgresql/*/main/postgresql.conf | head -1)
sed -i "s/^#\?listen_addresses.*/listen_addresses = 'localhost'/" "$PGCONF"
systemctl enable --now postgresql
systemctl restart postgresql

# ---- Caddy (official repository) ----
apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg >/dev/null
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg --yes
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
apt-get update -y >/dev/null
apt-get install -y caddy >/dev/null

# ---- the service user and folders ----
id stockastic >/dev/null 2>&1 || useradd --system --home /var/lib/stockastic --shell /usr/sbin/nologin stockastic
install -d -o stockastic -g stockastic -m 0750 /var/lib/stockastic /var/lib/stockastic/data
install -d -o root -g stockastic -m 0750 /etc/stockastic /etc/stockastic/final
install -d -o root -g root -m 0755 /opt/stockastic

# ---- the database: a role that owns it, with a long random password ----
if [ ! -f /root/stockastic-db-password ]; then
  head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 40 > /root/stockastic-db-password
  chmod 600 /root/stockastic-db-password
fi
DBPW=$(cat /root/stockastic-db-password)
sudo -u postgres psql -v ON_ERROR_STOP=1 -q <<SQL
DO \$\$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'stockastic') THEN
    CREATE ROLE stockastic LOGIN PASSWORD '$DBPW';
  ELSE
    ALTER ROLE stockastic LOGIN PASSWORD '$DBPW';
  END IF;
END \$\$;
SQL
sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='stockastic'" | grep -q 1 || sudo -u postgres createdb -O stockastic stockastic

echo "postgres: $(sudo -u postgres psql -tAc 'show server_version') listening on $(sudo -u postgres psql -tAc 'show listen_addresses')"
echo "caddy: $(caddy version | cut -d' ' -f1)"
echo "database ready: $(PGPASSWORD="$DBPW" psql -h 127.0.0.1 -U stockastic -d stockastic -tAc 'select current_user')"
