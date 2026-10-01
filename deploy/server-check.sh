#!/usr/bin/env bash
# Health check of the live server: certificate, services, app log, resources.
echo "--- certificate"
echo | openssl s_client -connect stockastic.dreammerchantsevent.me:443 -servername stockastic.dreammerchantsevent.me 2>/dev/null | openssl x509 -noout -issuer -subject -enddate
echo "--- services"
echo "stockastic: $(systemctl is-active stockastic)  caddy: $(systemctl is-active caddy)  postgresql: $(systemctl is-active postgresql)  fail2ban: $(systemctl is-active fail2ban)"
echo "--- app warnings and errors (last 5)"
sudo journalctl -u stockastic --no-pager -o cat | grep -E '"level":"(ERROR|WARN)"|panic' | tail -5
echo "(end)"
echo "--- caddy certificate events"
sudo journalctl -u caddy --no-pager -o cat | grep -oE 'certificate obtained successfully[^,]*|"error":"[^"]{0,120}' | tail -3
echo "--- limits applied to the running app"
PID=$(systemctl show -p MainPID --value stockastic)
grep -E 'Max open files' /proc/$PID/limits
echo "somaxconn=$(sysctl -n net.core.somaxconn)  port range=$(sysctl -n net.ipv4.ip_local_port_range | tr '\t' '-')"
echo "--- resources"
free -h | sed -n 2p
df -h / | tail -1
