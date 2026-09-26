# Deploying for the event

For 300 to 750 people over five hours. The recommendations below come from running the real server under
`apps/api/cmd/loadsim`, not from guesses. Where a number was not measured, it says so.

## What was measured

Real server, real WebSockets, real disk log, on a 12-core Windows laptop (a fast SSD, no reverse proxy), with the
150-company mock data. Every team had its own connection, like a browser.

| | 300 teams at the rulebook pace (2 trades/min each) | 750 teams asking for **six times** that pace |
|---|---|---|
| Errors, dropped sockets | **0, 0** | **0, 0** |
| A trade (median / 95th / 99th) | 3.7 ms / 24 ms / 83 ms | 3.1 ms / 5.8 ms / 12 ms |
| Reading a portfolio | 0.5 ms | 0.5 ms |
| Trade to confirmation on the team's own screen | 4 ms median | 3 ms median |
| All teams log in at the same instant | median 0.8 s, worst 1.1 s | median 1.0 s, worst 1.1 s |
| Every team trades the **same company in the same instant** | median 10 ms, worst 62 ms | |
| Trades the two-a-minute limit refused (correctly) | 0 | 5,248 of 6,748 |
| Restart after a hard kill (751 accounts, 112,500 grants, 1,500 trades) | | a few seconds, everything recovered |
| Log size across three hard restarts in a row | | unchanged (17.4 MB each time) |

Trading has a very large margin: a trade is one lock on one account, and there is no order book to search.
Prices are pushed to everyone in one small message each time they change (once a minute at the default), so
network traffic per browser is close to nothing. The two things that can still make people wait are below.

## The two things to watch

1. **Everyone logging in at once.** Password checking is deliberately slow work (bcrypt). It uses every core, and
   a login storm queues behind it. 750 simultaneous logins took about 1.1 s worst case on 12 cores. On a 2-core
   server expect roughly six times that (an estimate from the core count, not a measurement). Use 4 or more cores, and ask teams to log in during the briefing
   blocks (the first 20 minutes of the timeline), not at the moment trading opens.
2. **A long restart with a huge log.** Recovery reads the whole log. It is fast but grows with the event. Trades, fund
   events, the once-a-minute fund series and the audit log are kept in full, so allow up to 30 seconds after a full event.

## Secure the server first

Run `deploy/harden.sh` on a fresh Ubuntu server (key-only SSH, firewall with only the web ports open, automatic bans,
automatic updates). `docs/security.md` says what is protected and what is not.

## Recommended setup

**One server, in one region, near the venue.** State lives in memory and in one log file, so there must be
exactly one running server. The log refuses to open if a second server tries to use it, which protects you from
an accidental double start.

| | Recommendation | Why |
|---|---|---|
| Machine | AWS `c6i.xlarge` (4 vCPU, 8 GB) or bigger | Cores for the login storm. Memory use is tiny (under 150 MB measured) |
| Region | `ap-south-1` (Mumbai) if the venue is in India | Lowest delay for the participants |
| OS | Ubuntu 24.04 LTS | The server is one static binary: `CGO_ENABLED=0 GOOS=linux go build ./cmd/api` |
| Storage | `gp3` EBS volume, 3000 IOPS, or local NVMe (instance store) | Every trade waits for one disk sync, so sync speed sets the delay. NVMe is fastest. For EBS, `io2` is faster than `gp3` |
| HTTPS | **Caddy on the same machine** (`deploy/Caddyfile`) | Automatic certificates, handles WebSockets, queues connection bursts. Needs a domain name |
| Process manager | `systemd` (`deploy/stockastic.service`) | Restarts the server within seconds if it ever stops, and gives up after 20 starts in 5 minutes so a crash loop cannot fill the disk |
| Time | `chrony` (on by default on Ubuntu) | The event clock runs from the server's time |

## Do we need CloudFront?

**No, not for this event.** The reasons:

- The whole web app is about 110 KB compressed. 750 people loading it once is under 100 MB in total. The server
  already serves it from memory with caching headers, so it does not need help.
- Everything that matters (login, trades, the live WebSocket) cannot be cached. CloudFront would only add another
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

## PostgreSQL (the durable record)

Set `DATABASE_URL` and the server stores everything in PostgreSQL 14 or newer instead of the log file.

**Choose one:**
- *Same machine (cheapest, simplest):* install PostgreSQL on the server, keep its data on the EBS volume, listen only
  on `127.0.0.1`. Use `sslmode=disable`.
- *Amazon RDS (recommended if you can afford it):* a `db.m6i.large` single-AZ instance with 50 GB storage is plenty.
  Turn on automated backups (point-in-time recovery, 7 days) and use `sslmode=require`. Multi-AZ adds automatic
  failover of the database for roughly double the price.

**One time setup** (as a database administrator):
```sql
CREATE ROLE stockastic LOGIN PASSWORD 'a long random password';
CREATE DATABASE stockastic OWNER stockastic;
```
The server creates its own tables on first start. It needs to create tables, so keep the role as the owner.

**What is guaranteed.** A trade or any other change is acknowledged only after PostgreSQL has committed it to disk
(`synchronous_commit` is on for the server's writes; do not switch it off in the database). If the connection drops in
the middle of a write the server reconnects and retries for up to 20 seconds, and every record has its own id so a retry
can never store it twice. If the database stays unreachable, or another server takes the write lock, the server stops
at once (exit code 3) and systemd restarts it; nothing is acknowledged that is not stored. `/readyz` and the Systems page
report a database that has stopped accepting writes.

**One server only.** A second copy of the server pointed at the same database cannot start (it fails to get the write
lock). This protects against two servers acting on the same event.

**Lookup tables.** `accounts`, `trades`, `ledger_entries`, `wallet_history`, `activity`, `price_ticks`, `funds`,
`fund_flows`, `audit`, `news_releases`, `disputes`, `freezes` and the views `v_positions`, `v_cash_change`,
`v_team_activity`, `v_shared_addresses` are filled from the `events` table in the background. They never hold password
hashes. If they ever look wrong they can be emptied and rebuilt from `events` (the platform does not depend on
them). You can query them directly, for example every team's cash change: `SELECT * FROM v_cash_change;`.

**Backups.** `deploy/backup-postgres.sh` runs `pg_dump` and copies it to S3; put it on a cron job every 5 minutes
(turn on bucket versioning). On RDS also keep its automated backups (point-in-time recovery) turned on; on the
same-machine setup also take an EBS snapshot before the event and hourly during it.

**To recover on a new machine or database:**
```sh
createdb -O stockastic stockastic          # an empty database
aws s3 cp s3://your-bucket/stockastic.dump /tmp/stockastic.dump
pg_restore --dbname="$DATABASE_URL" /tmp/stockastic.dump
```
Then start the server; it replays `events` and the lookup tables catch up on their own. Practise this once on a
spare database before the event — a restore that has never been tried is not a backup.

**Moving an event that already started on the log file:** stop the server, run
`walimport -wal /var/lib/stockastic/data/stockastic.wal -db "$DATABASE_URL"` (an empty database is required), set
`DATABASE_URL`, start the server, and check a few teams before opening the market.

**Test it before the event:** with a spare database, run
`TEST_DATABASE_URL=postgres://... go test ./internal/pgstore ./internal/httpapi` (these tests wipe that database
and cover connections dying mid-write, a lost write lock, and a restart rebuilding identical state). They do not
run `pg_dump`/`pg_restore` themselves, so do that round trip by hand once, as above, before the event.

## Backups (log file mode)

The log only ever grows at the end, so copying it while the server runs is safe (a half-written last line is
discarded on restart, and was never confirmed to anyone).

- `deploy/backup.sh` copies it to S3 every minute (turn on bucket versioning). Worst case you lose about a
  minute, and only trades that were confirmed in that minute.
- Also take an EBS snapshot before the event and every hour during it.
- **To recover on a new machine:** create the instance, copy the newest log to `DATA_DIR/stockastic.wal`, and
  start the service. Practise this once.

## Before the event

1. Run `loadsim` against the real machine (not the laptop) with `-users 750`, and again at the rulebook pace.
   The numbers above are a baseline, not a promise for a different disk and CPU.
2. Kill the server with `kill -9` in the middle of the load test, then start it again (systemd does this by
   itself). It must come back within seconds with every trade, balance, fund position and price exactly as before, and
   a retry of a trade sent before the kill must return the original result instead of trading twice.
3. Restore from a backup onto a spare machine.
4. Turn off automatic reboots and package updates for the event day.
5. Set alarms: disk space, the service being down, and CPU above 80% for five minutes.
6. Have the organiser console open on a second device. The **Systems** page shows people connected, trades per
   minute, trade save time, free disk space and price updates.

## What this setup does not do yet

- **Automatic failover.** If the machine dies, recovery is the manual restore above (a few minutes).
- **Metrics dashboards.** The Systems page is the only live view; there is no Prometheus endpoint yet.
