#!/bin/bash

# Test script for Filester API v1
# Usage: ./test-filester.sh YOUR_API_KEY

API_KEY="$1"

if [ -z "$API_KEY" ]; then
  echo "Usage: $0 <FILESTER_API_KEY>"
  echo "Example: $0 your_bearer_token_here"
  echo ""
  echo "Or test guest upload (no API key):"
  echo "$0 guest"
  exit 1
fi

echo "========================================="
echo "Testing Filester API v1"
echo "========================================="
echo ""

# Create test file
TEST_FILE="test-upload-$(date +%s).txt"
echo "This is a test file created at $(date)" > "$TEST_FILE"
echo "Created test file: $TEST_FILE"
echo ""

# Test upload
echo "Test: Uploading file to Filester..."
echo ""

if [ "$API_KEY" = "guest" ]; then
  echo "Using guest upload (no authentication)..."
  RESPONSE=$(curl -v -X POST "https://u1.filester.me/api/v1/upload" \
    -F "file=@$TEST_FILE" 2>&1)
else
  echo "Using authenticated upload..."
  echo "API Key (first 10 chars): ${API_KEY:0:10}..."
  RESPONSE=$(curl -v -X POST "https://u1.filester.me/api/v1/upload" \
    -H "Authorization: Bearer $API_KEY" \
    -F "file=@$TEST_FILE" 2>&1)
fi

echo "$RESPONSE"
echo ""

# Extract HTTP code
HTTP_CODE=$(echo "$RESPONSE" | grep "< HTTP" | tail -1)
echo "HTTP Response: $HTTP_CODE"
echo ""

# Try to parse JSON response
JSON_RESPONSE=$(echo "$RESPONSE" | grep -v "^[<>*]" | tail -1)
echo "JSON Response: $JSON_RESPONSE"
echo ""

# Try to extract data
SUCCESS=$(echo "$JSON_RESPONSE" | jq -r '.success' 2>/dev/null)
SLUG=$(echo "$JSON_RESPONSE" | jq -r '.slug' 2>/dev/null)
MESSAGE=$(echo "$JSON_RESPONSE" | jq -r '.message' 2>/dev/null)
FILE_ID=$(echo "$JSON_RESPONSE" | jq -r '.file_id' 2>/dev/null)

# Construct download URL from slug
if [ -n "$SLUG" ] && [ "$SLUG" != "null" ]; then
  DOWNLOAD_URL="https://filester.me/d/$SLUG"
else
  DOWNLOAD_URL=""
fi

echo "Success: $SUCCESS"
echo "Slug: $SLUG"
echo "File ID: $FILE_ID"
echo "Download URL: $DOWNLOAD_URL"
echo "Message: $MESSAGE"
echo ""

if [ "$SUCCESS" = "true" ] && [ -n "$DOWNLOAD_URL" ]; then
  echo "✅ Upload successful!"
  echo "File URL: $DOWNLOAD_URL"
else
  echo "❌ Upload failed"
  echo ""
  echo "Possible issues:"
  echo "1. Invalid API key (should be Bearer token from Account Settings)"
  echo "2. Filester.me service is down"
  echo "3. Network connectivity issue"
  echo "4. Rate limiting (1,000 requests/hour)"
  echo ""
  echo "Try visiting https://filester.me in your browser to check if the service is online"
fi

# Cleanup
rm -f "$TEST_FILE"

echo ""
echo "========================================="
echo "Test complete"
echo "========================================="
