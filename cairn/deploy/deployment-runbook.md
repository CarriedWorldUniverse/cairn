# Cairn — EC2 Deployment Runbook

**Audience:** an AI agent (or human operator) executing a first-time Cairn deployment on `nexus-cw-ec2`.
**Goal:** A running Cairn instance, with the carried-world team's repos migrated, agents registered, signature enforcement enabled, and end-to-end commit-signing verified.
**Refs:** [`docs/cairn/specs/2026-05-09-cairn-foundation-design.md`](../../docs/cairn/specs/2026-05-09-cairn-foundation-design.md) (the design this runbook implements).

This runbook is **state-aware and step-gated**: each phase ends with verification commands. Do not proceed to the next phase if a verification fails. Recovery procedures are at the end.

> **Maintenance rule.** This runbook MUST stay in sync with the actual MVP build. Any PR that changes the Cairn binary's CLI surface, config flags, endpoints, command output formats, or operational behaviour MUST update this runbook in the same change. The build sequence in the design spec §14 names which steps land which runbook-relevant surfaces; cross-reference §16 below for the surfaces this runbook depends on.

---

## 0. Required access and tools

The executing agent needs:

- AWS CLI configured with profile `nexus-cw` (for AWS API operations against `nexus-cw-ec2`) — see `~/.aws/config`
- AWS Session Manager Plugin (or SSH keypair) for shell access into the EC2 instance
- A locally-built Cairn binary (linux/amd64), built from the `cairn` branch's `cairn/lock-day-one`-merged tip. Built on <server-host>-Linux, Jacinta's laptop, or wherever — **not on the EC2** (t3.micro RAM is insufficient for a Forgejo/Cairn build)
- The org's repos already migrated to module path `github.com/CarriedWorldUniverse/*` (verified by the 2026-05-07 nexus-cw rename — `casket-go`, `bridle`, `interchange`, `nexus`, `vessel`, `cairn`)
- The owner's identity seed file (32+ bytes of high-entropy material) ready to be transferred to the EC2 — Jacinta provides
- An offsite backup destination ready (S3 bucket or AWS Secrets Manager) for the `instance-hmac.key` and SQLite snapshots

Confirm before starting:

```bash
# Verify AWS profile works
aws --profile nexus-cw sts get-caller-identity

# Verify EC2 instance exists and is running
aws --profile nexus-cw ec2 describe-instances \
    --filters "Name=tag:Name,Values=nexus-cw-ec2" \
    --query "Reservations[].Instances[].{Id:InstanceId,State:State.Name,PublicDNS:PublicDnsName,PrivateIP:PrivateIpAddress}" \
    --output table

# Expected output: one instance, State=running
```

If any check fails, stop and surface to the operator before continuing.

---

## 1. Phase 0 — Pre-flight inventory

Goal: know the existing state before changing anything.

### 1.1 Open a shell on the EC2

Preferred: AWS Session Manager (no SSH key required).

```bash
aws --profile nexus-cw ssm start-session --target <instance-id-from-phase-0>
```

Fallback: SSH if a key is configured.

```bash
ssh -i ~/.ssh/<keyfile> ec2-user@<public-dns>
# or if Ubuntu AMI:
ssh -i ~/.ssh/<keyfile> ubuntu@<public-dns>
```

### 1.2 OS detection

```bash
cat /etc/os-release
uname -a
```

Record:
- Distribution + version (Amazon Linux 2 / 2023, Ubuntu 22.04, etc.)
- Kernel version
- Architecture (expected: `x86_64`)

This determines which package manager and service-management commands you'll use later.

### 1.3 Existing Forgejo inventory

The instance is documented as having "an empty Forgejo placeholder." Verify and inventory before changing anything:

```bash
# Is forgejo running as a service?
sudo systemctl status forgejo 2>/dev/null || \
sudo systemctl status gitea 2>/dev/null || \
echo "(no forgejo/gitea service found)"

# Is there a forgejo binary anywhere?
which forgejo gitea 2>/dev/null
find / -maxdepth 4 -type f -name 'forgejo' -o -name 'gitea' 2>/dev/null

# Where is the data directory?
sudo ls -la /var/lib/forgejo/ /var/lib/gitea/ /home/git/ 2>/dev/null

# Repo content?
sudo find /var/lib/forgejo /var/lib/gitea /home/git -type d -name 'repositories' 2>/dev/null
```

Record:
- Service name (forgejo or gitea)
- Binary path
- Data directory path
- Whether `repositories/` contains anything (per the design context, it should be empty or near-empty)

If `repositories/` contains real content that wasn't accounted for, **stop and surface to the operator** before proceeding.

### 1.4 Available disk and RAM

```bash
df -h /
free -h
```

Record. Expected: ~8GB disk free minimum, 1GB RAM total. Cairn's footprint at idle is ~150-300 MB; under load with WAL it can spike. Note baseline numbers.

### 1.5 Network reachability

```bash
# Is the instance reachable on its current Forgejo port (commonly 3000)?
sudo ss -tlnp | grep -E ':(3000|443|80|22)'

# Can the instance reach github.com (for push-mirror)?
curl -sI https://github.com | head -1

# Can the instance reach codeberg.org (in case we need upstream Forgejo)?
curl -sI https://codeberg.org | head -1
```

Record open ports. github.com and codeberg.org should both return `200 OK` or a redirect.

---

## 2. Phase 1 — Snapshot existing state

Goal: take a recovery snapshot before changing anything, even if the existing Forgejo is "empty."

### 2.1 Snapshot the existing Forgejo data dir

```bash
# Adjust paths from §1.3 inventory
sudo systemctl stop forgejo 2>/dev/null || sudo systemctl stop gitea 2>/dev/null

sudo tar czf /tmp/forgejo-pre-cairn-$(date +%Y%m%d-%H%M).tar.gz \
    -C /var/lib/forgejo .  # or whatever dir the inventory found

# Move snapshot off-instance
aws --profile nexus-cw s3 cp /tmp/forgejo-pre-cairn-*.tar.gz \
    s3://nexus-cw-backups/cairn-deployment/
```

Verify the snapshot landed in S3:

```bash
aws --profile nexus-cw s3 ls s3://nexus-cw-backups/cairn-deployment/
```

If the S3 bucket doesn't exist, surface to the operator (this is a one-time bucket setup).

### 2.2 Disable the existing service

Disable so it doesn't start on boot. Don't uninstall yet — keep the binary and data dir in place as a fallback.

```bash
sudo systemctl disable forgejo 2>/dev/null || sudo systemctl disable gitea 2>/dev/null
sudo systemctl stop forgejo 2>/dev/null || sudo systemctl stop gitea 2>/dev/null
```

Verify nothing is listening on Forgejo's port:

```bash
sudo ss -tlnp | grep -E ':3000' || echo "(port 3000 free)"
```

---

## 3. Phase 2 — Install Cairn binary

Goal: `/opt/cairn/cairn` is the running binary; `/var/lib/cairn/` is the data directory; the `cairn` system user owns both.

### 3.1 Create the system user

```bash
sudo useradd --system --shell /bin/false --home-dir /var/lib/cairn --create-home cairn
sudo mkdir -p /etc/cairn /var/log/cairn
sudo chown cairn:cairn /etc/cairn /var/log/cairn /var/lib/cairn
```

### 3.2 Transfer the binary

The binary should be transferred from your build host. From the build host:

```bash
# On the build host (e.g., <server-host>-Linux or Jacinta's laptop)
scp -i ~/.ssh/<key> /path/to/cairn-binary ec2-user@<ec2-host>:/tmp/cairn

# Or if using SSM, base64-encode and pipe (for small binaries; Cairn at ~80MB needs scp)
```

On the EC2:

```bash
sudo install -o cairn -g cairn -m 0755 /tmp/cairn /opt/cairn/cairn
ls -la /opt/cairn/cairn  # verify ownership and mode
/opt/cairn/cairn --version  # smoke test the binary
```

Expected: version string starting with `cairn-` (or whatever the build embeds).

### 3.3 Install required dependencies

Cairn (Forgejo derivative) needs:
- `sqlite3` (CLI for backups and pragma tuning)
- `git` (for serving git operations)
- `openssh-server` (for SSH cloning)
- `caddy` or `nginx` (for TLS termination and reverse proxy)

Amazon Linux 2023:
```bash
sudo dnf install -y sqlite git openssh-server
sudo dnf install -y caddy --enablerepo=epel  # or build from source if EPEL not available
```

Ubuntu:
```bash
sudo apt update
sudo apt install -y sqlite3 git openssh-server
sudo curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/setup.deb.sh' | sudo bash
sudo apt install -y caddy
```

Verify:
```bash
sqlite3 --version
git --version
caddy version
```

---

## 4. Phase 3 — Configure Cairn

Goal: `app.ini` exists, configured for SQLite + WAL + Cairn-specific section.

### 4.1 Create the data directory layout

```bash
sudo -u cairn mkdir -p \
    /var/lib/cairn/data \
    /var/lib/cairn/data/repositories \
    /var/lib/cairn/data/lfs \
    /var/lib/cairn/data/attachments \
    /var/lib/cairn/data/avatars \
    /var/lib/cairn/data/log
```

### 4.2 Generate `instance-hmac.key`

```bash
# 32 random bytes, hex-encoded for readability
sudo -u cairn bash -c 'umask 077; head -c 32 /dev/urandom | xxd -p -c 64 > /etc/cairn/instance-hmac.key'
sudo chmod 0400 /etc/cairn/instance-hmac.key
sudo chown cairn:cairn /etc/cairn/instance-hmac.key

ls -la /etc/cairn/instance-hmac.key
# Expected: -r-------- 1 cairn cairn 65 ...
```

**Critical:** back this file up immediately to AWS Secrets Manager:

```bash
sudo cat /etc/cairn/instance-hmac.key | aws --profile nexus-cw secretsmanager create-secret \
    --name cairn/instance-hmac-key \
    --description "Cairn instance HMAC key — DO NOT LOSE" \
    --secret-string file:///dev/stdin
```

Verify:
```bash
aws --profile nexus-cw secretsmanager describe-secret --secret-id cairn/instance-hmac-key
```

### 4.3 Write `app.ini`

```bash
sudo tee /etc/cairn/app.ini > /dev/null <<'EOF'
APP_NAME = Cairn
RUN_USER = cairn
RUN_MODE = prod
WORK_PATH = /var/lib/cairn

[database]
DB_TYPE = sqlite3
PATH = /var/lib/cairn/data/cairn.db
SQLITE_JOURNAL_MODE = WAL
SQLITE_TIMEOUT = 5000

[repository]
ROOT = /var/lib/cairn/data/repositories
DEFAULT_BRANCH = main

[server]
APP_DATA_PATH = /var/lib/cairn/data
DOMAIN = cairn.darksoft.local
HTTP_PORT = 3000
ROOT_URL = https://cairn.darksoft.local/
SSH_PORT = 22
SSH_LISTEN_PORT = 2222
LFS_START_SERVER = true
LFS_JWT_SECRET = REPLACE_BEFORE_FIRST_RUN
DISABLE_REGISTRATION = true
INSTALL_LOCK = true

[security]
INSTALL_LOCK = true
SECRET_KEY = REPLACE_BEFORE_FIRST_RUN
INTERNAL_TOKEN = REPLACE_BEFORE_FIRST_RUN

[service]
DISABLE_REGISTRATION = true
REQUIRE_SIGNIN_VIEW = false
ENABLE_BASIC_AUTHENTICATION = false

[log]
ROOT_PATH = /var/lib/cairn/data/log
MODE = file
LEVEL = info

[mirror]
ENABLED = true
DEFAULT_INTERVAL = 8h

[cairn]
enabled = true
enforce_signatures = false
reject_orphan_agents = true
hmac_key_path = /etc/cairn/instance-hmac.key
markdown_endpoints_enabled = true
wal_checkpoint_interval_minutes = 5

EOF

sudo chown cairn:cairn /etc/cairn/app.ini
sudo chmod 0640 /etc/cairn/app.ini
```

### 4.4 Generate the secret values

Replace the three `REPLACE_BEFORE_FIRST_RUN` placeholders:

```bash
SECRET_KEY=$(/opt/cairn/cairn generate secret SECRET_KEY)
INTERNAL_TOKEN=$(/opt/cairn/cairn generate secret INTERNAL_TOKEN)
LFS_JWT_SECRET=$(/opt/cairn/cairn generate secret LFS_JWT_SECRET)

sudo sed -i \
    -e "s|^SECRET_KEY = REPLACE_BEFORE_FIRST_RUN|SECRET_KEY = $SECRET_KEY|" \
    -e "s|^INTERNAL_TOKEN = REPLACE_BEFORE_FIRST_RUN|INTERNAL_TOKEN = $INTERNAL_TOKEN|" \
    -e "s|^LFS_JWT_SECRET = REPLACE_BEFORE_FIRST_RUN|LFS_JWT_SECRET = $LFS_JWT_SECRET|" \
    /etc/cairn/app.ini
```

Verify no placeholders remain:

```bash
sudo grep REPLACE_BEFORE_FIRST_RUN /etc/cairn/app.ini && \
    echo "ERROR: placeholders still present" || \
    echo "OK: all secrets generated"
```

### 4.5 Confirm `enforce_signatures = false` for migration window

This is critical — historical commits in migrated repos use email patterns that *look* like agent commits but have no registered agent records. Verification must be off until agents are registered.

```bash
sudo grep -E '^enforce_signatures' /etc/cairn/app.ini
# Expected output: enforce_signatures = false
```

---

## 5. Phase 4 — systemd service

```bash
sudo tee /etc/systemd/system/cairn.service > /dev/null <<'EOF'
[Unit]
Description=Cairn (agent-native git platform)
After=network.target

[Service]
Type=simple
User=cairn
Group=cairn
WorkingDirectory=/var/lib/cairn
ExecStart=/opt/cairn/cairn web --config /etc/cairn/app.ini
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/cairn /etc/cairn /var/log/cairn

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable cairn
sudo systemctl start cairn
sleep 5
sudo systemctl status cairn
```

Verify the service is running:

```bash
sudo systemctl is-active cairn  # expected: active
sudo journalctl -u cairn --since '2 minutes ago' --no-pager | tail -30
```

If the service failed to start, **stop here** — investigate the journal output before proceeding.

---

## 6. Phase 5 — TLS / reverse proxy

Goal: `https://cairn.<domain>` reaches the Cairn instance with valid TLS.

### 6.1 DNS

The domain (`cairn.darksoft.co.nz` or whatever Jacinta has provisioned) must already point to the EC2's public IP. Verify:

```bash
INSTANCE_DOMAIN=cairn.darksoft.co.nz  # adjust per actual config
dig +short A $INSTANCE_DOMAIN
# Expected: the EC2's public IP from Phase 0
```

If DNS isn't resolving yet, **stop and surface to the operator** to provision DNS.

### 6.2 Caddy config

```bash
sudo tee /etc/caddy/Caddyfile > /dev/null <<EOF
$INSTANCE_DOMAIN {
    reverse_proxy localhost:3000
    encode gzip
    
    # Health check passthrough
    handle /healthz {
        respond "ok" 200
    }
}
EOF

sudo systemctl restart caddy
sleep 3
sudo systemctl status caddy
```

Verify TLS:

```bash
curl -sI https://$INSTANCE_DOMAIN/ | head -1
# Expected: HTTP/2 200 or HTTP/2 302 (Cairn's index redirect)
```

If TLS fails, check Caddy's log:

```bash
sudo journalctl -u caddy --since '5 minutes ago' --no-pager | tail -30
```

Common issues: ACME challenge blocked by inbound firewall (open port 80 + 443 in EC2 security group), DNS not propagated yet (wait and retry).

---

## 7. Phase 6 — First-run admin user

Goal: Jacinta has an admin Forgejo account; secret keys are configured; instance is ready for repos.

```bash
# Create admin user from CLI
sudo -u cairn /opt/cairn/cairn admin user create \
    --config /etc/cairn/app.ini \
    --username alice \
    --email nexus@darksoft.co.nz \
    --password "$(openssl rand -base64 24)" \
    --admin

# The above prints the password to stdout — capture it from the operator's terminal
# and store securely. Or use --random-password and read from stdout.
```

The admin user should now be visible:

```bash
sudo -u cairn /opt/cairn/cairn admin user list --config /etc/cairn/app.ini
# Expected: alice listed as admin
```

Have Jacinta log in via the web UI and:
1. Change password to something memorable
2. Configure 2FA (recommended)
3. Upload SSH public key (for git push auth)

---

## 8. Phase 7 — Smoke tests

Before any repo migration, verify the Cairn-specific endpoints work:

### 8.1 `.well-known/` discovery

```bash
curl -s https://$INSTANCE_DOMAIN/.well-known/cairn.json | head -20
# Expected: JSON manifest with cairn_version, fingerprint_algo, etc.

curl -s https://$INSTANCE_DOMAIN/.well-known/llms.txt | head -10
# Expected: markdown content starting with "# Cairn"

curl -sI https://$INSTANCE_DOMAIN/.well-known/security.txt
# Expected: HTTP 200, Content-Type: text/plain
```

### 8.2 `?format=md` rendering

This requires a repo with content; defer to after Phase 8 (migration).

### 8.3 Database health

```bash
sudo -u cairn sqlite3 /var/lib/cairn/data/cairn.db "PRAGMA journal_mode;"
# Expected: wal

sudo -u cairn sqlite3 /var/lib/cairn/data/cairn.db ".schema agent"
# Expected: the agent table DDL
```

If the schema is missing, the Cairn migrations didn't run. Check the journal for migration errors:

```bash
sudo journalctl -u cairn --since '10 minutes ago' --no-pager | grep -i 'migrat\|cairn\|error'
```

---

## 9. Phase 8 — Repo migration

Migrate the org's repos from GitHub to Cairn. **`enforce_signatures = false` is in effect during this phase**, so historical commits with non-agent author emails are accepted.

Order: `casket-go` → `bridle` → `interchange` → `nexus` → `vessel` → `cairn`.

### 9.1 Migration loop (per repo)

For each repo:

```bash
REPO_NAME=casket-go  # vary per repo

# Step 1: create empty repo on Cairn via API (admin token from logged-in alice)
# (Run from Jacinta's machine, not the EC2)
curl -X POST https://$INSTANCE_DOMAIN/api/v1/admin/users/alice/repos \
    -H "Authorization: token $CAIRN_ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"name\": \"$REPO_NAME\", \"private\": true, \"auto_init\": false}"

# Step 2: from a local clone of github.com/CarriedWorldUniverse/$REPO_NAME, push --mirror to Cairn
cd /tmp
git clone --mirror https://github.com/CarriedWorldUniverse/$REPO_NAME.git $REPO_NAME-mirror
cd $REPO_NAME-mirror
git push --mirror https://$INSTANCE_DOMAIN/alice/$REPO_NAME.git

# Step 3: configure push-mirror back to GitHub for DR
# (Done via Cairn web UI: Settings → Mirroring → add github.com URL with token)

# Step 4: clean up local clone
cd /tmp && rm -rf $REPO_NAME-mirror
```

For repos with meaningful issue/PR history (currently only `cairn`), use Forgejo's **Migrate from External** flow via web UI instead of `git push --mirror`. This brings issues, PRs, comments, and labels over.

### 9.2 Verify each migration

```bash
curl -s https://$INSTANCE_DOMAIN/alice/$REPO_NAME/info/refs?service=git-upload-pack | head
# Expected: git protocol response, including refs

curl -s https://$INSTANCE_DOMAIN/alice/$REPO_NAME/commits/main?format=md | head -20
# Expected: markdown rendering of the latest commit
```

---

## 10. Phase 9 — Agent registration

For each team agent (Plumb, Anvil, Forge, Wren, Verity, Maren, Keel, others as needed):

### 10.1 On Jacinta's seed-bearing machine

```bash
# Set up CLI auth (one-time)
cairn auth login --instance https://$INSTANCE_DOMAIN
# (Prompts for username/password; stores token at ~/.config/cairn/<host>/token)

# Register each agent
for slug in plumb anvil forge wren verity maren keel; do
  cairn agent init --slug $slug --domain darksoft.co.nz --key-from hkdf
  cairn agent submit  # auto-active because authed as owner
done

# Verify
cairn agents list
# Expected: all listed agents with status=active
```

### 10.2 On each machine where agents will commit

For each machine that will run agent commits (Jacinta's laptop, <server-host>-Linux, etc.):

1. Place the owner seed at `~/.config/cairn/seed` (mode 0600), transferred securely from Jacinta
2. Run `cairn agent init` for each agent that will run on this machine — re-derives the keypair deterministically from the same seed and slug
3. Configure git for that agent (per-repo or global):

```bash
git config user.name "nexus-plumb"
git config user.email "nexus-plumb@darksoft.co.nz"
git config gpg.format ssh
git config user.signingkey "$HOME/.config/cairn/<host>/plumb.key.pub"
git config commit.gpgsign true
```

The signing key path points to the public key file (Cairn CLI creates both `.key` private and `.key.pub` public).

---

## 11. Phase 10 — Flip enforcement and validate

Goal: `enforce_signatures = true`, end-to-end agent commit round-trip succeeds.

### 11.1 Flip the flag

```bash
sudo sed -i 's|^enforce_signatures = false|enforce_signatures = true|' /etc/cairn/app.ini
sudo systemctl restart cairn
sleep 5
sudo systemctl status cairn
sudo journalctl -u cairn --since '2 minutes ago' --no-pager | tail -10
```

Verify:

```bash
sudo grep -E '^enforce_signatures' /etc/cairn/app.ini
# Expected: enforce_signatures = true
```

### 11.2 End-to-end test

From Jacinta's machine, configured as Plumb:

```bash
cd /path/to/test-repo  # a small repo cloned from Cairn
echo "test commit at $(date)" >> test.txt
git add test.txt
git commit -m "Test agent commit"
git push origin main
```

The push should succeed. Verify in Cairn's web UI and via the markdown endpoint:

```bash
LATEST_COMMIT=$(git rev-parse HEAD)
curl -s https://$INSTANCE_DOMAIN/alice/test-repo/commits/$LATEST_COMMIT?format=md | head -20
# Expected: shows Author: nexus-plumb, Agent: plumb (under alice), Signed: ✓
```

### 11.3 Negative test — commit without signature

To verify enforcement is actually working, attempt a push that *should* be rejected:

```bash
# Disable signing temporarily
git -c commit.gpgsign=false commit --allow-empty -m "Unsigned test"
git push origin main
# Expected: REJECTED with "agent commit signature missing or invalid"
```

If this push succeeds, signature enforcement is not active — investigate.

Restore signing:

```bash
git config commit.gpgsign true
git reset --hard HEAD~1  # undo the test commit
```

### 11.4 Negative test — commit by orphan agent

Configure git as an unregistered slug and attempt a push:

```bash
git config user.email "nexus-ghost@darksoft.co.nz"
echo "orphan test" >> test.txt
git add test.txt
git commit -m "Orphan test"
git push origin main
# Expected: REJECTED with "agent not found"
```

Restore the legitimate slug.

---

## 12. Phase 11 — Backup automation

Set up nightly backups via cron.

### 12.1 SQLite snapshot script

```bash
sudo tee /opt/cairn/backup-cairn.sh > /dev/null <<'EOF'
#!/bin/bash
set -e
DATESTAMP=$(date +%Y%m%d-%H%M)
BACKUP_DIR=/var/lib/cairn/backups
mkdir -p $BACKUP_DIR

# SQLite via .backup (safe with WAL)
sudo -u cairn sqlite3 /var/lib/cairn/data/cairn.db ".backup '$BACKUP_DIR/cairn-$DATESTAMP.db'"

# Tar the data dir (repos, LFS, attachments)
tar czf $BACKUP_DIR/cairn-data-$DATESTAMP.tar.gz \
    -C /var/lib/cairn/data \
    repositories lfs attachments avatars

# Ship to S3
aws --profile nexus-cw s3 cp $BACKUP_DIR/cairn-$DATESTAMP.db \
    s3://nexus-cw-backups/cairn/db/
aws --profile nexus-cw s3 cp $BACKUP_DIR/cairn-data-$DATESTAMP.tar.gz \
    s3://nexus-cw-backups/cairn/data/

# Keep only the last 3 local snapshots
ls -1t $BACKUP_DIR/cairn-*.db 2>/dev/null | tail -n +4 | xargs -r rm
ls -1t $BACKUP_DIR/cairn-data-*.tar.gz 2>/dev/null | tail -n +4 | xargs -r rm
EOF

sudo chmod 0755 /opt/cairn/backup-cairn.sh
```

### 12.2 Cron entry

```bash
echo "0 3 * * * /opt/cairn/backup-cairn.sh" | sudo crontab -u root -
sudo crontab -u root -l  # verify
```

### 12.3 Verify backup runs

```bash
sudo /opt/cairn/backup-cairn.sh
aws --profile nexus-cw s3 ls s3://nexus-cw-backups/cairn/db/
# Expected: today's .db file present
```

---

## 13. Final verification checklist

After all phases complete:

- [ ] `https://cairn.<domain>/` reachable with valid TLS
- [ ] `https://cairn.<domain>/.well-known/cairn.json` returns the manifest
- [ ] `https://cairn.<domain>/.well-known/llms.txt` returns markdown
- [ ] `enforce_signatures = true` in `/etc/cairn/app.ini`
- [ ] `cairn` systemd service running (`systemctl is-active cairn` → `active`)
- [ ] Caddy systemd service running
- [ ] All org repos migrated (`casket-go`, `bridle`, `interchange`, `nexus`, `vessel`, `cairn`)
- [ ] Each migrated repo has push-mirror to GitHub configured
- [ ] All team agents registered with status=active
- [ ] An end-to-end agent commit (Phase 11.2) succeeds and renders correctly
- [ ] Negative tests (unsigned commit, orphan agent) both reject as expected
- [ ] `instance-hmac.key` backed up to AWS Secrets Manager
- [ ] Nightly backup cron in place; first backup ran successfully
- [ ] DNS A-record for `cairn.<domain>` points to the EC2's public IP

---

## 14. Recovery procedures

| Failure | Recovery |
|---|---|
| systemd service won't start | `journalctl -u cairn --no-pager` → fix config error → restart |
| Database migration error | Stop service, restore latest snapshot from S3, fix the issue (e.g., disk space), re-run migrations |
| Push rejected unexpectedly during migration | Confirm `enforce_signatures = false`; if true, set to false, restart, retry |
| Caddy ACME failure | Check inbound 80/443 in EC2 security group; verify DNS; check Caddy logs |
| `instance-hmac.key` lost | Restore from AWS Secrets Manager: `aws secretsmanager get-secret-value --secret-id cairn/instance-hmac-key --query SecretString --output text > /etc/cairn/instance-hmac.key`. If lost from both places, all current fingerprints become unreproducible (still verifiable in DB; new registrations use different namespace) |
| Cairn process crashed mid-push | Forgejo handles this; the push either lands or fails atomically. Check repo refs match expectations |
| Agent commits accepted without verification | Check `enforce_signatures` setting; restart Cairn after change; verify in `app.ini` and journal |

---

## 15. Phase complete — handoff

Once §13 checklist is fully green:

1. Notify the operator (Jacinta) that the deployment is complete and verified
2. Provide:
   - Cairn URL
   - Admin username
   - Path to `instance-hmac.key` backup in Secrets Manager
   - List of registered agent fingerprints
   - First-backup timestamp
3. Document any deviations from this runbook in a deployment-log file in the same directory:
   `cairn/deploy/deployment-logs/YYYY-MM-DD-deployment.md`

Cairn is now the team's primary git platform. GitHub remains as DR via push-mirror.

---

## 16. Build-surfaces this runbook depends on

These are the parts of the Cairn binary's behaviour that this runbook assumes work as documented. If any of these change during build, **this runbook must be updated in the same PR**:

### CLI surfaces (referenced in §10 and §11)

- `cairn admin user create --config --username --email --password --admin` — Forgejo-inherited
- `cairn admin user list --config` — Forgejo-inherited
- `cairn generate secret SECRET_KEY|INTERNAL_TOKEN|LFS_JWT_SECRET` — Forgejo-inherited
- `cairn web --config <path>` — Forgejo-inherited
- `cairn auth login --instance <url>` — **Cairn-specific (Phase 1 build)**
- `cairn agent init --slug <s> --domain <d> --key-from random|hkdf` — **Cairn-specific (Phase 1 build)**
- `cairn agent submit [--owner <u>]` — **Cairn-specific (Phase 1 build)**
- `cairn agents list` — **Cairn-specific (Phase 1 build)**
- `cairn agents approve <fingerprint>` — **Cairn-specific (Phase 1 build)**
- `cairn agents block <fingerprint>` — **Cairn-specific (Phase 1 build)**
- `cairn commit-sign-helper --slug <s>` — **Cairn-specific (Phase 1 build)**

### Config surfaces (referenced in §4)

- `[cairn]` section in `app.ini` with flags: `enabled`, `enforce_signatures`, `reject_orphan_agents`, `hmac_key_path`, `markdown_endpoints_enabled`, `wal_checkpoint_interval_minutes`
- `[mirror] ENABLED = true` to enable push-mirror per repo
- `[security] INSTALL_LOCK = true` to disable the web installer after first-run

### File-system surfaces (referenced throughout)

- Binary location: `/opt/cairn/cairn`
- Config: `/etc/cairn/app.ini`
- Data dir: `/var/lib/cairn/data/`
- HMAC key: `/etc/cairn/instance-hmac.key` (mode 0400, owner `cairn`)
- Database file: `/var/lib/cairn/data/cairn.db`
- System user: `cairn` (UID assigned by useradd)

### HTTP surfaces (referenced in §6, §8, §11)

- `GET /.well-known/cairn.json` returns the documented manifest
- `GET /.well-known/llms.txt` returns markdown
- `GET /.well-known/security.txt` returns text/plain
- `GET /:owner/:repo/commits/:hash?format=md` returns markdown
- `POST /api/v1/admin/users/:user/repos` creates a repo (Forgejo API)
- `POST /api/cairn/v1/agents` registers an agent — **Cairn-specific (Phase 1 build)**
- `POST /api/cairn/v1/agents/:fingerprint/approve` approves a pending agent — **Cairn-specific (Phase 1 build)**

### Pre-receive hook behaviour (referenced in §11.3, §11.4)

- Rejects unsigned commits when `enforce_signatures = true` and author matches `nexus-{slug}@{domain}`
- Rejects commits whose agent slug is not registered (orphan agents)
- Rejects commits whose signature doesn't verify against the registered public key
- Accepts commits whose author email doesn't match the agent pattern (treats as human commit, vanilla Forgejo path)

### Manifest schema (referenced in §8.1)

The structure of `.well-known/cairn.json` documented in design spec §7 (subsection on `.well-known/cairn.json`). If the manifest schema changes, the smoke test `head -20` may need to look for different fields.

If a build PR adds a new Cairn-specific surface (e.g., new CLI subcommand, new endpoint, new config flag), add it to the appropriate subsection above and reference it from the relevant runbook phase.

---

## Changelog

- **2026-05-09 (initial draft):** matches design spec `2026-05-09-cairn-foundation-design.md`. No build has landed yet; runbook is forward-looking.
