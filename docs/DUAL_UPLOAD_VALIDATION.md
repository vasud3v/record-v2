# Dual Upload Validation - Gofile & Filester

## Overview
This document describes how the system validates that recordings are successfully uploaded to **BOTH** Gofile and Filester storage services before considering the operation successful.

## Critical Requirements

### 1. Both Uploads Must Succeed
- **Gofile upload** must complete successfully with a valid URL
- **Filester upload** must complete successfully with a valid URL
- If **either** upload fails, the entire operation fails
- Partial success (one service succeeding) is **NOT** acceptable

### 2. Supabase Storage is Mandatory
- All recording metadata **must** be stored in Supabase
- If Supabase is not configured, the handler returns `nil` during initialization
- If Supabase insert fails, the entire operation fails
- This ensures centralized tracking of all recordings

## Upload Validation Flow

### Step 1: Parallel Upload Execution
```go
// Launch both uploads concurrently
go func() { /* Upload to Gofile */ }()
go func() { /* Upload to Filester */ }()

// Wait for both to complete
gofileResp := <-gofileChan
filesterResp := <-filesterChan
```

### Step 2: Success Validation
```go
// Check if BOTH uploads succeeded
gofileSuccess := gofileResp.err == nil && gofileResp.url != ""
filesterSuccess := filesterResp.err == nil && filesterResp.url != ""

// BOTH must succeed - fail if either one fails
if !gofileSuccess || !filesterSuccess {
    // Operation FAILS
    // Falls back to GitHub Artifacts
    return result, result.Error
}
```

### Step 3: Success Criteria
For the upload to be considered successful:
1. ✅ Gofile upload returns `err == nil`
2. ✅ Gofile upload returns non-empty URL
3. ✅ Filester upload returns `err == nil`
4. ✅ Filester upload returns non-empty URL

If **any** of these conditions fail, the operation fails.

## Failure Scenarios

### Scenario 1: Both Uploads Fail
```
Result: FAIL
Error: "BOTH uploads failed - Gofile: [error], Filester: [error]"
Action: Fall back to GitHub Artifacts
```

### Scenario 2: Gofile Succeeds, Filester Fails
```
Result: FAIL
Error: "Filester upload failed: [error] (Gofile succeeded)"
Action: Fall back to GitHub Artifacts
Note: Even though Gofile succeeded, we require BOTH
```

### Scenario 3: Filester Succeeds, Gofile Fails
```
Result: FAIL
Error: "Gofile upload failed: [error] (Filester succeeded)"
Action: Fall back to GitHub Artifacts
Note: Even though Filester succeeded, we require BOTH
```

### Scenario 4: Both Uploads Succeed
```
Result: SUCCESS
Action: 
  1. Store metadata in Supabase (REQUIRED)
  2. Store metadata in JSON database
  3. Send success notification
  4. Delete local file
```

## Retry Logic

Each upload service has independent retry logic:
- **Retry attempts**: 3 attempts per service
- **Backoff strategy**: Exponential backoff
- **Timeout**: 5 minutes per upload attempt
- **Independence**: Gofile and Filester retries are independent

```go
// Each service retries independently
err := RetryWithBackoff(ctx, 3, func() error {
    url, uploadErr := su.uploadToGofileOnce(ctx, server, filePath)
    // ...
})
```

## Fallback Mechanism

When either upload fails after all retries:

1. **Log detailed error** with which service(s) failed
2. **Preserve local file** (don't delete it)
3. **Call FallbackToArtifacts()** to log file for GitHub Artifacts upload
4. **Send notification** about the failure and fallback
5. **Return error** to caller (operation fails)

The workflow YAML should have an artifact upload step:
```yaml
- name: Upload artifacts on failure
  if: failure()
  uses: actions/upload-artifact@v4
  with:
    name: recordings-${{ matrix.job_id }}-${{ github.run_id }}
    path: ./videos/**/*
    retention-days: 7
```

## Supabase Validation

After successful dual upload, Supabase storage is validated:

### Initialization Check
```go
func NewRecordingCompletionHandler(...) *RecordingCompletionHandler {
    if supabaseManager == nil {
        log.Printf("ERROR: supabaseManager is nil - Supabase is REQUIRED")
        return nil  // Handler creation fails
    }
    // ...
}
```

### Runtime Check
```go
if rch.supabaseManager == nil {
    err := fmt.Errorf("Supabase manager not configured - Supabase storage is required")
    // Send CRITICAL notification
    return err  // Operation fails
}
```

### Insert Validation
```go
insertedRecord, err := rch.supabaseManager.InsertRecording(supabaseRecording)
if err != nil {
    log.Printf("CRITICAL ERROR: Failed to add recording to Supabase: %v", err)
    // Send CRITICAL notification
    return fmt.Errorf("failed to store recording in Supabase: %w", err)  // Operation fails
}
```

## File Cleanup

Local files are only deleted after **BOTH** uploads succeed:

```go
// Delete local file after successful dual upload
if gofileSuccess && filesterSuccess {
    log.Printf("Both uploads succeeded, deleting local file: %s", filePath)
    if err := os.Remove(filePath); err != nil {
        log.Printf("Warning: Failed to delete local file %s: %v", filePath, err)
        // Don't fail the upload operation if file deletion fails
    }
}
```

If either upload fails, the file is preserved for GitHub Artifacts.

## Monitoring & Notifications

### Success Notification
```
Title: "Recording Completed - {channel}"
Content:
  - Channel name
  - Duration
  - File size
  - Quality
  - Gofile URL
  - Filester URL
  - Session ID
  - Matrix Job ID
```

### Failure Notification
```
Title: "Upload Failed - Fallback to Artifacts - {channel}"
Content:
  - Which service(s) failed
  - File details
  - Error message
  - Fallback action taken
  - Artifact retention period
```

### Supabase Failure Notification
```
Title: "Supabase Update Failed - CRITICAL"
Content:
  - Channel name
  - Error message
  - Note that recording is uploaded but not in database
```

## Verification Checklist

Before considering a recording successfully processed:

- [ ] Gofile upload completed with valid URL
- [ ] Filester upload completed with valid URL
- [ ] Supabase record inserted successfully
- [ ] JSON database updated
- [ ] Success notification sent
- [ ] Local file deleted

If **any** of these fail, the operation is considered failed.

## Code References

- **Upload validation**: `github_actions/storage_uploader.go` - `UploadRecording()` method
- **Supabase validation**: `github_actions/recording_completion_handler.go` - `HandleRecordingCompletion()` method
- **Retry logic**: `github_actions/storage_uploader.go` - `UploadToGofile()` and `UploadToFilester()` methods
- **Fallback mechanism**: `github_actions/storage_uploader.go` - `FallbackToArtifacts()` method

## Summary

The system enforces **strict dual-upload validation**:
1. ✅ **Both** Gofile and Filester uploads must succeed
2. ✅ **Supabase** storage is mandatory
3. ✅ Partial success is treated as failure
4. ✅ Failed uploads fall back to GitHub Artifacts
5. ✅ Local files are only deleted after complete success
6. ✅ All failures trigger notifications for monitoring
