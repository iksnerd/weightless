# Deployment Guide

Two deployment options: **App Engine** (simplest) or **Cloud Run** (with Litestream DB replication).

---

## Option A: App Engine (Simplest)

App Engine manages scaling, SSL, and routing automatically. DB lives at `/tmp/` (ephemeral — resets on deploy). Best for getting started.

### 1. Prerequisites

```bash
# Install gcloud CLI
brew install google-cloud-sdk

# Login and set project
gcloud auth login
gcloud config set project YOUR_PROJECT_ID
```

### 2. Deploy

```bash
cd ~/GolandProjects/weightless
gcloud app deploy
```

That's it. App Engine reads `app.yaml` and builds the Go binary automatically.

### 3. Verify

```bash
gcloud app browse
# Opens https://YOUR_PROJECT_ID.appspot.com

curl https://YOUR_PROJECT_ID.appspot.com/health
# → OK
```

### 4. Set environment variables

Edit `app.yaml` to add your registry key:

```yaml
runtime: go125

env_variables:
  DB_PATH: /tmp/weightless.db
  REGISTRY_KEY: your-secret-key-here

handlers:
- url: /.*
  script: auto
```

Then redeploy: `gcloud app deploy`

### 5. Custom domain

```bash
# Add custom domain to App Engine
gcloud app domain-mappings create your-domain.example.com
```

This outputs DNS records you need to add. Go to your domain registrar (Cloudflare, Namecheap, etc.) and add:

| Type | Name | Value |
|------|------|-------|
| CNAME | tracker | ghs.googlehosted.com. |

Or if using an apex domain:

| Type | Name | Value |
|------|------|-------|
| A | @ | (IP addresses shown by gcloud) |
| AAAA | @ | (IPv6 addresses shown by gcloud) |

Google provisions SSL automatically (takes 10-30 minutes).

```bash
# Check mapping status
gcloud app domain-mappings describe your-domain.example.com
```

Once DNS propagates:
```bash
curl https://your-tracker.example.com/health
# → OK
```

### App Engine limitations
- `/tmp/` is ephemeral — DB resets on each deploy or instance restart
- No persistent disk (use Cloud Run + Litestream for persistence)
- 60-second request timeout (fine for tracker)
- Free tier: 28 instance-hours/day

---

## Option B: Cloud Run + Litestream (Persistent DB, $0/Month Optimized)

Cloud Run runs the Docker container with Litestream replicating SQLite to a GCS bucket. DB survives restarts and deploys. We have heavily optimized this architecture to run entirely within the **Google Cloud Free Tier**.

### Key Serverless Optimizations Built-In:
1. **Graceful Shutdown:** `scripts/run.sh` traps `SIGTERM` signals from Cloud Run on scale-to-zero, ensuring Litestream finishes syncing the SQLite WAL to GCS before the container dies.
2. **Probabilistic Pruning:** Because long-running background goroutines don't run when the container is spun down, `handleAnnounce` probabilistically cleans up stale peers (1/100 requests) to keep the database tiny without background tasks.

### 1. Prerequisites

```bash
gcloud auth login
gcloud config set project YOUR_PROJECT_ID

# Enable required APIs
gcloud services enable run.googleapis.com
gcloud services enable artifactregistry.googleapis.com
gcloud services enable storage.googleapis.com
```

### 2. Create a GCS bucket for DB backups (Free Tier < 5GB)

```bash
# Must be in a free tier region like us-central1, us-east1, or us-west1
gsutil mb -l us-central1 gs://weightless-db-backup

# CRITICAL: Keep backups under the 5GB free limit by deleting old snapshots
echo '{"rule": [{"action": {"type": "Delete"}, "condition": {"age": 7}}]}' > lifecycle.json
gsutil lifecycle set lifecycle.json gs://weightless-db-backup
rm lifecycle.json

# Create HMAC keys for Litestream (S3-compatible access to GCS)
gsutil hmac create YOUR_SERVICE_ACCOUNT@YOUR_PROJECT_ID.iam.gserviceaccount.com
```

Save the `Access ID` and `Secret` — you'll need them.

### 3. Deploy (Optimized for $0/Month)

Run this exact command to ensure you stay within the free tier limits while guaranteeing Litestream replication works correctly:

```bash
gcloud run deploy weightless \
  --source . \
  --region us-central1 \
  --allow-unauthenticated \
  --max-instances 2 \
  --min-instances 0 \
  --concurrency 80 \
  --memory 256Mi \
  --cpu 1 \
  --no-cpu-throttling \
  --set-env-vars "GCS_ACCESS_KEY=YOUR_ACCESS_ID,GCS_SECRET_KEY=YOUR_SECRET,BACKUP_BUCKET=weightless-db-backup,REGISTRY_KEY=your-secret-key"
```

**Why these specific flags?**
* `--region us-central1`: Required for the Cloud Run free tier.
* `--min-instances 0`: Scales to zero when idle, consuming no vCPU-seconds.
* `--memory 256Mi`: The Alpine/Go/SQLite stack runs in <50MB, keeping GiB-seconds minimal.
* `--no-cpu-throttling`: **Crucial.** Ensures that Litestream replication (which happens in the background) and the **in-memory to SQLite flusher** have CPU time to finish pushing data *after* an HTTP request finishes and before the container shuts down.

### 4. Verify

```bash
# Get the auto-assigned URL
gcloud run services describe weightless --region us-central1 --format="value(status.url)"
# → https://weightless-xxxxx-uc.a.run.app

curl https://weightless-xxxxx-uc.a.run.app/health
# → OK
```

### 5. Custom domain

```bash
# Map custom domain
gcloud run domain-mappings create \
  --service weightless \
  --domain your-domain.example.com \
  --region us-central1
```

Add the DNS records shown:

| Type | Name | Value |
|------|------|-------|
| CNAME | tracker | ghs.googlehosted.com. |

Check status:
```bash
gcloud run domain-mappings describe \
  --domain your-domain.example.com \
  --region us-central1
```

SSL is provisioned automatically by Google.

### Cloud Run limits
- Free tier: 2 million requests/month, 180,000 vCPU-seconds, 360,000 GB-seconds.
- Egress: 1GB free internet data transfer per month (covers roughly 10 million BitTorrent `/announce` responses).
- Request timeout: 300s (configurable up to 3600s)
- With `min-instances=0`, cold starts will restore the DB from GCS (~1-3 seconds latency on the first request after being idle).

---

## After deployment: Test with `wl` CLI

```bash
# Build the CLI
go build -o wl ./cmd/wl/

# Create and register a torrent against your deployed weightless
./wl create \
  --name "test-dataset" \
  --tracker https://your-tracker.example.com \
  --api-key your-secret-key \
  ./some-file.zip

# Verify it registered
curl https://your-tracker.example.com/api/registry/search
```

## After deployment: Point Next.js frontend

In your Next.js project `.env.local`:
```env
TRACKER_URL=https://your-tracker.example.com
WEIGHTLESS_API_KEY=your-secret-key
```

---

## DNS Summary

Whichever option you choose, you need one DNS record:

| Type | Name | Value | TTL |
|------|------|-------|-----|
| CNAME | tracker | ghs.googlehosted.com. | 300 |

Google handles SSL certificate provisioning automatically. Propagation takes 10-30 minutes.

If you're using **Cloudflare**, set the proxy status to **DNS only** (grey cloud) until the Google SSL cert is provisioned, then you can enable the orange cloud proxy.

---

## Updating

**App Engine:**
```bash
gcloud app deploy
```

**Cloud Run:**
```bash
gcloud run deploy weightless --source . --region us-central1
```

Both rebuild from source and deploy with zero downtime.

---

## Monitoring

```bash
# App Engine logs
gcloud app logs tail -s default

# Cloud Run logs
gcloud run services logs read weightless --region us-central1

# Check health
curl https://your-tracker.example.com/health
```
