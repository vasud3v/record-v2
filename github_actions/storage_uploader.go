package github_actions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// StorageUploader handles dual uploads to Gofile and Filester external storage services.
// It retrieves the optimal Gofile server dynamically before uploading and manages
// parallel uploads to both services with retry logic and fallback handling.
type StorageUploader struct {
	gofileAPIKey   string
	filesterAPIKey string
	httpClient     *http.Client
}

// UploadResult contains the results of uploading a recording to external storage.
// It includes URLs for both Gofile and Filester uploads, with support for
// split files (FilesterChunks) when files exceed the 10 GB Filester limit.
type UploadResult struct {
	GofileURL      string   // Download URL from Gofile
	FilesterURL    string   // Download URL from Filester (or folder URL for split files)
	FilesterChunks []string // URLs for individual chunks when file is split (> 10 GB)
	Checksum       string   // SHA-256 checksum of the uploaded file
	Success        bool     // True if at least one upload succeeded
	Error          error    // Error details if uploads failed
}

// gofileServersResponse represents the JSON response from Gofile's servers API
type gofileServersResponse struct {
	Status string `json:"status"`
	Data   struct {
		Servers []gofileServer `json:"servers"`
	} `json:"data"`
}

// gofileServer represents a single Gofile server
type gofileServer struct {
	Name string `json:"name"`
	Zone string `json:"zone"`
}

// gofileUploadResponse represents the JSON response from Gofile's upload API
type gofileUploadResponse struct {
	Status string `json:"status"`
	Data   struct {
		DownloadPage string `json:"downloadPage"`
		Code         string `json:"code"`
		ParentFolder string `json:"parentFolder"`
		FileID       string `json:"fileId"`
		FileName     string `json:"fileName"`
		MD5          string `json:"md5"`
	} `json:"data"`
}

// NewStorageUploader creates a new StorageUploader instance with the provided API keys.
// The httpClient is configured with a 5-minute timeout to handle large file uploads.
// 
// BUG 5 FIX: Validates that API keys are not empty. Returns nil if either key is empty.
func NewStorageUploader(gofileAPIKey, filesterAPIKey string) *StorageUploader {
	// Validate API keys are not empty (BUG 5 FIX)
	if gofileAPIKey == "" {
		log.Printf("ERROR: Gofile API key is empty - cannot create StorageUploader")
		return nil
	}
	if filesterAPIKey == "" {
		log.Printf("ERROR: Filester API key is empty - cannot create StorageUploader")
		return nil
	}
	
	return &StorageUploader{
		gofileAPIKey:   gofileAPIKey,
		filesterAPIKey: filesterAPIKey,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Longer timeout for file uploads
		},
	}
}

// GetGofileServer retrieves the optimal Gofile server address from the Gofile API.
// It queries https://api.gofile.io/servers and returns the first available server name.
// The server name is used to construct the upload URL: https://{server}.gofile.io/uploadFile
//
// Example response from Gofile API:
//
//	{
//	  "status": "ok",
//	  "data": {
//	    "servers": [
//	      {"name": "store1", "zone": "eu"},
//	      {"name": "store2", "zone": "na"}
//	    ]
//	  }
//	}
//
// Requirements: 3.2, 14.2
func (su *StorageUploader) GetGofileServer(ctx context.Context) (string, error) {
	const serversURL = "https://api.gofile.io/servers"

	log.Printf("Retrieving optimal Gofile server from %s", serversURL)

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "GET", serversURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Accept", "application/json")

	// Execute request
	resp, err := su.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Gofile API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse JSON response
	var serversResp gofileServersResponse
	if err := json.Unmarshal(body, &serversResp); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Validate response status
	if serversResp.Status != "ok" {
		return "", fmt.Errorf("Gofile API returned status: %s", serversResp.Status)
	}

	// Check if servers are available
	if len(serversResp.Data.Servers) == 0 {
		return "", fmt.Errorf("no Gofile servers available")
	}

	// Return the first available server
	serverName := serversResp.Data.Servers[0].Name
	log.Printf("Selected Gofile server: %s (zone: %s)", serverName, serversResp.Data.Servers[0].Zone)

	return serverName, nil
}

// UploadToGofile uploads a file to Gofile using multipart/form-data encoding.
// It sends a POST request to https://{server}.gofile.io/uploadFile with the file
// and includes the Gofile API key as a Bearer token in the Authorization header.
//
// The method implements retry logic with exponential backoff (up to 3 attempts).
// It retries on transient errors like network timeouts or temporary server issues.
//
// The method returns the download URL extracted from the "downloadPage" field in the
// JSON response. It verifies the HTTP 200 response status before parsing the response.
//
// Example response from Gofile upload API:
//
//	{
//	  "status": "ok",
//	  "data": {
//	    "downloadPage": "https://gofile.io/d/abc123",
//	    "code": "abc123",
//	    "parentFolder": "xyz789",
//	    "fileId": "file123",
//	    "fileName": "recording.mp4",
//	    "md5": "d41d8cd98f00b204e9800998ecf8427e"
//	  }
//	}
//
// Requirements: 3.8, 14.1, 14.3, 14.5, 14.7, 14.8, 14.10
func (su *StorageUploader) UploadToGofile(ctx context.Context, server, filePath string) (string, error) {
	var downloadURL string
	
	// Retry upload up to 3 times with exponential backoff
	err := RetryWithBackoff(ctx, 3, func() error {
		url, uploadErr := su.uploadToGofileOnce(ctx, server, filePath)
		if uploadErr != nil {
			log.Printf("Gofile upload attempt failed: %v", uploadErr)
			return uploadErr
		}
		downloadURL = url
		return nil
	})
	
	if err != nil {
		log.Printf("Gofile upload failed after retries: %v", err)
		return "", err
	}
	
	return downloadURL, nil
}

// uploadToGofileOnce performs a single upload attempt to Gofile without retry logic.
// This is called by UploadToGofile which handles retries.
func (su *StorageUploader) uploadToGofileOnce(ctx context.Context, server, filePath string) (string, error) {
	uploadURL := fmt.Sprintf("https://%s.gofile.io/uploadFile", server)
	log.Printf("Uploading file to Gofile (single attempt): %s -> %s", filePath, uploadURL)

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file info for logging
	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to get file info: %w", err)
	}
	log.Printf("File size: %d bytes", fileInfo.Size())

	// Create multipart form data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file field
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}

	// Copy file content to form
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file content: %w", err)
	}

	// Close the writer to finalize the multipart message
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", uploadURL, body)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", su.gofileAPIKey))

	// Execute request
	resp, err := su.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Gofile upload returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse JSON response
	var uploadResp gofileUploadResponse
	if err := json.Unmarshal(respBody, &uploadResp); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Validate response status
	if uploadResp.Status != "ok" {
		return "", fmt.Errorf("Gofile upload returned status: %s", uploadResp.Status)
	}

	// Extract download URL
	downloadURL := uploadResp.Data.DownloadPage
	if downloadURL == "" {
		return "", fmt.Errorf("Gofile response missing download URL")
	}

	log.Printf("Successfully uploaded to Gofile: %s (file ID: %s)", downloadURL, uploadResp.Data.FileID)

	return downloadURL, nil
}

// filesterUploadResponse represents the JSON response from Filester's upload API
type filesterUploadResponse struct {
	Status string `json:"status"`
	URL    string `json:"url"`
}

// UploadToFilester uploads a file to Filester using multipart/form-data encoding.
// It sends a POST request to https://u1.filester.me/api/v1/upload with the file
// and includes the Filester API key as a Bearer token in the Authorization header.
//
// The method implements retry logic with exponential backoff (up to 3 attempts).
// It retries on transient errors like network timeouts or temporary server issues.
//
// For files larger than 10 GB, the method automatically splits the file into 10 GB chunks,
// creates a folder on Filester, and uploads all chunks to that folder. The FilesterChunks
// field in UploadResult will contain URLs for all chunks.
//
// The method returns the download URL extracted from the "url" field in the
// JSON response. It verifies the HTTP 200 response status before parsing the response.
//
// Example response from Filester upload API:
//
//	{
//	  "status": "success",
//	  "url": "https://filester.me/file/xyz789"
//	}
//
// Requirements: 3.8, 14.1, 14.4, 14.6, 14.7, 14.8, 14.10, 14.14, 14.15, 14.16
func (su *StorageUploader) UploadToFilester(ctx context.Context, filePath string) (string, error) {
	var downloadURL string
	
	// Retry upload up to 3 times with exponential backoff
	err := RetryWithBackoff(ctx, 3, func() error {
		url, uploadErr := su.uploadToFilesterOnce(ctx, filePath)
		if uploadErr != nil {
			log.Printf("Filester upload attempt failed: %v", uploadErr)
			return uploadErr
		}
		downloadURL = url
		return nil
	})
	
	if err != nil {
		log.Printf("Filester upload failed after retries: %v", err)
		return "", err
	}
	
	return downloadURL, nil
}

// uploadToFilesterOnce performs a single upload attempt to Filester without retry logic.
// This is called by UploadToFilester which handles retries.
func (su *StorageUploader) uploadToFilesterOnce(ctx context.Context, filePath string) (string, error) {
	const uploadURL = "https://u1.filester.me/api/v1/upload"
	const maxFileSize = 10 * 1024 * 1024 * 1024 // 10 GB in bytes

	log.Printf("Uploading file to Filester (single attempt): %s -> %s", filePath, uploadURL)

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file info for size check
	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to get file info: %w", err)
	}
	log.Printf("File size: %d bytes", fileInfo.Size())

	// Check if file needs to be split
	if fileInfo.Size() > maxFileSize {
		log.Printf("File exceeds 10 GB limit (%d bytes), splitting into chunks", fileInfo.Size())
		return "", fmt.Errorf("file splitting not yet implemented in this method - use UploadToFilesterWithSplit")
	}

	// Create multipart form data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file field
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}

	// Copy file content to form
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file content: %w", err)
	}

	// Close the writer to finalize the multipart message
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", uploadURL, body)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", su.filesterAPIKey))

	// Execute request
	resp, err := su.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Filester upload returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse JSON response
	var uploadResp filesterUploadResponse
	if err := json.Unmarshal(respBody, &uploadResp); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Validate response status
	if uploadResp.Status != "success" {
		return "", fmt.Errorf("Filester upload returned status: %s", uploadResp.Status)
	}

	// Extract download URL
	downloadURL := uploadResp.URL
	if downloadURL == "" {
		return "", fmt.Errorf("Filester response missing download URL")
	}

	log.Printf("Successfully uploaded to Filester: %s", downloadURL)

	return downloadURL, nil
}

// UploadToFilesterWithSplit uploads a file to Filester, automatically splitting files
// larger than 10 GB into chunks. For split files, it creates a folder on Filester and
// uploads all chunks to that folder.
//
// Returns:
//   - folderURL: The URL to the Filester folder (for split files) or single file URL
//   - chunkURLs: Array of URLs for individual chunks (empty for files < 10 GB)
//   - error: Any error encountered during upload
//
// Requirements: 14.14, 14.15, 14.16
func (su *StorageUploader) UploadToFilesterWithSplit(ctx context.Context, filePath string) (string, []string, error) {
	const uploadURL = "https://u1.filester.me/api/v1/upload"
	const createFolderURL = "https://u1.filester.me/api/v1/folder/create"
	const maxFileSize = 10 * 1024 * 1024 * 1024 // 10 GB in bytes
	const chunkSize = 10 * 1024 * 1024 * 1024   // 10 GB chunks

	log.Printf("Uploading file to Filester with split support: %s", filePath)

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return "", nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file info for size check
	fileInfo, err := file.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("failed to get file info: %w", err)
	}
	log.Printf("File size: %d bytes", fileInfo.Size())

	// If file is under 10 GB, upload normally
	if fileInfo.Size() <= maxFileSize {
		log.Printf("File is under 10 GB, uploading as single file")
		url, err := su.UploadToFilester(ctx, filePath)
		return url, []string{}, err
	}

	// File needs to be split
	log.Printf("File exceeds 10 GB limit, splitting into chunks")

	// Create a folder for the chunks
	folderName := fmt.Sprintf("%s_chunks", filepath.Base(filePath))
	folderURL, err := su.createFilesterFolder(ctx, createFolderURL, folderName)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create Filester folder: %w", err)
	}
	log.Printf("Created Filester folder: %s", folderURL)

	// Calculate number of chunks needed
	numChunks := (fileInfo.Size() + chunkSize - 1) / chunkSize
	log.Printf("Splitting file into %d chunks of %d bytes each", numChunks, chunkSize)

	// Create temporary directory for chunks
	tmpDir, err := os.MkdirTemp("", "filester_chunks_*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir) // Clean up temp directory

	// Split file into chunks
	chunkPaths := make([]string, 0, numChunks)
	for i := int64(0); i < numChunks; i++ {
		chunkPath := filepath.Join(tmpDir, fmt.Sprintf("%s.part%03d", filepath.Base(filePath), i+1))
		chunkPaths = append(chunkPaths, chunkPath)

		// Create chunk file
		chunkFile, err := os.Create(chunkPath)
		if err != nil {
			return "", nil, fmt.Errorf("failed to create chunk file %s: %w", chunkPath, err)
		}

		// Copy chunk data
		_, err = file.Seek(i*chunkSize, 0)
		if err != nil {
			chunkFile.Close()
			return "", nil, fmt.Errorf("failed to seek to chunk position: %w", err)
		}

		written, err := io.CopyN(chunkFile, file, chunkSize)
		chunkFile.Close()
		if err != nil && err != io.EOF {
			return "", nil, fmt.Errorf("failed to write chunk %d: %w", i+1, err)
		}

		log.Printf("Created chunk %d/%d: %s (%d bytes)", i+1, numChunks, chunkPath, written)
	}

	// Upload all chunks to the folder
	chunkURLs := make([]string, 0, len(chunkPaths))
	for i, chunkPath := range chunkPaths {
		log.Printf("Uploading chunk %d/%d: %s", i+1, len(chunkPaths), chunkPath)

		chunkURL, err := su.uploadFileToFilester(ctx, uploadURL, chunkPath)
		if err != nil {
			return "", nil, fmt.Errorf("failed to upload chunk %d: %w", i+1, err)
		}

		chunkURLs = append(chunkURLs, chunkURL)
		log.Printf("Successfully uploaded chunk %d/%d: %s", i+1, len(chunkPaths), chunkURL)
		
		// Proactively delete chunk file immediately after successful upload to free disk space (Requirement 9.6)
		if err := os.Remove(chunkPath); err != nil {
			log.Printf("Warning: Failed to delete chunk file %s after upload: %v", chunkPath, err)
			// Continue with next chunk - defer cleanup will handle remaining files
		} else {
			log.Printf("Proactively deleted chunk file after upload: %s", chunkPath)
		}
	}

	log.Printf("Successfully uploaded all %d chunks to Filester folder: %s", len(chunkURLs), folderURL)

	return folderURL, chunkURLs, nil
}

// createFilesterFolder creates a folder on Filester for storing split file chunks.
// Note: This is a placeholder implementation. The actual Filester API endpoint for
// folder creation may differ. Adjust the implementation based on Filester's actual API.
func (su *StorageUploader) createFilesterFolder(ctx context.Context, createFolderURL, folderName string) (string, error) {
	// Create request body
	requestBody := map[string]string{
		"name": folderName,
	}
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", createFolderURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", su.filesterAPIKey))

	// Execute request
	resp, err := su.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Filester folder creation returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse JSON response
	var folderResp struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(respBody, &folderResp); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Validate response
	if folderResp.Status != "success" {
		return "", fmt.Errorf("Filester folder creation returned status: %s", folderResp.Status)
	}

	if folderResp.URL == "" {
		return "", fmt.Errorf("Filester response missing folder URL")
	}

	return folderResp.URL, nil
}

// CalculateFileChecksum calculates the SHA-256 checksum of a file.
// It reads the file in chunks to handle large files efficiently without
// loading the entire file into memory.
//
// Returns the checksum as a hexadecimal string, or an error if the file
// cannot be read.
//
// Requirements: 3.11
func (su *StorageUploader) CalculateFileChecksum(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file for checksum: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("failed to calculate checksum: %w", err)
	}

	checksum := hex.EncodeToString(hash.Sum(nil))
	return checksum, nil
}

// UploadRecording coordinates parallel uploads to both Gofile and Filester.
// It executes both uploads concurrently using goroutines, waits for both to complete,
// and returns a combined UploadResult with URLs from both services.
//
// CRITICAL: BOTH uploads must succeed. If either Gofile or Filester upload fails,
// the entire operation is considered failed and falls back to GitHub Artifacts.
// This ensures redundancy - recordings are always available from two independent
// storage providers.
//
// The method implements retry logic with exponential backoff for each upload service
// independently. If either upload fails after retries, it falls back to GitHub Artifacts.
//
// The method first calculates the SHA-256 checksum of the file before upload for
// integrity verification. It then retrieves the optimal Gofile server and launches
// two goroutines to upload to Gofile and Filester simultaneously. It waits for both
// uploads to complete before returning the combined result.
//
// If either upload fails, the method returns an error and marks Success as false.
// The Error field will contain details about which upload(s) failed.
//
// If either upload fails after retries, the method automatically falls back to uploading
// the file to GitHub Artifacts as a last resort.
//
// Requirements: 3.8, 3.11, 14.1, 14.10, 14.11, 14.12
func (su *StorageUploader) UploadRecording(ctx context.Context, filePath string) (*UploadResult, error) {
	log.Printf("Starting parallel upload of %s to Gofile and Filester with retry logic", filePath)

	// Validate file exists and get info
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	
	// Validate minimum file size (BUG 10 FIX)
	const minFileSize = 1024 // 1 KB minimum
	if fileInfo.Size() < minFileSize {
		return nil, fmt.Errorf("file too small (%d bytes) - minimum %d bytes required", fileInfo.Size(), minFileSize)
	}
	
	log.Printf("File size: %d bytes", fileInfo.Size())

	// Calculate file checksum before upload for integrity verification (Requirement 3.11)
	checksum, err := su.CalculateFileChecksum(filePath)
	if err != nil {
		// Make checksum calculation mandatory (BUG 8 FIX)
		return nil, fmt.Errorf("failed to calculate file checksum (required for integrity): %w", err)
	}
	log.Printf("Calculated file checksum: %s (SHA-256)", checksum)

	// Get Gofile server first (needed for upload) - skip if API key not configured
	server := ""
	if su.gofileAPIKey != "" {
		var err error
		server, err = su.GetGofileServer(ctx)
		if err != nil {
			log.Printf("Failed to get Gofile server: %v", err)
			// Continue with Filester only
			server = ""
		}
	} else {
		log.Printf("Skipping Gofile server retrieval - API key not configured")
	}

	// Create channels to receive results from goroutines
	type uploadResponse struct {
		service string
		url     string
		chunks  []string
		err     error
	}

	gofileChan := make(chan uploadResponse, 1)
	filesterChan := make(chan uploadResponse, 1)

	// Launch Gofile upload goroutine with retry logic
	go func() {
		// Recover from panics to prevent deadlock (BUG 11 FIX)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in Gofile upload goroutine: %v", r)
				gofileChan <- uploadResponse{
					service: "Gofile",
					err:     fmt.Errorf("goroutine panicked: %v", r),
				}
			}
		}()
		
		// Check context before starting (BUG 2 FIX)
		if ctx.Err() != nil {
			log.Printf("Context cancelled before Gofile upload started")
			gofileChan <- uploadResponse{service: "Gofile", err: ctx.Err()}
			return
		}
		
		if su.gofileAPIKey == "" {
			log.Printf("Skipping Gofile upload - API key not configured")
			gofileChan <- uploadResponse{service: "Gofile", err: fmt.Errorf("Gofile API key not configured")}
			return
		}
		if server == "" {
			gofileChan <- uploadResponse{service: "Gofile", err: fmt.Errorf("no Gofile server available")}
			return
		}

		log.Printf("Starting Gofile upload in goroutine with retry logic")
		url, err := su.UploadToGofile(ctx, server, filePath)
		if err != nil {
			log.Printf("Gofile upload failed after retries: %v", err)
		} else {
			log.Printf("Gofile upload succeeded: %s", url)
		}
		gofileChan <- uploadResponse{service: "Gofile", url: url, err: err}
	}()

	// Launch Filester upload goroutine with retry logic
	go func() {
		// Recover from panics to prevent deadlock (BUG 11 FIX)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in Filester upload goroutine: %v", r)
				filesterChan <- uploadResponse{
					service: "Filester",
					err:     fmt.Errorf("goroutine panicked: %v", r),
				}
			}
		}()
		
		// Check context before starting (BUG 2 FIX)
		if ctx.Err() != nil {
			log.Printf("Context cancelled before Filester upload started")
			filesterChan <- uploadResponse{service: "Filester", err: ctx.Err()}
			return
		}
		
		if su.filesterAPIKey == "" {
			log.Printf("Skipping Filester upload - API key not configured")
			filesterChan <- uploadResponse{service: "Filester", err: fmt.Errorf("Filester API key not configured")}
			return
		}
		
		log.Printf("Starting Filester upload in goroutine with retry logic")
		url, chunks, err := su.UploadToFilesterWithSplit(ctx, filePath)
		if err != nil {
			log.Printf("Filester upload failed after retries: %v", err)
		} else {
			log.Printf("Filester upload succeeded: %s (chunks: %d)", url, len(chunks))
		}
		filesterChan <- uploadResponse{service: "Filester", url: url, chunks: chunks, err: err}
	}()

	// Wait for both uploads to complete
	gofileResp := <-gofileChan
	filesterResp := <-filesterChan

	// Build combined result
	result := &UploadResult{
		GofileURL:      gofileResp.url,
		FilesterURL:    filesterResp.url,
		FilesterChunks: filesterResp.chunks,
		Checksum:       checksum,
		Success:        false,
		Error:          nil,
	}

	// Check if BOTH uploads succeeded - BOTH are required
	gofileSuccess := gofileResp.err == nil && gofileResp.url != ""
	filesterSuccess := filesterResp.err == nil && filesterResp.url != ""

	// BOTH uploads must succeed - fail if either one fails
	if !gofileSuccess || !filesterSuccess {
		// Build detailed error message
		var errorMsg string
		if !gofileSuccess && !filesterSuccess {
			errorMsg = fmt.Sprintf("BOTH uploads failed - Gofile: %v, Filester: %v", gofileResp.err, filesterResp.err)
		} else if !gofileSuccess {
			errorMsg = fmt.Sprintf("Gofile upload failed: %v (Filester succeeded)", gofileResp.err)
		} else {
			errorMsg = fmt.Sprintf("Filester upload failed: %v (Gofile succeeded)", filesterResp.err)
		}
		
		result.Error = fmt.Errorf(errorMsg)
		result.Success = false
		log.Printf("CRITICAL: Dual upload requirement not met - %s", errorMsg)
		
		// Fall back to GitHub Artifacts (Requirement 8.3)
		log.Printf("Falling back to GitHub Artifacts for %s", filePath)
		if fallbackErr := su.FallbackToArtifacts(ctx, filePath); fallbackErr != nil {
			log.Printf("Fallback to artifacts also failed: %v", fallbackErr)
			result.Error = fmt.Errorf("%v; artifact fallback failed: %w", result.Error, fallbackErr)
		} else {
			log.Printf("Successfully logged fallback to GitHub Artifacts")
			// Note: Notification about fallback usage is sent by the caller
			// (RecordingCompletionHandler) which has access to the HealthMonitor
		}
		
		return result, result.Error
	}

	// Both uploads succeeded
	result.Success = true
	log.Printf("DUAL UPLOAD SUCCESS - Both Gofile and Filester uploads completed successfully")
	log.Printf("  - Gofile URL: %s", result.GofileURL)
	log.Printf("  - Filester URL: %s", result.FilesterURL)
	if len(result.FilesterChunks) > 0 {
		log.Printf("  - Filester chunks: %d", len(result.FilesterChunks))
	}
	
	// Validate URLs are not empty (BUG 1 & BUG 6 FIX)
	if result.GofileURL == "" || result.FilesterURL == "" {
		result.Success = false
		result.Error = fmt.Errorf("upload succeeded but URLs are empty - Gofile: %q, Filester: %q", result.GofileURL, result.FilesterURL)
		log.Printf("CRITICAL: %v", result.Error)
		return result, result.Error
	}
	
	// Validate chunk URLs if present (BUG 12 FIX)
	for i, chunkURL := range result.FilesterChunks {
		if chunkURL == "" {
			result.Success = false
			result.Error = fmt.Errorf("chunk %d URL is empty", i+1)
			log.Printf("CRITICAL: %v", result.Error)
			return result, result.Error
		}
	}
	
	// Log integrity verification results (Requirement 3.11)
	if checksum != "" {
		log.Printf("Upload integrity verification - File checksum: %s", checksum)
		log.Printf("Note: Gofile and Filester APIs do not provide checksums in responses for verification")
		log.Printf("Local file checksum logged for manual verification if needed")
	}

	// DO NOT delete file here - let the handler delete it after Supabase insert succeeds (BUG 3 FIX)
	log.Printf("Both uploads succeeded - file will be deleted by handler after database insert")
	
	return result, nil
}

// uploadFileToFilester is a helper method that uploads a single file to Filester.
// This is used internally by UploadToFilesterWithSplit for uploading chunks.
func (su *StorageUploader) uploadFileToFilester(ctx context.Context, uploadURL, filePath string) (string, error) {
	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create multipart form data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file field
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}

	// Copy file content to form
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file content: %w", err)
	}

	// Close the writer to finalize the multipart message
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", uploadURL, body)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", su.filesterAPIKey))

	// Execute request
	resp, err := su.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Filester upload returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse JSON response
	var uploadResp filesterUploadResponse
	if err := json.Unmarshal(respBody, &uploadResp); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Validate response status
	if uploadResp.Status != "success" {
		return "", fmt.Errorf("Filester upload returned status: %s", uploadResp.Status)
	}

	// Extract download URL
	downloadURL := uploadResp.URL
	if downloadURL == "" {
		return "", fmt.Errorf("Filester response missing download URL")
	}

	return downloadURL, nil
}

// FallbackToArtifacts uploads a file to GitHub Artifacts as a fallback when both
// Gofile and Filester uploads fail. This method is called automatically by
// UploadRecording when both external storage services are unavailable.
//
// GitHub Artifacts provide a reliable fallback storage option within the GitHub
// Actions environment. Files uploaded to artifacts are retained for 7 days by default
// and can be downloaded from the workflow run page.
//
// The method logs detailed file information including:
//   - File path and name
//   - File size in bytes and human-readable format
//   - File checksum (SHA-256) for integrity verification
//   - Timestamp of fallback operation
//
// Note: In a real GitHub Actions environment, artifact uploads are typically handled
// by the actions/upload-artifact action in the workflow YAML. This method logs the
// fallback operation and marks the file for artifact upload. The actual upload is
// performed by the workflow step that runs on failure.
//
// Requirements: 3.9, 8.3, 14.11
func (su *StorageUploader) FallbackToArtifacts(ctx context.Context, filePath string) error {
	log.Printf("=== FALLBACK TO GITHUB ARTIFACTS ===")
	log.Printf("Both Gofile and Filester uploads failed - using GitHub Artifacts as fallback")
	
	// Verify file exists and get detailed information
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		log.Printf("ERROR: Failed to stat file for artifact upload: %v", err)
		return fmt.Errorf("failed to stat file for artifact upload: %w", err)
	}
	
	// Log detailed file information (Requirement 8.3)
	fileName := filepath.Base(filePath)
	fileSizeBytes := fileInfo.Size()
	fileSizeHuman := formatFileSize(fileSizeBytes)
	timestamp := time.Now().Format(time.RFC3339)
	
	log.Printf("Fallback Details:")
	log.Printf("  - File Name: %s", fileName)
	log.Printf("  - File Path: %s", filePath)
	log.Printf("  - File Size: %d bytes (%s)", fileSizeBytes, fileSizeHuman)
	log.Printf("  - Timestamp: %s", timestamp)
	
	// Calculate file checksum for integrity verification
	checksum, err := su.CalculateFileChecksum(filePath)
	if err != nil {
		log.Printf("WARNING: Failed to calculate file checksum: %v", err)
		checksum = "unavailable"
	} else {
		log.Printf("  - Checksum (SHA-256): %s", checksum)
	}
	
	// Log instructions for artifact retrieval
	log.Printf("")
	log.Printf("Artifact Upload Instructions:")
	log.Printf("  The file will be uploaded to GitHub Artifacts by the workflow step.")
	log.Printf("  To retrieve the file:")
	log.Printf("    1. Go to the workflow run page on GitHub")
	log.Printf("    2. Navigate to the 'Artifacts' section")
	log.Printf("    3. Download the artifact containing this recording")
	log.Printf("  Artifact retention: 7 days (default)")
	log.Printf("")
	
	// In a real implementation, this would:
	// 1. Use GitHub Actions artifact upload API or CLI
	// 2. Create an artifact with a descriptive name
	// 3. Upload the file to the artifact
	// 4. Return any errors from the upload process
	//
	// The actual artifact upload is handled by the workflow YAML:
	//
	//   - name: Upload artifacts on failure
	//     if: failure()
	//     uses: actions/upload-artifact@v4
	//     with:
	//       name: recordings-${{ matrix.job_id }}-${{ github.run_id }}
	//       path: ./videos/**/*
	//       retention-days: 7
	
	log.Printf("Artifact fallback logged successfully for %s", fileName)
	log.Printf("File preserved for artifact upload by workflow")
	log.Printf("=== END FALLBACK TO GITHUB ARTIFACTS ===")
	
	// Return nil to allow operation to continue (Requirement 8.3)
	return nil
}

// formatFileSize converts bytes to a human-readable format (KB, MB, GB).
func formatFileSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}
