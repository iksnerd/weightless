# Weightless Workflow Skills

Guide for common administrative and development tasks.

## 1. Registering a New Dataset

To create a hybrid torrent and register it with the tracker:

```bash
# 1. Build the tools
./scripts/build.sh

# 2. Create and register
./wl create \
  --name "MyDataset" \
  --description "Description of data" \
  --publisher "YourName" \
  --tracker "http://localhost:8080" \
  ./path/to/data
```

## 2. Monitoring Tracker Health

The tracker provides real-time Prometheus metrics.

```bash
# View raw metrics
curl http://localhost:8080/metrics

# Key metrics to watch:
# - tracker_active_peers: Current active seeders/leechers
# - tracker_registered_torrents: Total torrents in registry
# - tracker_announces_total: Traffic volume
```

## 3. Takedowns and Blocking

If a torrent contains illegal content or needs to be removed:

```bash
# Delete from registry and add to blocklist
# (Requires REGISTRY_KEY to be set on server)
curl -X DELETE "http://localhost:8080/api/registry?info_hash=HASH&reason=REASON" \
     -H "X-Weightless-Key: your-secret-key"
```

## 4. Local Development Cycle

```bash
# Run all tests and verify coverage
./scripts/test.sh

# Start local server for testing
./scripts/dev.sh
```

## 5. Deployment

Deployment is automated via Cloud Run. See `docs/DEPLOY.md` for full details.

```bash
# Deploy to production
gcloud run deploy weightless --source . [flags...]
```
