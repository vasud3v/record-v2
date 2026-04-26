param(
    [Parameter(Mandatory=$true)]
    [string]$ApiKey
)

Write-Host "=========================================" -ForegroundColor Cyan
Write-Host "Testing Filester API v1" -ForegroundColor Cyan
Write-Host "=========================================" -ForegroundColor Cyan
Write-Host ""

# Create test file
$testFile = "test-upload-$(Get-Date -Format 'yyyyMMddHHmmss').txt"
"This is a test file created at $(Get-Date)" | Out-File -FilePath $testFile -Encoding UTF8
Write-Host "Created test file: $testFile"
Write-Host ""

# Test upload
Write-Host "Test: Uploading file to Filester..." -ForegroundColor Yellow
Write-Host ""

if ($ApiKey -eq "guest") {
    Write-Host "Using guest upload (no authentication)..."
    $headers = @{
        "Content-Type" = "multipart/form-data"
    }
} else {
    Write-Host "Using authenticated upload..."
    Write-Host "API Key (first 10 chars): $($ApiKey.Substring(0, [Math]::Min(10, $ApiKey.Length)))..."
    $headers = @{
        "Authorization" = "Bearer $ApiKey"
    }
}

try {
    # Read file content
    $fileBytes = [System.IO.File]::ReadAllBytes((Resolve-Path $testFile))
    $fileContent = [System.Text.Encoding]::GetString($fileBytes)
    
    # Create multipart form data
    $boundary = [System.Guid]::NewGuid().ToString()
    $LF = "`r`n"
    
    $bodyLines = (
        "--$boundary",
        "Content-Disposition: form-data; name=`"file`"; filename=`"$testFile`"",
        "Content-Type: text/plain$LF",
        $fileContent,
        "--$boundary--$LF"
    ) -join $LF
    
    $headers["Content-Type"] = "multipart/form-data; boundary=$boundary"
    
    Write-Host "Sending request to https://u1.filester.me/api/v1/upload..."
    
    $response = Invoke-RestMethod -Uri "https://u1.filester.me/api/v1/upload" `
        -Method Post `
        -Headers $headers `
        -Body $bodyLines `
        -ErrorAction Stop
    
    Write-Host ""
    Write-Host "Response:" -ForegroundColor Green
    $response | ConvertTo-Json -Depth 10 | Write-Host
    Write-Host ""
    
    if ($response.success -eq $true -and $response.url) {
        Write-Host "✅ Upload successful!" -ForegroundColor Green
        Write-Host "File URL: $($response.url)" -ForegroundColor Green
        Write-Host "Slug: $($response.slug)" -ForegroundColor Green
    } else {
        Write-Host "❌ Upload failed" -ForegroundColor Red
        Write-Host "Message: $($response.message)" -ForegroundColor Red
    }
    
} catch {
    Write-Host "❌ Upload failed with error:" -ForegroundColor Red
    Write-Host $_.Exception.Message -ForegroundColor Red
    
    if ($_.Exception.Response) {
        $statusCode = $_.Exception.Response.StatusCode.value__
        Write-Host "HTTP Status Code: $statusCode" -ForegroundColor Red
        
        try {
            $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
            $responseBody = $reader.ReadToEnd()
            Write-Host "Response Body: $responseBody" -ForegroundColor Red
        } catch {
            Write-Host "Could not read response body" -ForegroundColor Red
        }
    }
    
    Write-Host ""
    Write-Host "Possible issues:" -ForegroundColor Yellow
    Write-Host "1. Invalid API key (should be Bearer token from Account Settings)"
    Write-Host "2. Filester.me service is down"
    Write-Host "3. Network connectivity issue"
    Write-Host "4. Rate limiting (1,000 requests/hour)"
    Write-Host ""
    Write-Host "Try visiting https://filester.me in your browser to check if the service is online"
}

# Cleanup
Remove-Item $testFile -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "=========================================" -ForegroundColor Cyan
Write-Host "Test complete" -ForegroundColor Cyan
Write-Host "=========================================" -ForegroundColor Cyan
