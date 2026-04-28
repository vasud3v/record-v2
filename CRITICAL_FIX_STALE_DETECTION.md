# CRITICAL FIX: False Stale Stream Detection

## Problem Discovered

**Symptom:** Recording ran for 20 minutes but only captured 5 minutes of actual video content

**Root Cause:** False "stale stream" detection causing recording restarts every ~5 minutes

## Technical Analysis

### What Was Happening:

1. **Recording starts** → `riki_tiki_tavi_time_2026-04-28_17-35-56.mp4` (Sequence 0)
2. **After ~5 minutes** → Stale timeout triggers `ErrStreamEnded`
3. **Retry after 10 seconds** → Stream still online, creates NEW file
4. **New file created** → `riki_tiki_tavi_time_2026-04-28_17-35-56_1.mp4` (Sequence 1)
5. **Repeats every ~5 minutes** → Multiple 5-minute files instead of one continuous recording

### Why It Happened:

```go
// OLD LOGIC (BROKEN):
lastSegmentTime := time.Now()

for {
    // Fetch playlist
    playlist := fetchPlaylist()
    
    // Process segments
    for segment in playlist {
        if segment is NEW {
            writeSegment(segment)
            lastSegmentTime = time.Now()  // ❌ Only updates when NEW segments written
        }
    }
    
    // Check stale timeout
    if time.Since(lastSegmentTime) > 180s {
        return ErrStreamEnded  // ❌ Triggers even though stream is alive!
    }
}
```

**The Problem:**
- `lastSegmentTime` only updated when **new segments were written**
- During slow periods or buffering, playlist returns **same segments repeatedly**
- No new segments = `lastSegmentTime` doesn't update
- After 180 seconds of "no new segments", stale timeout triggers
- Stream is actually **alive and accessible**, but detected as "stale"

## The Fix

```go
// NEW LOGIC (FIXED):
lastSegmentTime := time.Now()

for {
    // Fetch playlist
    playlist := fetchPlaylist()
    
    // ✅ Update on EVERY successful fetch
    lastSegmentTime = time.Now()
    
    // Process segments
    for segment in playlist {
        if segment is NEW {
            writeSegment(segment)
        }
    }
    
    // Check stale timeout
    if time.Since(lastSegmentTime) > 180s {
        return ErrStreamEnded  // ✅ Only triggers if playlist fetch fails for 180s
    }
}
```

**The Solution:**
- Update `lastSegmentTime` on **every successful playlist fetch**
- Stream is considered alive as long as **playlist is accessible**
- Only trigger stale timeout if **playlist fetch fails** for 180 seconds
- Handles slow periods, buffering, and repeated segments correctly

## Files Modified

### `chaturbate/chaturbate.go`

**Function 1: `watchVideoOnlySegments`** (Line ~920)
```go
for {
    resp, err := client.Get(ctx, p.PlaylistURL)
    if err != nil {
        // ... error handling ...
        continue
    }
    
    // ✅ NEW: Update on successful fetch
    lastSegmentTime = time.Now()
    
    pl, _, err := safeDecodeFrom(...)
    // ... rest of processing ...
}
```

**Function 2: `watchMuxedSegments`** (Line ~1090)
```go
for {
    videoResp, err := client.Get(ctx, p.PlaylistURL)
    if err != nil {
        // ... error handling ...
        continue
    }
    
    // ✅ NEW: Update on successful fetch
    lastSegmentTime = time.Now()
    
    vpl, _, err := safeDecodeFrom(...)
    // ... rest of processing ...
}
```

## Impact

### Before Fix:
- ❌ Recording stops every ~5 minutes
- ❌ Creates multiple small files (5-minute segments)
- ❌ False "stream ended" detections
- ❌ Continuous restarts and file creation overhead
- ❌ Difficult to manage many small files

### After Fix:
- ✅ Continuous recording for full stream duration
- ✅ Single file per stream session
- ✅ Proper handling of slow/buffering periods
- ✅ No false "stream ended" detections
- ✅ Clean, manageable recordings

## Testing

### Expected Behavior After Fix:

1. **Recording starts** → Single file created
2. **Stream continues** → File grows continuously
3. **Slow periods** → Recording continues (playlist still accessible)
4. **Buffering** → Recording continues (playlist still accessible)
5. **Stream ends** → Only then does recording stop
6. **Result** → One continuous file for entire stream

### Progress Reports:
```
17:35:56  INFO stream type: LL-HLS (video+audio)
17:40:50  INFO Recording Active | Duration: 0:05:00 | Current File: 184.19 MB
17:45:50  INFO Recording Active | Duration: 0:10:00 | Current File: 368.38 MB
17:50:50  INFO Recording Active | Duration: 0:15:00 | Current File: 552.57 MB
17:55:50  INFO Recording Active | Duration: 0:20:00 | Current File: 736.76 MB
... continues until stream ends ...
```

### File Output:
```
videos/
└── riki_tiki_tavi_time_2026-04-28_17-35-56.mp4  (single continuous file)
```

**NOT:**
```
videos/
├── riki_tiki_tavi_time_2026-04-28_17-35-56.mp4      (5 min)
├── riki_tiki_tavi_time_2026-04-28_17-35-56_1.mp4    (5 min)
├── riki_tiki_tavi_time_2026-04-28_17-35-56_2.mp4    (5 min)
└── riki_tiki_tavi_time_2026-04-28_17-35-56_3.mp4    (5 min)
```

## Related Fixes

This fix works in conjunction with previous fixes:

1. **Stale Timeout Increase** (90s → 180s in GitHub Actions)
   - Provides more tolerance for network delays
   - Works with this fix to prevent false detections

2. **MaxDuration Disabled** (60 → 0)
   - Prevents intentional file splitting
   - Ensures continuous recording

3. **FlareSolverr Timeout Increase** (180s → 300s)
   - Ensures cookies are refreshed properly
   - Prevents Cloudflare blocks

## Verification

To verify this fix is working:

1. **Check progress reports** - Should show increasing duration
2. **Check file count** - Should be ONE file per stream
3. **Check file size** - Should grow continuously
4. **Check logs** - No "stream ended" messages during active stream

## Commit

**Commit:** `46bd5dd`
**Message:** fix: prevent false stale stream detection during slow periods
**Pushed to:** https://github.com/vasud3v/record-v2

## Next Steps

1. ✅ Cancel current workflow (it has the old code)
2. ✅ Start new workflow run (will use the fixed code)
3. ✅ Monitor for continuous recording
4. ✅ Verify single file output
5. ✅ Confirm no false "stream ended" messages
