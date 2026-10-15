# Redeploying Stockastic from nothing

This is a runbook for an AI agent (or a person) to put Stockastic back on the internet at
`https://stockastic.dreammerchantsevent.me` after the server has been taken down. Follow it top to bottom. Every
command here is one that was run for the first live deployment (October 2026) and worked; the notes under each step
are the problems that came up then.

The result: one AWS EC2 server running Ubuntu 24.04 with the Go app, PostgreSQL 16 and Caddy (HTTPS), Google sign-in
limited to `@vitstudent.ac.in`, backups every 5 minutes, verified by a full 1,050-person rehearsal.

## 0. Rules for the agent

- **Ask the owner before anything that costs money or cannot be undone**: launching or terminating servers,
  releasing IPs, deleting snapshots, wiping the database. Never touch AWS resources that are not Stockastic's.
- **Never print, commit or upload secrets** anywhere except onto the server's own settings file. The Google client
  secret and organiser password live only in git-ignored `deploy/*.secret.env` files on the owner's computer.
- **Never mention any AI assistant** in commits, code, docs or pull requests, and never add co-author lines. The owner
  presents this project as their own.
- Commit with plain descriptive messages; do not commit `apps/api/scenarios/final/` or any `*.secret.env` file (both
  are in `.gitignore`; check `git status` before every commit).

## 1. What you need before starting

| Item | Where it is | If it is missing |
|---|---|---|
| The repository | `github.com/harshitchhabi/Stockastic`, branch `main` | Clone it |
| Final event data: `universe.json`, `scenario.json`, `prices.json` | `apps/api/scenarios/final/` on the owner's computer (never in git) | Regenerate from the organisers' workbook with `tools/import_final.py`, then `python tools/verify_final.py` must say 22 of 22 |
| `deploy/google.secret.env` | Owner's computer (git-ignored) | Recreate it (section 3) from the Google Cloud console |
| `deploy/organiser.secret.env` | Owner's computer (git-ignored) | Recreate it with a new random password (section 3) |
| SSH key `stockastic-key.pem` | Deleted with the key pair at takedown | Section 2a makes a new one |
| AWS CLI signed in to account `537735702475`, region `ap-south-1` | Owner's computer | `aws configure`; check with `aws sts get-caller-identity`. Nothing else is on AWS: section 2 creates it all |
| Go 1.22+, Node 20+, npm | Owner's computer | Install |
| DNS for `dreammerchantsevent.me` | The owner's domain registrar (not Route 53; the AWS user has no Route 53 permission) | Only the owner can change it |

The AWS user `harshitchhabi` **cannot** use Route 53, SSM parameters, Service Quotas or IAM. Plan around that: the owner
edits DNS, and S3 backups need a role the owner must create.

## 2. AWS: the server, from an empty account

Nothing from the last deployment is kept on AWS: no server, disk, snapshot, IP, key pair or security group. Build
everything below. Commands are for **Git Bash** with the AWS CLI (prefix `MSYS_NO_PATHCONV=1` where a value starts
with `/`); each step prints the id the next step needs. Ask the owner before step 2b.4 (it starts billing).

First confirm the account and region, and that nothing named `stockastic` already exists (reuse it if it does):

```sh
aws sts get-caller-identity                     # account 537735702475
aws configure get region                        # ap-south-1 (Mumbai)
aws ec2 describe-instances --filters Name=tag:Name,Values=stockastic --query "Reservations[].Instances[].[InstanceId,State.Name]" --output text
aws ec2 describe-key-pairs --query "KeyPairs[].KeyName" --output text
aws ec2 describe-security-groups --query "SecurityGroups[].GroupName" --output text
```

### 2a. Key pair (the only way into the server)

```sh
mkdir -p ~/.ssh
aws ec2 create-key-pair --key-name stockastic-key --key-type ed25519 --query KeyMaterial --output text > ~/.ssh/stockastic-key.pem
```

AWS keeps no copy of the private key: this file is the only one. On Windows, copy it to
`C:\Users\<user>\.ssh\stockastic-key.pem` (Git Bash's `~` may be a different folder, check with `echo $HOME`), then in
PowerShell restrict it, or OpenSSH refuses it:

```powershell
icacls "$env:USERPROFILE\.ssh\stockastic-key.pem" /inheritance:r /grant:r "$($env:USERNAME):(R)"
```

Use Windows' own `ssh.exe`/`scp.exe` from PowerShell for everything after this; Git Bash's ssh could not read the
restricted key ("error in libcrypto"). Tell the owner to back the key file up privately.

### 2b. Security group, image, server

1. **Security group** `stockastic-web` in the default VPC:
   ```sh
   VPC=$(aws ec2 describe-vpcs --filters Name=is-default,Values=true --query "Vpcs[0].VpcId" --output text)
   SG=$(aws ec2 create-security-group --group-name stockastic-web --description "Stockastic: web from anywhere, SSH key-only" --vpc-id $VPC --query GroupId --output text)
   aws ec2 authorize-security-group-ingress --group-id $SG --ip-permissions \
     "IpProtocol=tcp,FromPort=80,ToPort=80,IpRanges=[{CidrIp=0.0.0.0/0}]" \
     "IpProtocol=tcp,FromPort=443,ToPort=443,IpRanges=[{CidrIp=0.0.0.0/0}]" \
     "IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=0.0.0.0/0,Description=SSH key-only}]"
   echo $SG
   ```
   SSH is open to every network on purpose (the owner's connection changes); section 4 makes it key-only with bans.
   A safety check may refuse to open port 22 to everyone until the owner confirms it; if so, ask them.
2. **Newest Ubuntu 24.04 image** (64-bit x86):
   ```sh
   AMI=$(MSYS_NO_PATHCONV=1 aws ec2 describe-images --owners 099720109477 --filters "Name=name,Values=ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*" "Name=state,Values=available" --query "sort_by(Images,&CreationDate)[-1].ImageId" --output text)
   echo $AMI
   ```
3. **Choose the size**: **c6i.xlarge** (4 vCPU, 8 GB; about ₹15 an hour). Tested: 1,050 players use a fraction of it.
4. **Launch** (ask the owner first):
   ```sh
   MSYS_NO_PATHCONV=1 aws ec2 run-instances --image-id $AMI --instance-type c6i.xlarge --key-name stockastic-key \
     --security-group-ids $SG \
     --block-device-mappings "DeviceName=/dev/sda1,Ebs={VolumeSize=40,VolumeType=gp3,Encrypted=true,DeleteOnTermination=true}" \
     --metadata-options "HttpTokens=required,HttpEndpoint=enabled" \
     --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=stockastic}]" "ResourceType=volume,Tags=[{Key=Name,Value=stockastic-disk}]" \
     --query "Instances[0].InstanceId" --output text
   ```
   Save the instance id as `I`, then `aws ec2 wait instance-status-ok --instance-ids $I` (2 to 4 minutes).

   **If AWS answers "not eligible for Free Tier"**, the account is on the free plan, which only allows small types.
   Either the owner upgrades the plan (Billing console → upgrade), or use `m7i-flex.large` (2 vCPU, 8 GB, the largest
   free-plan type; untested at full scale, so run the section 7 rehearsal before relying on it).

### 2c. A fixed IP

```sh
ALLOC=$(aws ec2 allocate-address --domain vpc --tag-specifications "ResourceType=elastic-ip,Tags=[{Key=Name,Value=stockastic}]" --query AllocationId --output text)
aws ec2 associate-address --instance-id $I --allocation-id $ALLOC
aws ec2 describe-addresses --allocation-ids $ALLOC --query "Addresses[0].PublicIp" --output text
```

Without a fixed IP the address changes whenever the server stops, and the domain breaks. The new IP will not be
the old `13.202.208.196`. Give it to the owner for DNS.

### 2d. DNS (owner only)

At the registrar for `dreammerchantsevent.me`: an **A record**, host `stockastic`, value = the new IP, TTL 5 minutes.
Do not touch the other records (the main domain points to GitHub Pages). Check from outside until it shows the new IP:

```sh
nslookup stockastic.dreammerchantsevent.me 8.8.8.8
```

HTTPS (section 5) cannot work until this resolves to the new IP.

### 2e. First login

```powershell
$k = "$env:USERPROFILE\.ssh\stockastic-key.pem"; $h = "ubuntu@<new IP>"
ssh -i $k -o StrictHostKeyChecking=accept-new $h "lsb_release -ds; nproc; free -h | sed -n 2p; df -h / | tail -1"
```

If the old IP was ever used before, remove the stale entry from `known_hosts` first (`ssh-keygen -R <ip>`).

## 3. The private settings files (on the owner's computer)

`deploy/google.secret.env` (git-ignored):

```
GOOGLE_CLIENT_ID=169842652892-mb3ecch9ja7mjm0th86sc4udl42s96g2.apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=<from Google Cloud console, Clients → Stockastic web>
GOOGLE_REDIRECT_URL=https://stockastic.dreammerchantsevent.me/api/auth/google/callback
GOOGLE_ALLOWED_DOMAINS=vitstudent.ac.in
SIGNUP_GOOGLE_ONLY=true
```

The Google project is already set up (External, **In production**, scopes openid/email/profile, no logo). It keeps
working as long as the domain stays the same. If the domain changes, add the new redirect URI on the Google client
and the new domain under Branding → Authorised domains.

`deploy/organiser.secret.env` (git-ignored); if it is lost, make a new one with a random 24-character password:

```
ADMIN_EMAIL=organiser@dreammerchantsevent.me
ADMIN_PASSWORD=<random>
ADMIN_NAME=Organiser
```

## 4. Harden the server

From PowerShell on the owner's computer (`$k` = the key path, `$h` = `ubuntu@<elastic IP>`):

```powershell
ssh -i $k -o StrictHostKeyChecking=accept-new $h "mkdir -p ~/deploy"
scp -i $k deploy/harden.sh deploy/99-stockastic.conf deploy/journald-stockastic.conf deploy/stockastic.service deploy/Caddyfile deploy/backup-postgres.sh deploy/server-setup.sh deploy/server-install.sh deploy/server-backup-setup.sh deploy/server-check.sh deploy/server-wipe.sh "${h}:~/deploy/"
ssh -i $k $h "sed -i 's/\r$//' ~/deploy/*.sh ~/deploy/*.conf ~/deploy/Caddyfile ~/deploy/stockastic.service; sudo ADMIN_USER=ubuntu bash ~/deploy/harden.sh"
```

- **Always strip Windows line endings** (the `sed` above) before running anything copied from Windows.
- `harden.sh` turns off SSH passwords and root login, enables `ufw` (80, 443, rate-limited 22), `fail2ban`,
  automatic updates and the network tuning. Do **not** pass `ADMIN_IP` (the owner wants SSH from any network).
- Then check, from a **new** connection, that the key still works and that a password login is refused.

## 5. Install PostgreSQL, Caddy and the app

1. **PostgreSQL 16 and Caddy, user, folders, database** (makes its own random database password in
   `/root/stockastic-db-password`):
   ```powershell
   ssh -i $k $h "sudo bash ~/deploy/server-setup.sh"
   ```
2. **Build** on the owner's computer (the web app must be built first; it is embedded into the Go binary):
   ```powershell
   cd apps/web; npm ci; npm run build
   cd ../api; $env:CGO_ENABLED="0"; $env:GOOS="linux"; $env:GOARCH="amd64"
   go build -trimpath -ldflags "-s -w" -o stockastic-linux ./cmd/api
   Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
   ```
3. **Upload and install** (the secret files go to a private temporary folder and are shredded after install):
   ```powershell
   ssh -i $k $h "rm -rf /tmp/stk; mkdir -m 700 /tmp/stk"
   scp -i $k apps/api/stockastic-linux apps/api/scenarios/final/universe.json apps/api/scenarios/final/scenario.json apps/api/scenarios/final/prices.json deploy/google.secret.env deploy/organiser.secret.env "${h}:/tmp/stk/"
   ssh -i $k $h "sed -i 's/\r$//' /tmp/stk/*.env; sudo bash ~/deploy/server-install.sh"
   ```
   `server-install.sh` installs the binary to `/opt/stockastic/api`, the data to `/etc/stockastic/final/`, writes
   `/etc/stockastic/env` (mode 0640, root:stockastic; makes the login-signing secret on the server), installs the
   systemd unit and the Caddyfile, starts both, and prints the listening ports. Expected: `app: {"ok":true}`, ports
   443, 80, 22 public; 8080, 5432, 2019 on 127.0.0.1 only.
4. Delete the local `stockastic-linux` build afterwards.

Caddy gets the HTTPS certificate from Let's Encrypt by itself within a minute, **once DNS points at the server**.

## 6. Backups

```powershell
ssh -i $k $h "sudo bash ~/deploy/server-backup-setup.sh"
```

A `pg_dump` every 5 minutes to `/var/backups/stockastic/latest.dump` plus hourly copies for 24 hours. Optionally take an EBS
snapshot of the disk before the event (`aws ec2 create-snapshot --volume-id <root volume> --description ...`); delete it at takedown. Off-site backups to S3
(`deploy/backup-postgres.sh`) need an IAM role on the instance that the owner must create; the current AWS user
cannot.

## 7. Verify (do not skip)

1. **From outside**:
   ```sh
   curl -s https://stockastic.dreammerchantsevent.me/healthz          # {"ok":true}
   curl -s https://stockastic.dreammerchantsevent.me/api/status       # googleEnabled true, googleOnlySignup true
   curl -s -o /dev/null -w "%{redirect_url}\n" https://stockastic.dreammerchantsevent.me/api/auth/google/start   # accounts.google.com ... hd=vitstudent.ac.in
   ```
2. **On the server**: `bash ~/deploy/server-check.sh` (certificate, services, app errors, limits, resources).
3. **Organiser login**: sign in with the email and password from `deploy/organiser.secret.env` through the
   "Organisers" link; the Control room and Systems pages load.
4. **The owner's phone test**: Continue with Google with a `@vitstudent.ac.in` account works; a personal Gmail is
   refused. Only the owner can do this.
5. **Full rehearsal against the live site** (creates 1,050 test players; wipe afterwards):
   ```powershell
   cd apps/api; go build -o mockevent.exe ./cmd/mockevent
   $secret = (ssh -i $k $h "sudo cat /root/stockastic-jwt-secret").Trim()   # in memory only, never written to a file
   ./mockevent.exe -url https://stockastic.dreammerchantsevent.me -admin-email organiser@dreammerchantsevent.me -admin-password <pw> -secret $secret -teams 350 -teammates 2 -save prod-final.json
   ```
   Expect about 94 checks, all PASS (a real team that already exists will show as one extra team in the final count).
6. **Crash test**: `sudo systemctl kill -s KILL stockastic` on the server; it must be back within about 4 seconds, and
   `./mockevent.exe ... -compare prod-final.json` must report every team and fund unchanged.
7. **Wipe all test data and replace the signing secret**:
   ```powershell
   ssh -i $k $h "sudo bash ~/deploy/server-wipe.sh"
   ```
   Expected: 0 teams, clock not started, only the organiser account. Delete `prod-final.json`. Optionally snapshot
   this clean state (section 6) for a quick rebuild if something breaks before the event.

## 8. Things that went wrong last time, and the fixes already in the code

| Symptom | Cause | What to do |
|---|---|---|
| `Permission denied (publickey)` with the right key | Git Bash's ssh and Windows' `ssh.exe` look in different home folders, or the key's permissions are too open | Use PowerShell's `ssh.exe` with the key in `C:\Users\<user>\.ssh`, restricted with `icacls` |
| Scripts fail with `$'\r': command not found` | Windows line endings | `sed -i 's/\r$//'` on the server before running |
| 503 errors for a few seconds after a crash | (fixed) Caddy's health check marked the app down | The Caddyfile now uses `lb_try_duration 20s`; keep it |
| Long PowerShell one-liners with nested quotes break | PowerShell and bash quoting together | Put the remote commands in a `.sh` file, copy it up, run it |
| Local PostgreSQL on Windows won't listen on 5433 | Windows refused the port after a reboot | Use another port (5439 worked) for local tests only; the server is unaffected |
| Sign-ins "server busy" with 1,000 at once | (fixed) password hashing for Google accounts | Google accounts have no password now; nothing to do |

## 9. Running the event, and taking everything down

- Start the instance in AWS before the event; the app, database, Caddy and backups start by themselves. Sign in as
  organiser and check Systems.
- After a rehearsal with test accounts: `server-wipe.sh` (or Start completely fresh in the Control room).
- Between events, if the owner wants to keep it: **stop** the instance (disk and IP still cost about ₹650 a month with
  GST; running costs about ₹15 an hour).

**Taking it down completely** (the owner's choice: nothing is kept on AWS). Ask the owner first; none of this can be
undone. If the event's results matter, first copy them off the server:

```powershell
ssh -i $k $h "sudo -u postgres pg_dump -Fc -d stockastic -f /tmp/final.dump && sudo chmod 644 /tmp/final.dump"
scp -i $k "${h}:/tmp/final.dump" .\stockastic-final.dump        # keep privately; restore with pg_restore
```

Then delete everything, in this order:

```sh
I=$(aws ec2 describe-instances --filters Name=tag:Name,Values=stockastic "Name=instance-state-name,Values=pending,running,stopping,stopped" --query "Reservations[0].Instances[0].InstanceId" --output text)
VOL=$(aws ec2 describe-instances --instance-ids $I --query "Reservations[0].Instances[0].BlockDeviceMappings[0].Ebs.VolumeId" --output text)
ALLOC=$(aws ec2 describe-addresses --filters Name=instance-id,Values=$I --query "Addresses[0].AllocationId" --output text)
ASSOC=$(aws ec2 describe-addresses --allocation-ids $ALLOC --query "Addresses[0].AssociationId" --output text)

aws ec2 disassociate-address --association-id $ASSOC
aws ec2 release-address --allocation-id $ALLOC                    # the IP
aws ec2 terminate-instances --instance-ids $I                     # the server
aws ec2 wait instance-terminated --instance-ids $I
aws ec2 delete-volume --volume-id $VOL 2>/dev/null || true        # the disk (usually already gone with the server)
for s in $(aws ec2 describe-snapshots --owner-ids self --filters Name=volume-id,Values=$VOL --query "Snapshots[].SnapshotId" --output text); do aws ec2 delete-snapshot --snapshot-id $s; done
aws ec2 delete-security-group --group-name stockastic-web          # the firewall group
aws ec2 delete-key-pair --key-name stockastic-key                  # the key pair (the .pem file is now useless)
```

Check nothing is left that costs money:

```sh
aws ec2 describe-instances --query "Reservations[].Instances[?State.Name!='terminated'].InstanceId" --output text
aws ec2 describe-volumes --query "Volumes[].VolumeId" --output text
aws ec2 describe-snapshots --owner-ids self --query "Snapshots[].SnapshotId" --output text
aws ec2 describe-addresses --query "Addresses[].PublicIp" --output text
```

All four must print nothing. Then the owner removes the `stockastic` A record at the registrar. Keep, on the owner's
computer and backed up privately: `deploy/google.secret.env`, `deploy/organiser.secret.env`,
`apps/api/scenarios/final/`, and any `stockastic-final.dump`. The Google Cloud project costs nothing and can stay as it
is; it keeps working for the same domain. Next time, start this runbook at section 1.

## 10. Where to learn more

`docs/deployment.md` (sizing, database guarantees, recovery), `docs/security.md` (threats and limits),
`docs/owner-checklist.md` (event-day checklist), `docs/admin-api.md` (organiser routes), `deploy/env.example`
(every setting).
