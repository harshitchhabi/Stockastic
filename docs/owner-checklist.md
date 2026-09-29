# What only you can do

The code side is finished and tested. These are the things that need you: accounts, money, decisions, and rehearsal on the real
machine. Physical things (the venue, its Wi-Fi, devices) are left out on purpose.

## 1. Before you deploy

| Do | Why | How |
|---|---|---|
| Buy a domain name (about ₹500 to ₹1,000 a year) | HTTPS and Google sign-in both need one | Point its DNS `A` record at the server's fixed (Elastic) IP |
| Create the AWS server | The event runs on one machine | `docs/deployment.md`: a `c6i.2xlarge` in `ap-south-1`, Ubuntu 24.04, a `gp3` disk, an Elastic IP |
| Set the AWS security group | The first wall against network floods | Allow only ports 80 and 443 from anywhere and port 22 from your address (or anywhere, with key-only SSH). AWS Shield Standard is on by default and free |
| Run `deploy/harden.sh` | Key-only SSH, firewall, automatic bans, updates | Put your SSH key on the server first, run the script, then open a second terminal and log in **before** closing the first |
| Install Caddy with `deploy/Caddyfile` | HTTPS and request limits | It is already set to `stockastic.dreammerchantsevent.me` |
| Fill in `/etc/stockastic/env` from `deploy/env.example` | The server's settings | Make `JWT_SECRET` 32+ random characters (`openssl rand -hex 32`), the organiser password 12+ characters and not reused. `chmod 600` the file |
| Copy the final data to the server by hand | It is git-ignored on purpose (future prices and news) | `universe.json`, `scenario.json`, `prices.json` in one folder, and `UNIVERSE_PATH` and `SCENARIO_PATH` pointing at them. Run `python tools/verify_final.py` on your computer first: it should say 22 of 22 |
| Install PostgreSQL and set `DATABASE_URL` | The real, reliable system of record: exactly-once writes, and wallet/sign-in history the file cannot give you | `docs/deployment.md`, "PostgreSQL (the durable record)". Same machine is fine and free; keep it listening only on `127.0.0.1` |
| Turn on backups | A copy of the database off the machine | `deploy/backup-postgres.sh` on a cron job (every 5 minutes), to S3, plus an hourly EBS snapshot. Do one restore on a spare machine or database to prove it works — `docs/deployment.md` has the exact commands |

## 2. Choose how people register (pick one or combine)

- **Approved emails (strongest, if you have a list).** Participants page, "Approved emails". Only listed people can register, once each.
- **Event code.** Announce a code in the room and set it on the Participants page. Change it after the room is full.
- **Google sign-in.** Off until you set it up (below). Best for one account per real person, and nobody forgets a password.
- Always: **close registration** once every team is in (it stops new teams; a teammate who arrives late can still join their team with its code while it has room), and keep `MAX_ACCOUNTS` (teams, not people) above the most teams you could get: 1100 covers everyone registering alone.

### Setting up Google sign-in (free, about 20 minutes)

1. Go to the Google Cloud console (console.cloud.google.com), create a project, and open **APIs and Services**.
2. **OAuth consent screen**: choose *External*, fill in the app name, your email, and links to a home page (`https://stockastic.dreammerchantsevent.me/`) and a privacy page (`https://stockastic.dreammerchantsevent.me/privacy.html`, already part of the site: edit `apps/web/public/privacy.html` to add a contact address). Then click **Publish app** so it is in *production*. In testing mode Google allows only 100 people.
3. **Credentials**: create an **OAuth client ID** of type *Web application*. Under *Authorized redirect URIs* add exactly `https://stockastic.dreammerchantsevent.me/api/auth/google/callback`.
4. Copy the client ID and secret into `/etc/stockastic/env`:
   ```
   GOOGLE_CLIENT_ID=...
   GOOGLE_CLIENT_SECRET=...
   GOOGLE_REDIRECT_URL=https://stockastic.dreammerchantsevent.me/api/auth/google/callback
   GOOGLE_ALLOWED_DOMAINS=vitstudent.ac.in    (only @vitstudent.ac.in accounts can sign in with Google)
   SIGNUP_GOOGLE_ONLY=true                    (nobody can register with an email and password, so only real VIT accounts get in)
   ```
5. Restart the server. "Continue with Google" appears on the sign-in page. Test it with your own account and with one outside the allowed domain.
6. Everyone presses **Continue with Google**. Someone new then sees a welcome screen: the leader types a team name and presses
   **Create team**; teammates type the team code from the leader's Team page and press **Join team**. After that, Continue with Google
   takes each person straight in.
7. Ask the college IT team early whether third-party apps are allowed for student accounts. If a test sign-in shows "Access blocked" or
   "admin_policy_enforced", they need to allow your OAuth client ID in the Google Workspace admin console (Security, API controls, App access
   control).

The server checks Google's signature, the client id, the expiry, a one-time value for that sign-in, and that the email is verified. It
was tested against a stand-in for Google, **not against the real one**, so the test in step 5 matters.

## 3. Decisions the rulebook leaves to you

These are set to placeholders and are all one value in `rulebook.json` (or a button in the console):

- **Prizes** are decided by you in person. The console's **Standings** page ranks all teams, the investors and the funds (with a CSV
  download of each), and **Funds** shows every strategy log for the judges.
- **Management fee**: each fund chooses 1 to 2% on its Fund desk (1.5% until it chooses); it is fixed once investors are in.
- **Tie-break order** for the 20th qualifying place (peak value, then fewer trades, then coin toss). Publish it before Phase 1.
- Whether **fund managers may see who invested** in their fund (they see counts and totals only now).
- **What follows a warning** for a team below the 5% share in funds (a second warning could mean disqualification).
- The minimum **device and browser** requirements to publish a day ahead (any current Chrome, Edge, Firefox or Safari works).

## 4. Rehearse on the real machine

1. Load test with `cmd/loadsim -users 1000` and attack test with `cmd/abusesim` against a throwaway copy, from another machine.
2. Kill the server process mid-run (`kill -9`), let systemd restart it, and check nothing was lost.
3. Fill the disk to below `DISK_MIN_FREE_MB` on a test copy and see that trades are refused and the Systems page says so.
4. Run a shortened event end to end with real accounts: register, Phase 1, close Phase 1, form the funds, window 0, Phase 2, the other windows, and the final close. The Control room's **Next step** button does each step.
5. Clear the rehearsal from the Control room: **Start completely fresh** deletes every rehearsal team so everyone registers again on the day (use this if the rehearsal used test accounts), while **Reset the whole event** keeps the teams and puts each back to its starting cash. A server restart on its own never clears anything.

## 5. On the day

- Log in as organiser on a laptop **and** on your phone (a different network) before the event starts.
- Give teams shares in **Shares** if you want anyone to be able to sell.
- Run the event from the **Control room**: each **Next step** click moves it on (nothing moves by itself). After "Phase 1: closed", form the
  funds on the **Funds** page before opening window 0. Watch **Systems** (people connected, disk space, failed saves).
- If a team is stuck out of its account: **Clear sign-in locks**, or reset its password from its page.
- After registration is full: close it.
