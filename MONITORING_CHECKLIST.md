# GitHub Actions Recording Monitoring Checklist

## ✅ Current Status: WORKING
Recording started at **17:35:56** and passed the 5-minute mark at **17:40:50** with **184.19 MB** recorded.

## Known Issues (Non-Critical)

### 1. FlareSolverr Timeout (⚠️ Warning)
**Status:** Occurring but handled gracefully
```
⚠️ Attempt 1/2/3 failed: context deadline exceeded
```
- **Impact:** Cookie refresh fails, continues with cached cookies
- **Fix Applied:** Increased timeout to 5 minutes
- **Action:** Monitor - if recordings fail with Cloudflare errors, this is the cause

### 2. Session Duplication Warning (⚠️ Warning)
```
[WARN] HTTP 403 in CycleTLS - Response body: w3: session_duplicated
```
- **Impact:** Might indicate multiple concurrent requests
- **Risk:** Could trigger Chaturbate anti-bot detection
- **Action:** Monitor for Cloudflare blocks or stream access issues

### 3. Quality Downgrade (ℹ️ Info)
```
resolution 1080p (target: 2160p), framerate 30fps (target: 60fps)
```
- **Cause:** Stream doesn't offer 4K
- **Status:** Expected behavior - records best available quality
- **Action:** None needed

## What to Monitor

### Every 5 Minutes (Progress Reports)
Look for these messages:
```
INFO [channel] —— Recording Active | Duration: X:XX:XX | Current File: XXX MB
```

**Expected timeline:**
- 17:40:50 → 5 minutes ✅ CONFIRMED
- 17:45:50 → 10 minutes
- 17:50:50 → 15 minutes
- 17:55:50 → 20 minutes
- Continue every 5 minutes...

### Critical Errors to Watch For

#### 1. Stream Ended Prematurely
```
ERROR [channel] stream ended
INFO [channel] stream ended, checking again in 10s
```
**If this happens before stream actually ends:**
- Stale timeout might need further increase
- Network issues between GitHub Actions and streaming server

#### 2. Cloudflare Blocking
```
ERROR [channel] channel was blocked by Cloudflare
```
**If this happens:**
- FlareSolverr cookie refresh failed
- Cookies expired or invalid for GitHub Actions IP
- May need manual cookie update

#### 3. Disk Space Critical
```
ERROR [channel] disk space critical
DISK CRITICAL (X.XGB free, XX%)
```
**If this happens:**
- Background uploader not keeping up
- Recording will pause until space is freed
- Check upload service (Gofile/Filester) status

#### 4. Playlist Decode Errors
```
ERROR [channel] failed to decode m3u8 playlist: #EXTM3U absent
```
**If this persists:**
- Cloudflare challenge page instead of playlist
- Cookie/authentication issue
- Network connectivity problem

### Background Uploader Messages (Every 90 seconds)
```
Skipping active recording file (goondvr running, no/zero sequence): filename.mp4
```
**This is NORMAL** - means recording is active and protected from premature upload

## Success Indicators

✅ **Recording is working if you see:**
1. Progress reports every 5 minutes with increasing duration
2. File size growing (should be ~30-40 MB per minute for 1080p)
3. No error messages between progress reports
4. Background uploader skipping the active file

## Expected Behavior

### Recording Phase (0-5 hours)
- Progress reports every 5 minutes
- File grows continuously
- Background uploader skips active file
- No errors or warnings

### Graceful Shutdown (5.4 hours)
```
Shutdown threshold reached (5.40 hours), initiating graceful shutdown
```
- Stops accepting new recordings
- Waits up to 5 minutes for active recording to complete
- Triggers next workflow run
- Uploads completed recording
- Saves state to cache

### Emergency Shutdown (if cancelled)
```
⚠️ Workflow cancellation detected - initiating emergency shutdown
📼 Saving in-progress recordings...
```
- Stops recording immediately
- Uploads partial recording
- Saves state to cache

## File Size Estimates

**1080p @ 30fps:**
- ~30-40 MB per minute
- ~1.8-2.4 GB per hour
- ~9-12 GB for 5 hours

**Disk space needed:**
- Recording: ~12 GB
- Conversion buffer: ~2 GB
- Total: ~15 GB per 5-hour session

## Troubleshooting

### If recording stops after 5 minutes again:
1. Check for "stream ended" or "playlist stale" errors
2. Verify stale timeout is 180 seconds in logs
3. Check network connectivity to streaming server
4. Review Cloudflare block count

### If no progress reports appear:
1. Check if goondvr process is still running
2. Look for crash or panic messages
3. Check disk space
4. Review system resource usage

### If upload fails:
1. Check Gofile/Filester API status
2. Verify API keys are valid
3. Check network connectivity
4. Review file size (some services have limits)

## Current Configuration

- **Stale Timeout:** 180 seconds (3 minutes) in GitHub Actions
- **MaxDuration:** 0 (disabled - continuous recording)
- **MaxFilesize:** 0 (disabled)
- **FlareSolverr Timeout:** 300 seconds (5 minutes)
- **Workflow Timeout:** 300 minutes (5 hours)
- **Graceful Shutdown:** 5.4 hours (324 minutes)

## Next Steps

1. ✅ Confirm 10-minute progress report (17:45:50)
2. ✅ Confirm 15-minute progress report (17:50:50)
3. ✅ Monitor for any errors or warnings
4. ✅ Verify recording completes and uploads successfully
5. ✅ Check database entry is created
6. ✅ Verify next workflow run is triggered automatically
