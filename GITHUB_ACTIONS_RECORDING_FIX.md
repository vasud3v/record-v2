# GitHub Actions Recording Duration Fix

## Problem
Recordings in GitHub Actions were stopping after approximately 5 minutes, while local recordings worked correctly for the full stream duration.

## Root Cause
The issue was caused by two factors:

### 1. File Splitting (Minor Issue)
- **Previous behavior**: `MaxDuration` was set to 60 minutes, causing recordings to split into 1-hour segments
- **Impact**: This created multiple files but didn't stop the recording
- **Fix**: Changed `MaxDuration` to 0 to disable file splitting

### 2. Stale Stream Detection (Major Issue)
- **Previous behavior**: If no new HLS segments arrived for 90 seconds, the stream was considered "ended"
- **Impact**: GitHub Actions runners have variable network performance, causing segment fetching delays
- **Result**: Stream detected as ended → recording stops → restarts after 10 seconds → creates multiple 5-minute files
- **Fix**: Increased stale timeout to 180 seconds (3 minutes) specifically for GitHub Actions environment

## Changes Made

### File: `github_actions/github_actions_mode.go`
**Line 569**: Changed MaxDuration from 60 to 0
```go
// BEFORE
MaxDuration: 60, // Split into 1-hour segments to enable background uploads

// AFTER  
MaxDuration: 0, // CHANGED: Disable file splitting - let recordings run continuously
```

### File: `chaturbate/chaturbate.go`
**Lines 912-917 and 1078-1083**: Made stale timeout dynamic based on environment
```go
// BEFORE
const staleTimeout = 90 * time.Second // If no new segments for 90s, consider stream ended

// AFTER
// Use longer timeout in GitHub Actions to account for network variability
staleTimeout := 90 * time.Second
if os.Getenv("GITHUB_ACTIONS") == "true" {
    staleTimeout = 180 * time.Second // 3 minutes for GitHub Actions
}
```

## Why This Works

### Local Environment
- Stable network connection
- Low latency to streaming servers
- 90-second timeout is sufficient

### GitHub Actions Environment
- Variable network performance
- Potential routing delays
- Shared infrastructure
- 180-second timeout provides buffer for network variability

## Testing
After deploying these changes:
1. Recordings should continue for the full stream duration
2. No more premature "stream ended" detections
3. Single continuous recording file per stream session
4. Background uploader will still process completed recordings

## Additional Notes
- The fix is environment-aware and only affects GitHub Actions
- Local recordings maintain the original 90-second timeout
- If streams genuinely end, they will still be detected (just with a longer grace period)
- The 10-second retry mechanism remains in place for actual stream interruptions
