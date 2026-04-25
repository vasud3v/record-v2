# Troubleshooting Guide

## Issue: "Channel is in a private show" for Public Streams

### Symptoms
- Channel is live and public on Chaturbate website
- GitHub Actions logs show: `channel is in a private show, try again in 5 min(s)`
- Works perfectly when running locally

### Root Cause
This issue has **multiple layers** that all need to work together:

#### Layer 1: Cloudflare IP-Based Blocking ✅ SOLVED
- **Problem**: GitHub Actions uses datacenter IPs that Cloudflare flags as bots
- **Solution**: FlareSolverr + TLS fingerprint spoofing (CycleTLS)

#### Layer 2: FlareSolverr Timeout ⚠️ CRITICAL
- **Problem**: FlareSolverr was timing out after 60 seconds
- **Impact**: Falls back to old cookies from `settings.json` which are tied to YOUR home IP
- **Result**: Cloudflare blocks because cookies don't match GitHub Actions IP
- **Solution**: Increased timeout to 120 seconds

#### Layer 3: Session Establishment
- **Problem**: Chaturbate API requires session establishment before returning HLS source
- **Solution**: Visit homepage + room page before calling API

#### Layer 4: Cookie Completeness
- **Problem**: Only capturing `cf_clearance` cookie, missing session cookies
- **Solution**: Capture ALL cookies from FlareSolverr

### How to Verify It's Working

Look for these log messages in GitHub Actions:

#### ✅ SUCCESS Indicators:
```
✅ Successfully refreshed Cloudflare cookies!
   New cf_clearance: sqZf5sPKfIRSUdhsa...
   Total cookies received: 5
   Cookie names: [cf_clearance csrftoken ...]
```

```
[DEBUG] rusiksb31: Visiting homepage to establish session: https://chaturbate.com
[DEBUG] rusiksb31: Homepage visit successful
[DEBUG] rusiksb31: Visiting room page: https://chaturbate.com/rusiksb31/
[DEBUG] rusiksb31: Room page visit successful (body length: 45678)
[INFO] rusiksb31: API Response - room_status="public", hls_source_present=true, code="", num_viewers=123
INFO [rusiksb31] channel is online
INFO [rusiksb31] recording started
```

#### ❌ FAILURE Indicators:
```
⚠️  Warning: Failed to refresh cookies with FlareSolverr: ... Timeout after 60.0 seconds
   Continuing with existing cookies from settings.json
```
**This means FlareSolverr timed out and you're using wrong cookies!**

```
INFO [rusiksb31] channel was blocked by Cloudflare (cookies configured); retrying in 10s
```
**This means cookies are from wrong IP or TLS fingerprint mismatch**

```
[INFO] rusiksb31: API Response - room_status="private", hls_source_present=false
```
**This could be:**
- Actually in a private show (wait 5 minutes)
- Age verification required (need authenticated cookies)
- Session not established (homepage/room visit failed)

### Current Implementation Status

| Component | Status | Description |
|-----------|--------|-------------|
| **FlareSolverr** | ✅ Configured | Runs in Docker container, solves Cloudflare |
| **TLS Spoofing** | ✅ Implemented | CycleTLS spoofs Chrome fingerprint |
| **Cookie Refresh** | ✅ Implemented | Gets fresh cookies on startup |
| **All Cookies** | ✅ Implemented | Captures all cookies, not just cf_clearance |
| **Session Warmup** | ✅ Implemented | Visits homepage + room before API |
| **Timeout Fix** | ✅ Implemented | 120s timeout for FlareSolverr |
| **Debug Logging** | ✅ Implemented | Comprehensive logs for troubleshooting |

### What to Check Next

1. **Wait for next GitHub Actions run** with the timeout fix
2. **Look for the SUCCESS indicators** in the logs
3. **If still failing**, check these:
   - Is FlareSolverr container running? (should see it in services)
   - Is `USE_FLARESOLVERR=true` set? (should see in env vars)
   - Are there any FlareSolverr errors? (check container logs)

### Advanced Debugging

If you need to debug further, check the FlareSolverr container logs:
```bash
docker logs <flaresolverr-container-id>
```

Look for:
- `Challenge solved successfully`
- `Cookies extracted: [...]`
- Any error messages about timeouts or failures

### Why 120 Seconds?

Cloudflare's challenges can take varying amounts of time:
- **Simple challenges**: 5-10 seconds
- **Complex challenges**: 30-60 seconds
- **Very complex challenges**: 60-120 seconds

The 60-second timeout was too aggressive for complex challenges. 120 seconds provides enough buffer while still failing fast if there's a real problem.

### Expected Behavior After Fix

1. **Startup** (0-120s):
   - FlareSolverr launches Chrome
   - Solves Cloudflare challenge
   - Extracts fresh cookies
   - Application starts with valid cookies

2. **First Check** (immediately):
   - Visits homepage (establishes session)
   - Visits room page (confirms access)
   - Calls API (gets HLS source)
   - Starts recording if online

3. **Ongoing** (every 5 minutes):
   - Checks if channel is online
   - Records when live
   - Uploads when complete

### Common Mistakes

❌ **Using cookies from browser**: These are tied to YOUR IP, won't work in GitHub Actions
✅ **Let FlareSolverr get cookies**: Fresh cookies for GitHub Actions IP

❌ **Only cf_clearance cookie**: Missing session cookies
✅ **All cookies from FlareSolverr**: Complete authentication

❌ **60-second timeout**: Too short for complex challenges
✅ **120-second timeout**: Enough time for any challenge

❌ **Direct API call**: No session established
✅ **Homepage → Room → API**: Proper session flow

### Still Not Working?

If after all these fixes it's still not working, the issue might be:

1. **Geo-restrictions**: Chaturbate might block GitHub Actions datacenter locations
   - Solution: Use a proxy in a different region

2. **Account-level restrictions**: Some streams require logged-in accounts
   - Solution: Get cookies from a logged-in browser session

3. **Rate limiting**: Too many requests from same IP
   - Solution: Add delays between checks

4. **Chaturbate API changes**: They might have changed their API
   - Solution: Check if API endpoint still works in browser DevTools
