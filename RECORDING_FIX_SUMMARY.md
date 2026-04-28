# Recording Duration Fix - Complete Summary

## Problem
Recordings were stopping after approximately 5 minutes even though streams were live for much longer (1+ hours).

## Root Causes Identified

### 1. **Stale Timeout Too Short** ❌ FIXED
- **Issue**: Stale timeout was set to 30 minutes in `watchVideoOnlySegments`
- **Impact**: If no new segments arrived for 30 minutes, recording would stop
- **Fix**: Increased to 60 minutes in both `watchVideoOnlySegments` and `watchMuxedSegments`
- **Commit**: `8bbb0ab` - CRITICAL fix

### 2. **Background Uploader Interference** ❌ FIXED
- **Issue**: Background uploader was running every 90 seconds checking for completed files
- **Impact**: Could interfere with active recordings through file locking or process management
- **Fix**: Completely removed background uploader - all uploads now happen after recording completes
- **Commit**: `47c7ad5`

### 3. **Missing ENDLIST Detection** ❌ FIXED
- **Issue**: No detection of `#EXT-X-ENDLIST` tag which signals stream end
- **Impact**: Relied only on stale timeout to detect stream end
- **Fix**: Added proper ENDLIST detection in both video-only and muxed streams
- **Commit**: `b507180`

## Changes Made

### File: `chaturbate/chaturbate.go`

#### Change 1: Increased Stale Timeout (Lines 910-918, 1090-1098)
```go
// BEFORE
staleTimeout := 30 * time.Minute
if os.Getenv("GITHUB_ACTIONS") == "true" {
    staleTimeout = 30 * time.Minute // 3 minutes for GitHub Actions
}

// AFTER
staleTimeout := 60 * time.Minute
if os.Getenv("GITHUB_ACTIONS") == "true" {
    staleTimeout = 60 * time.Minute // 60 minutes for GitHub Actions
}
```

#### Change 2: Added ENDLIST Detection (Lines 1040-1048, 1380-1388)
```go
// NEW CODE
// Check for #EXT-X-ENDLIST tag which indicates stream has ended
if strings.Contains(resp, "#EXT-X-ENDLIST") {
    fmt.Printf("[INFO] Stream ended (detected #EXT-X-ENDLIST tag)\n")
    return internal.ErrChannelOffline
}
```

#### Change 3: Enhanced Logging (Lines 1050-1056, 1390-1396)
```go
// NEW CODE
if time.Since(lastSegmentTime) > staleTimeout {
    fmt.Printf("[INFO] Stream ended (no new segments for %v, last segment at %v)\n", 
        staleTimeout, lastSegmentTime.Format("15:04:05"))
    return internal.ErrChannelOffline
}
```

### File: `.github/workflows/continuous-runner.yml`

#### Change 4: Removed Background Uploader (Lines 540-670)
```yaml
# REMOVED: Entire bg_uploader() function (130 lines)
# REMOVED: bg_uploader & invocation
# REMOVED: BG_PID=$!
# REMOVED: kill "$BG_PID" cleanup code

# ADDED: Note in startup message
echo "  NOTE: Background uploader disabled - uploads happen after recording completes"
```

### File: `github_actions/github_actions_mode.go`

#### Existing Configuration (Line 569)
```go
MaxDuration: 0, // CHANGED: Disable file splitting - let recordings run continuously
```
This was already correct - no file splitting.

## Testing Instructions

### 1. Start a New Workflow Run
The fixes are now live on the `main` branch. Start a new workflow run to test.

### 2. Monitor the Logs
Look for these new log messages:

**During Recording:**
```
[INFO] —— Recording Active | Duration: 0:05:00 | Current File: 184.20 MB
[INFO] —— Recording Active | Duration: 0:10:00 | Current File: 368.40 MB
[INFO] —— Recording Active | Duration: 0:15:00 | Current File: 552.60 MB
```

**When Stream Ends:**
```
[INFO] Stream ended (detected #EXT-X-ENDLIST tag)
```
OR
```
[INFO] Stream ended (no new segments for 1h0m0s, last segment at 18:45:23)
```

### 3. Expected Behavior

✅ **Recordings should continue for the full stream duration**
- No more stopping at 5 minutes
- Will record until stream actually ends or 5-hour workflow timeout

✅ **Clean shutdown when stream ends**
- Proper detection via ENDLIST tag or 60-minute stale timeout
- Clear log message explaining why recording stopped

✅ **No upload interference**
- All uploads happen after recording completes
- No background process touching active files

## Verification Checklist

- [ ] Recording continues past 5 minutes
- [ ] Recording continues past 10 minutes
- [ ] Recording continues past 30 minutes
- [ ] Recording continues past 60 minutes
- [ ] Log shows "Recording Active" every 5 minutes
- [ ] Log shows clear reason when recording stops
- [ ] Upload happens after recording completes
- [ ] No "Skipping active recording file" messages during recording

## Commits

1. `b507180` - Initial stale timeout increase + ENDLIST detection (partial)
2. `47c7ad5` - Removed background uploader completely
3. `8bbb0ab` - **CRITICAL** - Fixed stale timeout in watchVideoOnlySegments
4. `766ab84` - Added detailed logging for stream end detection

## Next Steps

1. **Test with live stream** - Verify recordings continue for full duration
2. **Monitor logs** - Check for new INFO messages
3. **Report results** - Confirm if issue is resolved

## Rollback Instructions

If issues occur, revert to commit before `b507180`:
```bash
git revert 766ab84 8bbb0ab 47c7ad5 b507180
git push origin main
```

---

**Status**: ✅ All fixes deployed to `main` branch
**Last Updated**: 2026-04-29
**Tested**: Awaiting user confirmation with live stream
