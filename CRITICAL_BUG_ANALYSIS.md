# CRITICAL BUG: Stale Detection Defeated by Premature Timer Reset

## Executive Summary

**The previous "fix" (commit `46bd5dd`) actually BROKE stale detection by updating the timer on every playlist fetch instead of only when new segments are written.**

## The Bug

### What Was Implemented (WRONG ❌)

```go
for {
    // Fetch playlist
    resp, err := client.Get(ctx, p.PlaylistURL)
    
    // ❌ WRONG: Update timer on EVERY fetch
    lastSegmentTime = time.Now()
    
    // Process segments
    for segment in playlist {
        if segment is NEW {
            writeSegment(segment)
            // Also updates here (redundant)
            lastSegmentTime = time.Now()
        }
    }
    
    // Check stale timeout
    if time.Since(lastSegmentTime) > 180s {
        return ErrStreamEnded
    }
}
```

### Why This Is Broken

**Scenario: Stream ends but playlist is still accessible**

1. Stream ends at 10:00:00
2. Last segment written at 10:00:00
3. At 10:00:01 - Fetch playlist → **Timer resets** ❌
4. At 10:00:02 - Fetch playlist → **Timer resets** ❌
5. At 10:00:03 - Fetch playlist → **Timer resets** ❌
6. ... continues forever ...
7. **Stale timeout NEVER triggers** ❌

The playlist keeps returning successfully (HTTP 200), but with NO new segments. The timer keeps resetting, so we never detect the stream has ended.

## The Correct Implementation

### What Should Happen (CORRECT ✅)

```go
for {
    // Fetch playlist
    resp, err := client.Get(ctx, p.PlaylistURL)
    
    // ✅ CORRECT: Do NOT update timer here
    
    // Process segments
    for segment in playlist {
        if segment is NEW {
            writeSegment(segment)
            // ✅ ONLY update when NEW segment written
            lastSegmentTime = time.Now()
        }
    }
    
    // Check stale timeout
    if time.Since(lastSegmentTime) > 180s {
        return ErrStreamEnded  // ✅ Triggers correctly
    }
}
```

### Why This Works

**Scenario: Stream ends but playlist is still accessible**

1. Stream ends at 10:00:00
2. Last segment written at 10:00:00 → `lastSegmentTime = 10:00:00`
3. At 10:00:01 - Fetch playlist → No new segments → Timer NOT reset ✅
4. At 10:00:02 - Fetch playlist → No new segments → Timer NOT reset ✅
5. At 10:00:03 - Fetch playlist → No new segments → Timer NOT reset ✅
6. ... continues ...
7. At 10:03:00 - Check: `time.Since(10:00:00) = 180s` → **Stale timeout triggers** ✅
8. Recording stops cleanly ✅

## Real-World Impact

### Before This Fix (Broken Behavior)

```
18:00:00  Recording starts
18:05:00  Stream ends (last segment written)
18:05:01  Playlist fetch → Timer resets to 18:05:01
18:05:02  Playlist fetch → Timer resets to 18:05:02
18:05:03  Playlist fetch → Timer resets to 18:05:03
... continues forever ...
18:16:00  User cancels workflow (frustrated)
Result: Recording hangs, never detects stream ended ❌
```

### After This Fix (Correct Behavior)

```
18:00:00  Recording starts
18:05:00  Stream ends (last segment written at 18:05:00)
18:05:01  Playlist fetch → No new segments → Timer stays at 18:05:00
18:05:02  Playlist fetch → No new segments → Timer stays at 18:05:00
18:05:03  Playlist fetch → No new segments → Timer stays at 18:05:00
... continues ...
18:08:00  Stale check: time.Since(18:05:00) = 180s → Triggers!
18:08:00  Recording stops cleanly
Result: Proper stream end detection ✅
```

## Why The Original "Fix" Was Wrong

The commit `46bd5dd` message said:

> "Update last segment time on successful playlist fetch. This prevents false 'stale stream' detection during slow periods"

**This logic was flawed because:**

1. **Slow periods still have NEW segments** - they're just less frequent
2. When NEW segments arrive, `lastSegmentTime` gets updated (line 1041)
3. The timer should ONLY reset when NEW content arrives
4. Resetting on every fetch defeats the purpose of stale detection

## The Correct Understanding

**Stale detection should answer:** "How long has it been since we received NEW content?"

- ✅ **Correct:** Track time since last NEW segment written
- ❌ **Wrong:** Track time since last successful playlist fetch

**Why?** Because a playlist can return successfully (HTTP 200) but contain NO new segments. This happens when:
- Stream has ended
- Broadcaster stopped streaming
- CDN is still serving the last known playlist

## Files Modified

- `chaturbate/chaturbate.go`
  - Line 931: Removed premature `lastSegmentTime` update in `watchVideoOnlySegments`
  - Line 1103: Removed premature `lastSegmentTime` update in `watchMuxedSegments`
  - Line 1041: Kept correct update when segment written in `watchVideoOnlySegments`
  - Line 1334: Kept correct update when chunk written in `watchMuxedSegments` (Chaturbate)
  - Line 1451: Kept correct update when segments written in `watchMuxedSegments` (Stripchat)

## Commits

- **Broken:** `46bd5dd` - "fix: prevent false stale stream detection during slow periods"
  - This commit BROKE stale detection by updating timer on every fetch
  
- **Fixed:** `8068e3c` - "fix: CRITICAL - remove premature lastSegmentTime updates"
  - This commit FIXES stale detection by only updating when segments are written

## Testing

To verify this fix works:

1. ✅ Start recording a live stream
2. ✅ Wait for stream to end naturally
3. ✅ Recording should stop within 3 minutes (180s timeout)
4. ✅ Should NOT hang forever
5. ✅ Should show "stream ended" or "channel offline" message

## Lessons Learned

1. **Stale detection must track NEW content, not just successful API calls**
2. **A successful HTTP response doesn't mean new data is available**
3. **Timer resets should only happen when actual progress is made**
4. **"Preventing false positives" can create false negatives if done wrong**

## Summary

The previous fix tried to prevent false stale detection by updating the timer on every playlist fetch. This was wrong because it prevented ALL stale detection, including legitimate cases where the stream has ended. The correct fix is to only update the timer when NEW segments are actually written to the file.

**This is the REAL fix for the 5-minute recording issue.**
