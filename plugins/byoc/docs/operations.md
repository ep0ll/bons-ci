# Operations Runbook

## Health Checks

| Endpoint | What it checks | Expected response |
|----------|---------------|-------------------|
| `GET /healthz` | Process alive | `{"status":"ok"}` — HTTP 200 |
| `GET /readyz` | DB reachable | `{"status":"ready"}` — HTTP 200; `503` if DB is down |
| `GET /metrics` | Prometheus scrape | Full metrics page |

---

## Key Metrics to Monitor

| Metric | Alert threshold | Meaning |
|--------|----------------|---------|
| `byoc_provision_latency_seconds{p90}` | > 90 s | Runners taking too long to come online |
| `byoc_job_queue_depth` (sum) | > 50 | Runner limits saturated; tenants waiting |
| `byoc_runners_active_total` | — | Normal operational gauge per tenant |
| `byoc_rate_limit_hits_total` | > 10/min | Burst protection triggering; may need higher burst |
| `byoc_runner_terminations_total{reason="stuck_registering"}` | > 0 | cloud-init failures; check OCI image |
| `byoc_runner_terminations_total{reason="stuck_provisioning"}` | > 0 | OCI Compute capacity issues |
| `byoc_api_request_duration_seconds{p99}` | > 2 s | API latency degradation |

---

## Scaling the Control Plane

The control plane is stateless — add instances behind the OCI Load Balancer:

```bash
# Scale up: add a second control-plane Container Instance
oci container-instances container-instance create \
  --display-name byoc-control-plane-2 \
  # ... same config as the first instance

# Add to the load balancer backend set
oci lb backend create \
  --load-balancer-id <lb-ocid>    \
  --backend-set-name byoc-backend \
  --ip-address <new-instance-ip>  \
  --port 8080
```

**Caveats for multi-instance:**
- The job channel and wait queues are in-process. In a multi-instance deployment,
  each webhook may land on a different instance. Add a shared Redis-backed queue
  before scaling to 2+ instances in production.
- The rate limiter is per-instance. Effective burst = `burst × instance_count`.

---

## Incident Runbook

### Runners Stuck in Provisioning

**Symptom**: `byoc_runner_terminations_total{reason="stuck_provisioning"}` increasing.

**Investigation**:
```bash
# List stuck runners
curl https://byoc.example.com/v1/tenants/<tenant_id>/runners | \
  jq '.data[] | select(.status=="provisioning")'

# Check OCI Compute for the instance
oci compute instance get --instance-id <oci_instance_id>

# Check OCI capacity
oci limits value list --compartment-id <compartment> --service-name compute
```

**Resolution**:
1. If OCI returned an error during launch, the reconciler will auto-clean in 5 min.
2. If OCI capacity is exhausted in the AZ, consider adding a secondary AD fallback shape.
3. If the image is corrupt: rebuild the custom image (see `modules/compute` README).

---

### Runners Stuck in Registering

**Symptom**: Runner is provisioned (OCI instance RUNNING) but never reaches GitHub.

**Investigation**:
```bash
# Get OCI instance ID from the runner record
INSTANCE_ID=$(curl -s https://byoc.example.com/v1/tenants/<id>/runners | \
  jq -r '.data[] | select(.status=="registering") | .oci_instance_id')

# View cloud-init output (requires OCI Bastion or console connection)
oci compute instance-console-connection create --instance-id $INSTANCE_ID
# Then use the VNC console or serial console to check:
# sudo cat /var/log/cloud-init-output.log
```

**Common causes**:
1. **NAT gateway down**: runner can't reach `github.com`. Check OCI route table.
2. **Registration token expired**: token is valid for ~1h; if OCI was slow to start, token may be stale. Reconciler terminates and a new runner is provisioned on the next webhook.
3. **Wrong image**: runner binary not pre-installed. Re-bake the custom image.

---

### Job Queue Growing Unbounded

**Symptom**: `byoc_job_queue_depth` rising; tenants complaining jobs aren't starting.

**Investigation**:
```bash
# Check MaxRunners per tenant
curl https://byoc.example.com/v1/tenants/<id> | jq '.data.max_runners'

# Check active runners
curl https://byoc.example.com/v1/tenants/<id>/runners | \
  jq '[.data[] | select(.status != "terminated")] | length'
```

**Resolution**:
1. Increase `max_runners` for the affected tenant (requires a store update or PATCH endpoint).
2. Check for runners stuck in non-terminal states — the reconciler should clean them.
3. If the wait queue is an in-memory backlog that grew after a restart, the jobs are lost — GitHub will re-queue them after the runner timeout.

---

### GitHub API Rate Limit

**Symptom**: `byoc_rate_limit_hits_total` rising; logs show GitHub 403 or 429 errors.

**Investigation**:
```bash
# GitHub App rate limit is per installation: 15,000 req/hour
# Check how many registration tokens are being created
grep "CreateRegistrationToken" /var/log/byoc.log | wc -l
```

**Resolution**:
1. The installation token cache (55-min TTL) should prevent redundant token fetches.
2. Verify the in-memory cache is working — check for cache-miss log lines.
3. If running multiple control-plane instances, each has its own cache. Add a shared Redis cache.

---

## Database Maintenance

```bash
# Connect to MySQL (via OCI Bastion)
mysql -h <db-endpoint> -u byoc_admin -p byoc

# Check idempotency lock table size (should be auto-purged)
SELECT COUNT(*), MIN(expires_at), MAX(expires_at)
FROM idempotency_locks;

# Manually purge expired locks if needed
DELETE FROM idempotency_locks WHERE expires_at < NOW();

# Check runner accumulation (terminated runners can be archived after 30 days)
SELECT status, COUNT(*) FROM runners GROUP BY status;

# Archive old terminated runners
INSERT INTO runners_archive SELECT * FROM runners
  WHERE status = 'terminated' AND terminated_at < DATE_SUB(NOW(), INTERVAL 30 DAY);
DELETE FROM runners
  WHERE status = 'terminated' AND terminated_at < DATE_SUB(NOW(), INTERVAL 30 DAY);
```

---

## Backup & Recovery

| Resource | Backup method | RTO | RPO |
|----------|--------------|-----|-----|
| MySQL (control-plane state) | OCI MySQL automated backup — daily, 7-day retention | 30 min | 24 h |
| OCI Vault secrets | OCI Vault automatic replication within region | N/A | N/A |
| Terraform state | OCI Object Storage — versioned bucket | N/A | N/A |

**Recovery from DB loss** (worst case):
1. Restore MySQL from last backup.
2. Any runners provisioned after the backup will appear in OCI but not in the DB.
3. The reconciler's OCI orphan sweep (when fully implemented with OCI Search API)
   will detect and terminate them. In the interim, run the orphan sweep manually.

---

## Terraform Drift Detection

Run weekly to catch manual changes:

```bash
cd infra/terraform/environments/prod
terraform plan -refresh-only
```

Any unexpected drift should be investigated before `terraform apply`.

---

## Useful CLI Snippets

```bash
# List all active runners across all tenants
curl -s https://byoc.example.com/v1/tenants | jq -r '.data[].id' | \
  xargs -I{} curl -s "https://byoc.example.com/v1/tenants/{}/runners" | \
  jq '[.data[] | select(.status != "terminated")]'

# Watch provision latency in real time
watch -n 10 'curl -s http://localhost:8080/metrics | grep byoc_provision_latency'

# Terminate a specific stuck runner manually (admin operation)
# 1. Get the OCI instance ID
curl -s https://byoc.example.com/v1/tenants/<id>/runners | \
  jq -r '.data[] | select(.id == "<runner-id>") | .oci_instance_id'

# 2. Terminate via OCI CLI
oci compute instance terminate --instance-id <oci-instance-id> --force

# 3. Update DB status (until the admin API is implemented)
mysql -h <db-endpoint> -u byoc_admin -p byoc \
  -e "UPDATE runners SET status='terminated', terminated_at=NOW() WHERE id='<runner-id>'"
```
