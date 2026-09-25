# Security and protection against abuse

What protects the event from a team that tries to cheat, jam the server, or break in, how each protection was tested,
and what it cannot do.

## The rule that matters most

The server decides everything. A browser only asks. A trade names a company and a number of shares; the price, the cash
check, the 25% limit, the two-a-minute allowance, who may trade for a fund, and every organiser power are all decided and
checked on the server. Changing what a page shows or what a request says changes nothing the server has not agreed to.

## Protections

| Threat | Protection |
|---|---|
| Reading or changing traffic | HTTPS through Caddy (`deploy/Caddyfile`), HSTS, the app listens on 127.0.0.1 only |
| Stealing or forging a login | Tokens signed with a 32+ character secret, algorithm pinned, session version so sign-out and removal cancel them at once, role and status read live on every request |
| A team acting as organiser | Every organiser route sits behind a server-side check. A test walks the real route table and fails if any route is unprotected |
| Guessing passwords | bcrypt, and 10 wrong guesses at one email lock new sign-ins for that email for 2 minutes. The lock never signs out a team that is already in, so a rival cannot throw someone out mid-trade |
| Filling the server with junk accounts | Optional event code for registration (organisers can change it live), a cap on accounts (`MAX_ACCOUNTS`), organisers can close registration, and one mailbox is one person (case, `+tag` and Gmail dots are ignored) |
| Hiding text in names, or spreadsheets running a name as a formula | Names refuse control, invisible and direction-changing characters and `<` `>`. CSV exports prefix dangerous cells |
| One team flooding the server | A per-account limit of 40 requests a second (organisers 300), a two-trades-a-minute allowance, a per-address limit on anonymous traffic (3,000 a second) |
| A rival in the same room using everyone's allowance | The address limit counts only anonymous traffic. Signed-in requests are limited per account. Wrong-password guesses are limited per email, not per address, so a whole venue on one address is not locked out |
| Password-checking used as a weapon | Made-up emails cost no password work. The rest wait for a slot for at most 4 seconds, then get a "busy, try again" answer instead of building an endless queue |
| WebSocket abuse | At most 4 sockets per account (a newer one replaces the oldest), 4,000 in all, at most 1,500 waiting to log in, 3 seconds to log in, 4 KB per message, and a socket sending more than 20 messages a second is cut off |
| Slow or stalled connections | Header timeout 10 s, request 30 s, response 30 s, headers up to 16 KB. Caddy repeats these limits in front |
| Huge or malformed requests | Request bodies are limited to 64 KB (256 KB at Caddy), JSON is decoded strictly, and quantities, prices, ids and symbols are range and format checked |
| Losing data or filling the disk | Every trade is on disk before it is confirmed, the log is tidied at each start, trades are refused when disk space is low, and the service stops restarting after a crash loop |
| Taking over the machine | Key-only SSH, no root login, firewall with only 80 and 443 open, automatic bans for repeated SSH failures, automatic security updates (`deploy/harden.sh`), and a sandboxed service with no extra rights |
| Known vulnerabilities in code we depend on | `govulncheck` and `npm audit` were run: nothing affects the code. One advisory in a package we do not use (`x/crypto/openpgp`) has no fix and is not reachable |

## How it was tested

- **Route audit** (`TestEveryRouteIsProtected`): every route is called with no login, a fake token and a team's token.
- **Forged tokens**: `alg: none`, wrong signature, edited payload, empty, garbage, oversized.
- **Hostile input**: 18 kinds of bad trade (huge, negative, fractional, wrong type, path and script strings, nested JSON, 200 KB
  bodies, null bytes) and organiser inputs. All refused with a 4xx, with no change to the account.
- **Sign-up**: event code, alias mailboxes, hidden characters, account cap.
- **Limits**: per-account flood, WebSocket per-account, message-flood, oversize frame, never-logged-in socket, total socket cap.
- **Chaos** (`TestChaosThenRestartIsIdentical`, run with the race detector): 24 teams trading, investing and withdrawing at once
  while the organiser freezes, pays, gives shares and reads screens, then a reset in the middle of it. No server error, no data
  race, and a restart rebuilt exactly the same money, holdings, trades and funds.
- **Attack on a real server** (`cmd/abusesim`): while 100 honest teams traded, one machine ran 300 attacking connections at once.
  550,000 unauthenticated requests, 140,000 wrong-password attempts, a flood from one signed-in account, 300 KB request bodies,
  3,000 idle sockets, message-flood sockets and 400 slow-header connections. Honest results were identical to the run without an
  attack (every trade accepted or refused for the same reasons). Honest page loads went from 0.5 ms to a median of 6 ms
  (99th percentile 51 ms). All message-flood sockets were cut off, all slow-header connections were dropped, and the server was
  healthy afterwards.
- **Every screen in a real browser** (Edge, automated): sign-up through the form, Explore with 150 companies, buying, the 25%
  refusal, Holdings, Funds before and after formation, investing on screen, the fund desk, the second team's disabled trade
  button, and every organiser page. No page errors and no server errors.
- **Load**: 700 teams, all logging in at once, all trading, a hard kill and restart: no errors, every trade recovered.

## Loopholes looked for in the game itself

Each of these was tried as a test (`loopholes_test.go` and others). The ones marked "fixed" were open before.

| Attempt | Result |
|---|---|
| Send the same trade many times at once | One trade (retries return the original) |
| Send many different trades at once to beat two a minute | Exactly the allowed number go through |
| Buy in many parallel requests to get past the 25% single-company limit | Cannot: the check runs while the account is locked |
| Invest in a fund in many parallel requests to spend cash twice or exceed 60% in one fund | Cannot: cash never goes negative and the 60% limit holds |
| Withdraw tiny amounts over and over, hoping rounding pays a little extra | No gain was possible, but hardened anyway: payouts round down and the smallest withdrawal is ₹1 (fixed) |
| Fill the log with strategy entries, disputes, profile rewrites or endless fund operations | **Fixed:** at most 3 strategy entries per checkpoint, 5 open disputes, one profile change every 5 seconds, 30 fund operations a minute (refused attempts do not count) |
| Name a fund after a real company or bank | **Fixed:** well-known names are refused (Section 8) |
| Never put 5% into funds | Not blocked (the rulebook only warns), but **now visible**: the organiser sees each investor's share in funds, a filter for those below the minimum, and a note on the Prize 2 table |
| Leave a fund and drop below 5% | Refused with the reason. To switch funds, invest in the new one first, then withdraw |
| Diversify only in the last minute to win Prize 4 | **Fixed:** diversification is now averaged over the whole event |
| Read future prices or news | Only current prices, and history up to now, are public. The schedule and news data are on organiser routes only, which the route audit checks |
| Read the data files or source through the web server | Path tricks all fail |
| Inject script through a name, headline or fund text | Text is shown as text, and a Content-Security-Policy stops any script that is not the app's own |
| Trade as the other team of a fund | Refused: only the trader places trades |
| A fund manager leaking the early news | Cannot be stopped by the server (it is a rules matter): the organiser sees who is in each fund and can remove a team |

## What it cannot do

- **Volumetric attacks from many machines.** Nothing running on the server can stop a flood that fills the network link before
  it arrives. Use an AWS security group that allows only ports 80 and 443 (and SSH from your address), and AWS Shield Standard,
  which is on by default for EC2 at no cost, handles the common network-level floods. If you want more, put Cloudflare's free
  plan in front (it needs a domain).
- **A rival inside the venue.** Everyone behind one address looks the same to the server. The protections above are built so
  that one person's flood cannot use up other people's allowances, but a very large flood from inside the room can still slow the
  shared Wi-Fi itself. Ask the venue to keep the network for participants only and to block participants from each other.
- **Lock-out griefing of new sign-ins.** Someone who knows a team's email can make ten wrong guesses and stop that team signing in
  again for 2 minutes. Teams that are already signed in are not affected. Organisers can reset a password or sign a team in
  again from the console.
- **Two people using one account, or one person using two accounts with different mailboxes.** That is a rules matter. The
  alias check, the event code and the organiser's ability to see who is online and remove a team are the tools for it.
- **Caddy and the hardening script were written and syntax-checked but not run.** They need a real server. Run
  `deploy/harden.sh` and the rehearsal in `docs/deployment.md` on the real machine and check that you can still log in over SSH from
  a second terminal before you close the first.

## Working from any address

Nothing in the app is tied to an address. The organiser console works from any network or device that can reach the site (a
phone hotspot works if the venue Wi-Fi fails), and the organiser's sign-in is never locked by wrong guesses (the password is 12
or more characters). Tokens are not bound to an address. On the server, `deploy/harden.sh` allows SSH from anywhere with a key
by default (with automatic bans for repeated failures); setting `ADMIN_IP` is optional and only if you want SSH limited to one
fixed address. If you change networks, run `ufw allow from <new address> to any port 22` from the AWS console session, or leave
`ADMIN_IP` unset.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `SIGNUP_CODE` | none | Registration needs this code. Organisers can change it live |
| `MAX_ACCOUNTS` | 2000 | Most accounts that can exist |
| `MAX_SOCKETS` | 4000 | Most live connections |
| `MAX_SOCKETS_PER_ACCOUNT` | 4 | Most sockets one account can hold |
| `TRUSTED_PROXIES` | `127.0.0.1,::1` | Addresses whose `X-Forwarded-For` header is believed |
| `DISK_MIN_FREE_MB` | 200 | Trades are refused when free disk space is lower |
