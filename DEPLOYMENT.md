# Public deployment runbook

This runbook deploys one Go application container and one Caddy reverse proxy on one Ubuntu server.
Caddy is the only public service. The application stores browser-session JSON in the external named
volume `supplier-catalog-data`; every command below preserves that volume unless a restore is
explicitly requested.

## Values required before deployment

Replace these examples with real values:

- `catalog.example.com`: a domain or subdomain you control.
- `SERVER_IP`: the Ubuntu server's public IPv4 address (and IPv6 address if used).
- `SSH_USER` and the server's SSH hostname/address.

The repository does not contain these values, so no GitHub Actions deployment workflow is included.
An automated workflow would additionally require an SSH destination, SSH username, private deploy
key, host-key fingerprint, deployment directory and the final domain as repository/environment
secrets. Prefer a GitHub Environment with required approval before adding such a workflow.

## DNS and network requirements

Before starting Caddy, create an `A` record for `catalog.example.com` pointing to `SERVER_IP`. Create
an `AAAA` record only when the server has working public IPv6. Remove stale `A`/`AAAA` records. Ports
80 and 443 must reach the server so Caddy can obtain and renew its HTTPS certificate. Port 8080 must
not be opened or forwarded.

Verify DNS from another machine:

```sh
dig +short A catalog.example.com
dig +short AAAA catalog.example.com
```

Both results, if present, must belong to this server.

## One-time Ubuntu setup

Run these commands over SSH on Ubuntu 22.04 or 24.04. Keep the SSH rule that matches your actual SSH
port; the example uses port 22.

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl git
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
. /etc/os-release
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable" | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
sudo usermod -aG docker "$USER"
```

Log out and reconnect so group membership takes effect, then configure the firewall:

```sh
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 443/udp
sudo ufw enable
sudo ufw status
```

## First deployment

Use a stable path and the repository's real HTTPS clone URL:

```sh
sudo mkdir -p /opt/supplier-catalog-sync
sudo chown "$USER":"$USER" /opt/supplier-catalog-sync
git clone https://github.com/leduc121/supplier-catalog-sync.git /opt/supplier-catalog-sync
cd /opt/supplier-catalog-sync
git checkout main
```

Create `.env` with the real domain and an immutable image tag based on the checked-out commit. Set
`DOMAIN` to the DNS name prepared above:

```sh
cd /opt/supplier-catalog-sync
DOMAIN=catalog.example.com
SHA=$(git rev-parse --short=12 HEAD)
printf 'DOMAIN=%s\nAPP_IMAGE=supplier-catalog-sync:%s\n' "$DOMAIN" "$SHA" > .env
```

Then create the data volume exactly once and deploy:

```sh
cd /opt/supplier-catalog-sync
docker volume inspect supplier-catalog-data >/dev/null 2>&1 || docker volume create supplier-catalog-data
docker compose config
docker compose build catalog
docker compose up -d
docker compose ps
docker compose logs --tail=100 catalog caddy
```

Do not run `docker compose down -v`, `docker volume rm supplier-catalog-data`, or replace the external
volume. Normal `docker compose down` and container recreation preserve it.

## Updating without losing data

Back up first. Record the current image from `.env`, then build the new commit under a new immutable
tag:

```sh
cd /opt/supplier-catalog-sync
mkdir -p backups
docker compose stop catalog
docker run --rm -v supplier-catalog-data:/data:ro -v "$PWD/backups":/backup alpine:3.20 sh -c 'tar czf /backup/catalog-before-update.tgz -C /data .'
docker compose start catalog
git fetch origin
git checkout main
git pull --ff-only origin main
NEW_SHA=$(git rev-parse --short=12 HEAD)
sed -i "s|^APP_IMAGE=.*|APP_IMAGE=supplier-catalog-sync:${NEW_SHA}|" .env
docker compose build catalog
docker compose up -d --no-deps catalog
docker compose ps
curl --fail --silent --show-error "https://$(sed -n 's/^DOMAIN=//p' .env)/healthz"
```

Compose recreates only the application container and remounts the existing external volume.

## Rollback

Keep previous application images on the server. Set `APP_IMAGE` back to the exact previous tag and
recreate only the application container:

```sh
cd /opt/supplier-catalog-sync
docker image ls supplier-catalog-sync
sed -i 's|^APP_IMAGE=.*|APP_IMAGE=supplier-catalog-sync:PREVIOUS_SHA|' .env
docker image inspect supplier-catalog-sync:PREVIOUS_SHA >/dev/null
docker compose up -d --no-deps catalog
docker compose ps
```

This does not change `supplier-catalog-data`. If the deployment also changed Compose or Caddy
configuration, check out the previous known-good Git commit first, retain the same `.env`, and run
`docker compose up -d`; never add `-v`.

## Backup and restore

For a consistent backup, briefly stop writes to the single application instance:

```sh
cd /opt/supplier-catalog-sync
mkdir -p backups
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
docker compose stop catalog
docker run --rm -v supplier-catalog-data:/data:ro -v "$PWD/backups":/backup alpine:3.20 sh -c "tar czf /backup/catalog-${STAMP}.tgz -C /data ."
docker compose start catalog
sha256sum "backups/catalog-${STAMP}.tgz" > "backups/catalog-${STAMP}.tgz.sha256"
```

Copy the archive and checksum off the server. To restore, choose a verified archive, stop the app,
make a pre-restore backup, and replace only the contents of the existing volume:

```sh
cd /opt/supplier-catalog-sync
sha256sum -c backups/catalog-RESTORE_TIMESTAMP.tgz.sha256
docker compose stop catalog
docker run --rm -v supplier-catalog-data:/data -v "$PWD/backups":/backup alpine:3.20 sh -c 'tar czf /backup/catalog-pre-restore.tgz -C /data . && find /data -mindepth 1 -delete && tar xzf /backup/catalog-RESTORE_TIMESTAMP.tgz -C /data'
docker compose start catalog
docker compose ps
```

The restore command does not delete or recreate the Docker volume. Confirm the archive filename
before running it because it replaces the volume's current files.

## Deployment verification

First verify exposure and HTTPS from a machine outside the server:

```sh
curl -I http://catalog.example.com/
curl --fail --show-error --silent https://catalog.example.com/healthz
curl -I https://catalog.example.com/
```

HTTP must redirect to HTTPS, health must return `{"status":"ok"}`, and the HTTPS certificate must
match the domain. On the server, `docker compose ps` should show only Caddy publishing host ports;
`catalog` should show `8080/tcp`, not `0.0.0.0:8080`.

Open `https://catalog.example.com` in a browser and use the outage workflow in README.md. In browser
developer tools, confirm `catalog_session` and `catalog_csrf` have `Secure` and `SameSite=Lax`; the
session cookie is also `HttpOnly`. Confirm a cross-origin mutation is rejected:

```sh
curl -i -X POST https://catalog.example.com/api/start -H 'Origin: https://attacker.example' -H 'Content-Type: application/json' --data '{"scenario":"happy","fail_after":-1}'
```

It must return HTTP 403 with `origin_rejected`.

Finally, select **Normal catalog** and **After batch 2**, then select **Start update**. Confirm
**INCOMPLETE** at `2 / 3`, restart only the app, and refresh the same browser:

```sh
cd /opt/supplier-catalog-sync
docker compose restart catalog
docker compose ps
```

The same session must still be **INCOMPLETE** at `2 / 3`. Select **Resume update** and confirm
**Completed** at `3 / 3`. Recreate the container once more with `docker compose up -d --force-recreate
--no-deps catalog`; the completed catalog and run history must still be present. This verifies HTTPS,
cookie continuity, persistent session JSON and outage/resume across container recreation.
