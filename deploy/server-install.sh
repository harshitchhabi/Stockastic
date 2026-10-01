#!/usr/bin/env bash
# Installs the app, the final data and the settings file, then starts the service and Caddy.
# Expects in /tmp/stk: stockastic-linux, universe.json, scenario.json, prices.json, google.secret.env, organiser.secret.env
set -euo pipefail
S=/tmp/stk

install -o root -g root -m 0755 "$S/stockastic-linux" /opt/stockastic/api
install -o root -g stockastic -m 0640 "$S/universe.json" "$S/scenario.json" "$S/prices.json" /etc/stockastic/final/

# ---- settings: secrets made here stay here ----
if [ ! -f /root/stockastic-jwt-secret ]; then
  openssl rand -hex 32 > /root/stockastic-jwt-secret
  chmod 600 /root/stockastic-jwt-secret
fi
JWT=$(cat /root/stockastic-jwt-secret)
DBPW=$(cat /root/stockastic-db-password)
umask 077
{
  echo "# Stockastic settings. Readable only by root and the service. Made by the install script."
  echo "JWT_SECRET=$JWT"
  grep -E '^(ADMIN_EMAIL|ADMIN_PASSWORD|ADMIN_NAME)=' "$S/organiser.secret.env"
  echo "TOKEN_TTL_HOURS=24"
  echo "ADDR=127.0.0.1:8080"
  echo "DATA_DIR=/var/lib/stockastic/data"
  echo "DATABASE_URL=postgres://stockastic:$DBPW@127.0.0.1:5432/stockastic?sslmode=disable"
  echo "ALLOW_SIGNUP=true"
  echo "ALLOWED_ORIGINS=https://stockastic.dreammerchantsevent.me"
  echo "UNIVERSE_PATH=/etc/stockastic/final/universe.json"
  echo "SCENARIO_PATH=/etc/stockastic/final/scenario.json"
  echo "DISK_MIN_FREE_MB=500"
  echo "SIGNUP_CODE="
  echo "MAX_ACCOUNTS=1100"
  echo "MAX_SOCKETS=4000"
  echo "MAX_SOCKETS_PER_ACCOUNT=10"
  echo "TRUSTED_PROXIES=127.0.0.1,::1"
  grep -E '^(GOOGLE_CLIENT_ID|GOOGLE_CLIENT_SECRET|GOOGLE_REDIRECT_URL|GOOGLE_ALLOWED_DOMAINS|SIGNUP_GOOGLE_ONLY)=' "$S/google.secret.env"
  echo "LOG_LEVEL=info"
} > /etc/stockastic/env.new
chown root:stockastic /etc/stockastic/env.new
chmod 0640 /etc/stockastic/env.new
mv /etc/stockastic/env.new /etc/stockastic/env

# ---- the service ----
install -o root -g root -m 0644 ~ubuntu/deploy/stockastic.service /etc/systemd/system/stockastic.service
systemctl daemon-reload
systemctl enable stockastic >/dev/null 2>&1
systemctl restart stockastic

# ---- HTTPS in front ----
install -o root -g root -m 0644 ~ubuntu/deploy/Caddyfile /etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile >/dev/null
systemctl enable caddy >/dev/null 2>&1
systemctl restart caddy

# remove the uploaded copies (the secrets now live only in /etc/stockastic/env)
shred -u "$S/google.secret.env" "$S/organiser.secret.env" 2>/dev/null || rm -f "$S/google.secret.env" "$S/organiser.secret.env"
rm -rf "$S"

for i in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; then break; fi
  sleep 1
done
echo "app: $(curl -sS http://127.0.0.1:8080/readyz)"
echo "service: $(systemctl is-active stockastic), caddy: $(systemctl is-active caddy)"
echo "listening:"; ss -tlnp | awk 'NR>1 {print "  " $4}' | sort -u
