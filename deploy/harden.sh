#!/usr/bin/env bash
# Hardens a fresh Ubuntu 22.04 or 24.04 server for the event: key-only SSH, a firewall that shows only the web ports,
# automatic brute-force bans, automatic security updates, and safer network settings.
#
# Run it as root ON THE SERVER, from a session you keep open, after you have put your public key in the admin user's
# ~/.ssh/authorized_keys and checked that you can log in with it:
#
#   sudo ADMIN_USER=ubuntu ADMIN_IP=203.0.113.10 bash harden.sh
#
# ADMIN_USER  the account you log in with over SSH (required)
# ADMIN_IP    optional: if set, SSH is allowed only from this address (best, if your address is fixed)
#
# It refuses to turn passwords off unless that user already has an SSH key, so it cannot lock you out that way.
set -euo pipefail

: "${ADMIN_USER:?set ADMIN_USER to the account you log in with}"
[ "$(id -u)" -eq 0 ] || { echo "run as root"; exit 1; }
HOME_DIR="$(getent passwd "$ADMIN_USER" | cut -d: -f6)"
[ -s "$HOME_DIR/.ssh/authorized_keys" ] || { echo "$ADMIN_USER has no SSH key in $HOME_DIR/.ssh/authorized_keys. Add one first, or this would lock you out."; exit 1; }

export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y ufw fail2ban unattended-upgrades chrony

# ---- SSH: keys only, no root, few tries ----
cat >/etc/ssh/sshd_config.d/99-stockastic.conf <<EOF
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
MaxAuthTries 3
LoginGraceTime 20
MaxStartups 5:50:20
X11Forwarding no
AllowTcpForwarding no
AllowAgentForwarding no
ClientAliveInterval 300
ClientAliveCountMax 2
AllowUsers $ADMIN_USER
EOF
sshd -t
systemctl reload ssh || systemctl reload sshd

# ---- firewall: only web ports are open to the world ----
ufw --force reset
ufw default deny incoming
ufw default allow outgoing
ufw allow 80/tcp
ufw allow 443/tcp
if [ -n "${ADMIN_IP:-}" ]; then
  ufw allow from "$ADMIN_IP" to any port 22 proto tcp
else
  ufw limit 22/tcp # at most 6 new SSH connections from one address in 30 seconds
fi
ufw --force enable

# ---- ban addresses that keep failing at SSH ----
cat >/etc/fail2ban/jail.d/stockastic.local <<EOF
[sshd]
enabled  = true
maxretry = 4
findtime = 10m
bantime  = 1h
EOF
systemctl enable --now fail2ban
systemctl restart fail2ban

# ---- automatic security updates, and a correct clock (the event runs on the server's time) ----
dpkg-reconfigure -f noninteractive unattended-upgrades
systemctl enable --now chrony

# ---- safer network behaviour, and room for many connections ----
install -m 0644 "$(dirname "$0")/99-stockastic.conf" /etc/sysctl.d/99-stockastic.conf
sysctl --system >/dev/null

# ---- the log cannot fill the disk ----
mkdir -p /etc/systemd/journald.conf.d
install -m 0644 "$(dirname "$0")/journald-stockastic.conf" /etc/systemd/journald.conf.d/stockastic.conf
systemctl restart systemd-journald

echo
echo "Done. Test it from ANOTHER terminal before you close this one:  ssh $ADMIN_USER@<this server>"
echo "Then check:  ufw status verbose   fail2ban-client status sshd   ss -tlnp   (the app must listen on 127.0.0.1 only)"
