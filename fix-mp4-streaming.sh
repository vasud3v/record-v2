#!/bin/bash

# Script to fix MP4 files for web streaming
# This moves the moov atom to the beginning of the file for progressive download/streaming

if [ $# -eq 0 ]; then
    echo "Usage: $0 <input.mp4> [output.mp4]"
    echo ""
    echo "Fixes MP4 files for web streaming by moving metadata (moov atom) to the beginning."
    echo "This allows the video to start playing before the entire file is downloaded."
    echo ""
    echo "Examples:"
    echo "  $0 video.mp4                    # Fixes in-place (replaces original)"
    echo "  $0 video.mp4 video_fixed.mp4    # Creates a new fixed file"
    exit 1
fi

INPUT_FILE="$1"
OUTPUT_FILE="${2:-}"

# Check if input file exists
if [ ! -f "$INPUT_FILE" ]; then
    echo "❌ Error: Input file '$INPUT_FILE' not found"
    exit 1
fi

# Check if ffmpeg is installed
if ! command -v ffmpeg &> /dev/null; then
    echo "❌ Error: ffmpeg is not installed"
    echo "Install it with: sudo apt-get install ffmpeg"
    exit 1
fi

# Determine output file
if [ -z "$OUTPUT_FILE" ]; then
    # In-place fix: use temporary file
    OUTPUT_FILE="${INPUT_FILE%.mp4}_fixed_temp.mp4"
    IN_PLACE=true
else
    IN_PLACE=false
fi

echo "🔧 Fixing MP4 for streaming..."
echo "   Input:  $INPUT_FILE"
echo "   Output: $OUTPUT_FILE"
echo ""

# Run ffmpeg with faststart flag
echo "⏳ Processing..."
if ffmpeg -nostdin -y -i "$INPUT_FILE" -c copy -movflags +faststart "$OUTPUT_FILE" 2>&1 | \
   grep -v "frame=" | grep -v "time=" | grep -v "speed=" | grep -v "^$"; then
    
    # Check if output file was created successfully
    if [ -f "$OUTPUT_FILE" ] && [ -s "$OUTPUT_FILE" ]; then
        INPUT_SIZE=$(stat -f%z "$INPUT_FILE" 2>/dev/null || stat -c%s "$INPUT_FILE" 2>/dev/null)
        OUTPUT_SIZE=$(stat -f%z "$OUTPUT_FILE" 2>/dev/null || stat -c%s "$OUTPUT_FILE" 2>/dev/null)
        
        echo ""
        echo "✅ Success!"
        echo "   Input size:  $(numfmt --to=iec-i --suffix=B $INPUT_SIZE 2>/dev/null || echo "$INPUT_SIZE bytes")"
        echo "   Output size: $(numfmt --to=iec-i --suffix=B $OUTPUT_SIZE 2>/dev/null || echo "$OUTPUT_SIZE bytes")"
        
        # If in-place, replace original
        if [ "$IN_PLACE" = true ]; then
            mv "$OUTPUT_FILE" "$INPUT_FILE"
            echo "   ✅ Original file replaced with fixed version"
        fi
        
        echo ""
        echo "The video should now play correctly in web browsers and streaming platforms."
        exit 0
    else
        echo ""
        echo "❌ Error: Output file was not created or is empty"
        rm -f "$OUTPUT_FILE"
        exit 1
    fi
else
    echo ""
    echo "❌ Error: ffmpeg processing failed"
    rm -f "$OUTPUT_FILE"
    exit 1
fi
