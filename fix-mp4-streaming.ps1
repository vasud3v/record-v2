param(
    [Parameter(Mandatory=$true, Position=0)]
    [string]$InputFile,
    
    [Parameter(Mandatory=$false, Position=1)]
    [string]$OutputFile = ""
)

# Script to fix MP4 files for web streaming
# This moves the moov atom to the beginning of the file for progressive download/streaming

Write-Host ""
Write-Host "🔧 MP4 Streaming Fix Tool" -ForegroundColor Cyan
Write-Host "=" * 50 -ForegroundColor Cyan
Write-Host ""

# Check if input file exists
if (-not (Test-Path $InputFile)) {
    Write-Host "❌ Error: Input file '$InputFile' not found" -ForegroundColor Red
    exit 1
}

# Check if ffmpeg is installed
$ffmpegPath = Get-Command ffmpeg -ErrorAction SilentlyContinue
if (-not $ffmpegPath) {
    Write-Host "❌ Error: ffmpeg is not installed" -ForegroundColor Red
    Write-Host "Download from: https://ffmpeg.org/download.html" -ForegroundColor Yellow
    exit 1
}

# Determine output file
$InPlace = $false
if ([string]::IsNullOrEmpty($OutputFile)) {
    # In-place fix: use temporary file
    $OutputFile = [System.IO.Path]::ChangeExtension($InputFile, "") + "_fixed_temp.mp4"
    $InPlace = $true
}

Write-Host "Input:  $InputFile" -ForegroundColor White
Write-Host "Output: $OutputFile" -ForegroundColor White
Write-Host ""

# Run ffmpeg with faststart flag
Write-Host "⏳ Processing..." -ForegroundColor Yellow

try {
    $process = Start-Process -FilePath "ffmpeg" `
        -ArgumentList "-nostdin", "-y", "-i", "`"$InputFile`"", "-c", "copy", "-movflags", "+faststart", "`"$OutputFile`"" `
        -NoNewWindow -Wait -PassThru -RedirectStandardError "ffmpeg_error.log"
    
    if ($process.ExitCode -eq 0 -and (Test-Path $OutputFile) -and ((Get-Item $OutputFile).Length -gt 0)) {
        $inputSize = (Get-Item $InputFile).Length
        $outputSize = (Get-Item $OutputFile).Length
        
        Write-Host ""
        Write-Host "✅ Success!" -ForegroundColor Green
        Write-Host "   Input size:  $([math]::Round($inputSize / 1MB, 2)) MB" -ForegroundColor White
        Write-Host "   Output size: $([math]::Round($outputSize / 1MB, 2)) MB" -ForegroundColor White
        
        # If in-place, replace original
        if ($InPlace) {
            Remove-Item $InputFile -Force
            Move-Item $OutputFile $InputFile -Force
            Write-Host "   ✅ Original file replaced with fixed version" -ForegroundColor Green
        }
        
        Write-Host ""
        Write-Host "The video should now play correctly in web browsers and streaming platforms." -ForegroundColor Green
        
        # Cleanup error log
        if (Test-Path "ffmpeg_error.log") {
            Remove-Item "ffmpeg_error.log" -Force
        }
        
        exit 0
    } else {
        Write-Host ""
        Write-Host "❌ Error: ffmpeg processing failed" -ForegroundColor Red
        
        if (Test-Path "ffmpeg_error.log") {
            Write-Host ""
            Write-Host "Error details:" -ForegroundColor Yellow
            Get-Content "ffmpeg_error.log" | Select-Object -Last 20 | Write-Host -ForegroundColor Red
            Remove-Item "ffmpeg_error.log" -Force
        }
        
        if (Test-Path $OutputFile) {
            Remove-Item $OutputFile -Force
        }
        exit 1
    }
} catch {
    Write-Host ""
    Write-Host "❌ Error: $($_.Exception.Message)" -ForegroundColor Red
    
    if (Test-Path $OutputFile) {
        Remove-Item $OutputFile -Force
    }
    
    if (Test-Path "ffmpeg_error.log") {
        Remove-Item "ffmpeg_error.log" -Force
    }
    
    exit 1
}
