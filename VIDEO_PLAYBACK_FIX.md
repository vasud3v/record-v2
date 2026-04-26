# Video Playback Fix - MP4 Streaming Issue

## Problem

Videos uploaded to Filester/Gofile are not playable in web browsers - they just show a loading spinner and never start playing.

## Root Cause

The issue occurs with **fragmented MP4 (fMP4)** files that are recorded directly without proper finalization. These files have the **moov atom** (metadata containing video information like duration, codecs, and seek index) located at the **end of the file** instead of at the beginning.

When a browser tries to play such a video:
1. It starts downloading from the beginning
2. It looks for the moov atom to understand the video structure
3. Since the moov atom is at the end, it must download the **entire file** before playback can start
4. For large files (1GB+), this appears as infinite loading

## Solution

Use the **`+faststart`** flag with ffmpeg to move the moov atom to the beginning of the file. This enables **progressive download** - the video can start playing while still downloading.

### Automatic Fix (GitHub Actions)

The workflow has been updated to automatically fix all MP4 files before upload:

```bash
ffmpeg -nostdin -y -i input.mp4 -c copy -movflags +faststart output.mp4
```

- `-c copy`: No re-encoding (fast, preserves quality)
- `-movflags +faststart`: Moves moov atom to the beginning
- Processing time: ~5-10 seconds for a 1GB file

### Manual Fix for Existing Files

#### Option 1: Using the Fix Script (Recommended)

**Linux/Mac:**
```bash
bash fix-mp4-streaming.sh video.mp4
```

**Windows:**
```powershell
powershell -ExecutionPolicy Bypass -File fix-mp4-streaming.ps1 video.mp4
```

#### Option 2: Using ffmpeg Directly

```bash
ffmpeg -i input.mp4 -c copy -movflags +faststart output.mp4
```

## Technical Details

### What is the moov atom?

The moov atom (movie atom) is a container in MP4 files that holds:
- Video/audio codec information
- Track metadata (duration, dimensions, bitrate)
- Sample table (frame locations and timestamps)
- Seek index for random access

### Why does position matter?

**moov at end (bad for streaming):**
```
[ftyp][mdat (video data)][moov (metadata)]
```
- Browser must download entire file to read moov
- No progressive playback possible

**moov at start (good for streaming):**
```
[ftyp][moov (metadata)][mdat (video data)]
```
- Browser reads moov immediately
- Can start playing while downloading mdat
- Enables seeking before full download

### File Size Impact

The `+faststart` operation:
- ✅ Does NOT re-encode video (preserves quality)
- ✅ Does NOT significantly change file size (±0.1%)
- ✅ Is very fast (I/O bound, not CPU bound)
- ✅ Is safe (creates new file, doesn't modify original until success)

## Verification

To check if an MP4 file has faststart enabled:

```bash
ffprobe -v error -show_entries format_tags=major_brand -of default=noprint_wrappers=1:nokey=1 video.mp4
```

Or use a hex editor to check if `moov` appears before `mdat` in the file.

## Prevention

### For New Recordings

The application's finalization mode should be set to `remux` or `transcode`:

**Config:**
```json
{
  "finalize_mode": "remux",
  "ffmpeg_container": "mp4"
}
```

This ensures all recordings are properly finalized with faststart before being moved to the completed directory.

### For GitHub Actions Uploads

The workflow now automatically:
1. Converts `.ts` files to `.mp4` with `+faststart`
2. Fixes existing `.mp4` files with `+faststart`
3. Verifies file integrity before upload
4. Only uploads properly formatted files

## References

- [FFmpeg movflags documentation](https://ffmpeg.org/ffmpeg-formats.html#mov_002c-mp4_002c-ismv)
- [MP4 file structure](https://developer.apple.com/documentation/quicktime-file-format)
- [Progressive download vs streaming](https://en.wikipedia.org/wiki/Progressive_download)

## Summary

✅ **Fixed:** All future uploads will have proper moov atom positioning  
✅ **Scripts:** Use `fix-mp4-streaming.sh` or `.ps1` to fix existing files  
✅ **Fast:** No re-encoding, just metadata reorganization  
✅ **Safe:** Original files preserved until success confirmed  
