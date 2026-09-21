# Deploying for the event

For 300 to 750 people over five hours. The recommendations below come from running the real server under
`apps/api/cmd/loadsim`, not from guesses. Where a number was not measured, it says so.

## What was measured

Real server, real WebSockets, real disk log, on a 12-core Windows laptop (a fast SSD, no reverse proxy).
Every team had its own connection, like a browser.

| | 300 teams at the rulebook pace (2 orders/min each) | 750 teams at **six times** that pace (about 150 orders/second) |
|---|---|---|
| Errors, dropped sockets | **0, 0** | **0, 0** |
| Placing an order (median / 95th / 99th) | 3 ms / 33 ms / 224 ms | 8 ms / 94 ms / 318 ms |
| Reading an order book | 0.5 ms | 2.5 ms |
| Live update reaching the team's own screen | 3 ms median | 8 ms median |
| All teams log in at the same instant | median 0.9 s, worst 1.8 s | median 1.2 s, worst 2.0 s |
| Every team orders the **same company in the same instant** | median 0.6 s, worst 1.1 s | median 2.0 s, worst 4.9 s |
| Server memory | 19 to 59 MB | 23 to 144 MB |
| Traffic to each browser | about 1 KB/s | about 17 KB/s |
| Restart after a crash (about 10,000 orders, 750 accounts) | | **1.1 s**, everything recovered |

The rulebook allows two trades per minute per account, so real traffic is far below the right-hand column.
**Trading itself has a large margin.** The three things that can make people wait are below.

## The three things to watch

1. **A stampede on one company.** Orders in one company are handled one at a time, and each is written to disk
   before it is confirmed. If every team hits the same company in the same second (say, at market open), the
   last team waits for all the others: about 2 to 5 seconds for 750 teams on this laptop's disk. It is safe and
   correct, only slow at the tail. A disk with faster syncs shortens it in proportion (see Storage).
2. **Everyone logging in at once.** Password checking is deliberately slow work (bcrypt). It uses every core, and
   a login storm queues behind it. 750 simultaneous logins took about 2 s worst case on 12 cores. On a 2-core
   server expect roughly six times that (an estimate from the core count, not a measurement). Use 4 or more cores, and ask teams to log in during the briefing
   blocks (the first 20 minutes of the timeline), not at the moment trading opens.
3. **A long restart with a huge log.** Recovery reads the whole log. It is fast (about 20,000 records a second)
   but grows with the event. A worst-case full event is a few hundred thousand orders, so allow 10 to 30 seconds.

## Recommended setup

**One server, in one region, near the venue.** State lives in memory and in one log file, so there must be
exactly one running server. The log refuses to open if a second server tries to use it, which protects you from
an accidental double start.

| | Recommendation | Why |
|---|---|---|
| Machine | AWS `c6i.xlarge` (4 vCPU, 8 GB) or bigger | Cores for the login storm. Memory use is tiny (under 150 MB measured) |
| Region | `ap-south-1` (Mumbai) if the venue is in India | Lowest delay for the participants |
| OS | Ubuntu 24.04 LTS | The server is one static binary: `CGO_ENABLED=0 GOOS=linux go build ./cmd/api` |
| Storage | `gp3` EBS volume, 3000 IOPS, or local NVMe (instance store) | Every order waits for one disk sync, so sync speed sets the stampede delay. NVMe is fastest. For EBS, `io2` is faster than `gp3` |
| HTTPS | **Caddy on the same machine** (`deploy/Caddyfile`) | Automatic certificates, handles WebSockets, queues connection bursts. Needs a domain name |
| Process manager | `systemd` (`deploy/stockastic.service`) | Restarts the server within seconds if it ever stops |
| Time | `chrony` (on by default on Ubuntu) | The event clock runs from the server's time |

## Do we need CloudFront?

**No, not for this event.** The reasons:

- The whole web app is about 110 KB compressed. 750 people loading it once is under 100 MB in total. The server
  already serves it from memory with caching headers, so it does not need help.
- Everything that matters (login, orders, the live WebSocket) cannot be cached. CloudFront would only add another
  hop and another thing to misconfigure while passing WebSockets and login headers through.
- Everyone is in one place, so a worldwide cache network buys nothing.

Add it only if you later want protection from attacks (AWS Shield and a web application firewall) or a public,
worldwide audience. If you do, cache **only** `/assets/*`, and send `/api/*` and `/ws` straight through with no
caching, all headers forwarded, and a long idle timeout.

An **Application Load Balancer** is the other common choice. It works too (set its idle timeout to 3600 seconds
so WebSockets stay open), but with a single server it adds cost and a hop without adding safety.

## Server settings

- **Connections.** Raise the open-file limit (`LimitNOFILE=65535` in the unit file) and the connection queue
  (`deploy/99-stockastic.conf`). On the Windows test machine, 300 brand-new connections in the same instant
  were refused because the default queue is small; Linux needs the same tuning, and Caddy in front absorbs it.
- **Secrets.** Put settings in `/etc/stockastic/env` (`deploy/env.example`), readable only by the service user.
  `JWT_SECRET` must be 32 or more random characters. Set `ALLOW_SIGNUP=false` once teams are registered.
- **Listen address.** The server listens on `127.0.0.1:8080` by default. Keep it that way behind Caddy.

## Backups

The log only ever grows at the end, so copying it while the server runs is safe (a half-written last line is
discarded on restart, and was never confirmed to anyone).

- `deploy/backup.sh` copies it to S3 every minute (turn on bucket versioning). Worst case you lose about a
  minute, and only orders that were confirmed in that minute.
- Also take an EBS snapshot before the event and every hour during it.
- **To recover on a new machine:** create the instance, copy the newest log to `DATA_DIR/stockastic.wal`, and
  start the service. Practise this once.

## Before the event

1. Run `loadsim` against the real machine (not the laptop) with `-users 750`, and again at the rulebook pace.
   The numbers above are a baseline, not a promise for a different disk and CPU.
2. Kill the server with `kill -9` in the middle of the load test, then start it again (systemd does this by
   itself). It must come back within seconds with every order, balance and working order exactly as before, and
   a retry of an order sent before the kill must return the original result instead of trading twice.
3. Restore from a backup onto a spare machine.
4. Turn off automatic reboots and package updates for the event day.
5. Set alarms: disk space, the service being down, and CPU above 80% for five minutes.
6. Have the organiser console open on a second device. The **Systems** page shows people connected, orders and
   trades per minute, order save time, and any company that has stopped.

## What this setup does not do yet

- **Automatic failover.** If the machine dies, recovery is the manual restore above (a few minutes).
- **Postgres.** The log file is the system of record. It sits behind an interface so Postgres can replace it,
  but that is not built.
- **Faster stampedes.** Handling several orders per disk sync inside one company would cut the worst case
  further. It is a bigger change to the matching engine and only worth doing if the rehearsal shows it is needed.
- **Metrics dashboards.** The Systems page is the only live view; there is no Prometheus endpoint yet.
