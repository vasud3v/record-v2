# Cache Management Guide

## Problem: Cache Storage Limit

GitHub Actions has a **10 GB cache storage limit** per repository. When you hit this limit:
- New caches cannot be created
- Workflows may fail
- Performance degrades

## Current Solution

### 1. Automatic Daily Cleanup
A new workflow (`.github/workflows/cache-cleanup.yml`) runs daily at 00:00 UTC to:
- Delete caches older than 7 days
- Report cache usage statistics
- Warn when approaching the 10 GB limit

### 2. Manual Cleanup
You can manually trigger cleanup anytime:

```bash
# Delete all caches (nuclear option)
gh cache delete --all

# Delete specific cache by ID
gh cache delete <cache-id>

# List all caches to see what's taking space
gh cache list --limit 100

# Trigger the cleanup workflow manually
gh workflow run cache-cleanup.yml
```

### 3. Cache Strategy Improvements

The workflow now uses better cache keys to prevent accumulation:

**Old Strategy (BAD):**
```yaml
key: state-pending-upload-
```
- All runs shared the same cache
- Caches accumulated over time
- Hit 10 GB limit quickly

**New Strategy (GOOD):**
```yaml
key: state-pending-upload-${{ needs.validate.outputs.session_id }}-${{ github.run_id }}-job-${{ matrix.job.id }}
restore-keys: |
  state-pending-upload-${{ needs.validate.outputs.session_id }}-
  state-pending-upload-
```
- Each workflow run has unique cache
- Old caches can be safely deleted
- Better isolation between runs

## Cache Usage Patterns

### Typical Cache Sizes
- **Shared config:** ~1 KB (negligible)
- **Job state (no recordings):** ~250 bytes (negligible)
- **Job state (with recordings):** 500 MB - 2 GB per job

### Example Calculation
With 20 matrix jobs recording simultaneously:
- 20 jobs × 737 MB average = **14.7 GB** (exceeds limit!)
- This is why cleanup is critical

## Best Practices

### 1. Upload Recordings Immediately
Don't let recordings accumulate in cache:
```yaml
- name: Upload cached recordings from previous run
  if: always()
  # This step uploads and deletes recordings
```

### 2. Set Appropriate Retention
The cleanup workflow defaults to 7 days:
```yaml
max_age_days: '7'  # Adjust based on your needs
```

### 3. Monitor Cache Usage
Check cache usage regularly:
```bash
# Quick check
gh cache list | head -20

# Detailed analysis
gh cache list --json sizeInBytes,createdAt,key | \
  jq '[.[] | {key: .key, size_mb: (.sizeInBytes / 1024 / 1024 | floor), age_hours: ((now - (.createdAt | fromdateiso8601)) / 3600 | floor)}]'
```

### 4. Emergency Cleanup
If you hit the limit during a workflow run:
```bash
# Delete all caches immediately
gh cache delete --all

# Or delete only old caches
gh workflow run cache-cleanup.yml
```

## Monitoring

### Set Up Alerts
Monitor cache usage and set up alerts:

1. **Daily Check:** Review cleanup workflow logs
2. **Weekly Report:** Check cache usage trends
3. **Alert Threshold:** Set alert at 8 GB (80% of limit)

### Cache Usage Dashboard
Create a simple dashboard to track:
- Total cache size over time
- Number of caches
- Average cache size
- Oldest cache age

## Troubleshooting

### Issue: "Cache storage limit exceeded"
**Symptoms:** Workflow fails with cache save error

**Solution:**
```bash
# Immediate fix
gh cache delete --all

# Long-term fix
# 1. Reduce cache retention (5 days instead of 7)
# 2. Upload recordings more frequently
# 3. Run cleanup workflow more often (twice daily)
```

### Issue: Caches growing too large
**Symptoms:** Individual caches > 1 GB

**Solution:**
1. Check what's being cached:
   ```yaml
   path: |
     ./videos  # This can be large!
     ./state
     ./partial
   ```

2. Ensure recordings are uploaded and deleted:
   ```bash
   # In workflow
   rm -f "$video"  # Delete after successful upload
   ```

3. Use compression:
   ```yaml
   env:
     ZSTD_CLEVEL: 19  # Maximum compression
   ```

### Issue: Too many small caches
**Symptoms:** Hundreds of tiny caches

**Solution:**
1. Consolidate cache keys
2. Use more specific restore-keys
3. Run cleanup more frequently

## Cache Lifecycle

### Normal Workflow Run
```
1. Start workflow
   ↓
2. Restore cache (if exists)
   ↓
3. Upload cached recordings
   ↓
4. Delete uploaded files
   ↓
5. Record new content
   ↓
6. Save cache (small, no recordings)
   ↓
7. End workflow
```

### With Cancellation
```
1. Start workflow
   ↓
2. Restore cache (if exists)
   ↓
3. Upload cached recordings
   ↓
4. Record new content
   ↓
5. [CANCELLED]
   ↓
6. Emergency cleanup
   ↓
7. Save cache (may contain recordings)
   ↓
8. Next run uploads these recordings
```

## Optimization Tips

### 1. Minimize Cache Size
- Upload recordings immediately
- Don't cache unnecessary files
- Use maximum compression

### 2. Smart Cache Keys
- Include session ID for isolation
- Use run ID for uniqueness
- Add job ID for matrix jobs

### 3. Aggressive Cleanup
- Delete caches older than 5 days
- Run cleanup twice daily if needed
- Monitor usage trends

### 4. Fallback Strategy
If cache is full:
1. Workflow continues without cache
2. Recordings uploaded directly
3. No state restoration (fresh start)

## Cost Analysis

### Cache Storage Costs
- **Free tier:** 10 GB included
- **Paid tier:** Additional storage available
- **Cleanup:** Free (no cost for deletions)

### Workflow Minutes Impact
- Cache save: ~30 seconds per job
- Cache restore: ~20 seconds per job
- Cleanup workflow: ~1 minute daily

### Trade-offs
- **More cache:** Faster restores, but hits limit
- **Less cache:** Slower restores, but more headroom
- **Optimal:** 7-day retention with daily cleanup

## Summary

**Key Points:**
1. ✅ Daily automatic cleanup prevents limit issues
2. ✅ Better cache keys provide isolation
3. ✅ Immediate recording upload minimizes cache size
4. ✅ Manual cleanup available for emergencies
5. ✅ Monitoring helps catch issues early

**Action Items:**
- [ ] Enable cache-cleanup workflow
- [ ] Monitor cache usage weekly
- [ ] Set up alerts at 8 GB threshold
- [ ] Review cache strategy monthly
- [ ] Document any cache-related incidents

**Resources:**
- GitHub Actions Cache Docs: https://docs.github.com/en/actions/using-workflows/caching-dependencies-to-speed-up-workflows
- Cache Limits: https://docs.github.com/en/actions/learn-github-actions/usage-limits-billing-and-administration
