package internal

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	cycletls "github.com/Danny-Dasilva/CycleTLS/cycletls"
	"github.com/HeapOfChaos/goondvr/server"
)

// Global shared CycleTLS client for thread-safe concurrent requests
var (
	globalCycleTLS     cycletls.CycleTLS
	cycleTLSMu         sync.Mutex // Protects globalCycleTLS from concurrent access
	cycleTLSOnce       sync.Once
	cycleTLSInitialized bool
)

// initGlobalCycleTLS initializes the global CycleTLS client once
func initGlobalCycleTLS() {
	cycleTLSOnce.Do(func() {
		if os.Getenv("USE_FLARESOLVERR") == "true" {
			globalCycleTLS = cycletls.Init()
			cycleTLSInitialized = true
			fmt.Println("[INFO] Initialized shared CycleTLS client for concurrent requests")
		}
	})
}

// Req represents an HTTP client with customized settings.
type Req struct {
	client     *http.Client
	useCycle   bool              // when true, use CycleTLS instead of standard http.Client
	isMedia    bool              // when true, omits browser-spoofing headers not needed for CDN media requests
	referer    string            // CDN Referer/Origin override; only used when isMedia is true
}

// NewReq creates a new HTTP client for Chaturbate page requests.
func NewReq() *Req {
	// Check if we should use CycleTLS (GitHub Actions mode with FlareSolverr)
	useCycleTLS := os.Getenv("USE_FLARESOLVERR") == "true"
	
	// Initialize global CycleTLS client if needed
	if useCycleTLS {
		initGlobalCycleTLS()
	}
	
	req := &Req{
		client: &http.Client{
			Transport: CreateTransport(),
		},
		useCycle: useCycleTLS,
	}
	
	return req
}

// NewMediaReq creates a new HTTP client for CDN media requests (playlists, segments).
// It omits headers like X-Requested-With that are only needed for Chaturbate page fetches.
func NewMediaReq() *Req {
	// Check if we should use CycleTLS (GitHub Actions mode with FlareSolverr)
	useCycleTLS := os.Getenv("USE_FLARESOLVERR") == "true"
	
	// Initialize global CycleTLS client if needed
	if useCycleTLS {
		initGlobalCycleTLS()
	}
	
	req := &Req{
		client: &http.Client{
			Transport: CreateTransport(),
		},
		isMedia:  true,
		useCycle: useCycleTLS,
	}
	
	return req
}

// NewMediaReqWithReferer creates a media HTTP client that sends the given URL as
// Referer and Origin instead of the Chaturbate defaults. Use this for non-Chaturbate CDNs.
func NewMediaReqWithReferer(referer string) *Req {
	// Check if we should use CycleTLS (GitHub Actions mode with FlareSolverr)
	useCycleTLS := os.Getenv("USE_FLARESOLVERR") == "true"
	
	// Initialize global CycleTLS client if needed
	if useCycleTLS {
		initGlobalCycleTLS()
	}
	
	req := &Req{
		client: &http.Client{
			Transport: CreateTransport(),
		},
		isMedia:  true,
		referer:  referer,
		useCycle: useCycleTLS,
	}
	
	return req
}

// CreateTransport initializes a custom HTTP transport.
func CreateTransport() *http.Transport {
	// The DefaultTransport allows user changes the proxy settings via environment variables
	// such as HTTP_PROXY, HTTPS_PROXY.
	defaultTransport := http.DefaultTransport.(*http.Transport)

	newTransport := defaultTransport.Clone()
	newTransport.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}
	return newTransport
}

// Post sends an HTTP POST request with form data and returns the response as a string.
func (h *Req) Post(ctx context.Context, url string, data string) (string, error) {
	// Use CycleTLS if enabled (GitHub Actions mode)
	if h.useCycle {
		return h.PostWithCycleTLS(ctx, url, data)
	}
	
	// Standard HTTP client path
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	
	// Set content type for form data
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.SetRequestHeaders(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("client do: %w", err)
	}
	defer resp.Body.Close()

	if server.Config.Debug && resp.StatusCode >= 400 {
		fmt.Printf("[DEBUG] HTTP %d: %s\n", resp.StatusCode, req.URL)
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	return string(b), nil
}

// PostWithReferer sends an HTTP POST request with form data and a custom Referer header.
// This is needed for endpoints like /get_edge_hls_url_ajax/ which require a room-specific Referer.
func (h *Req) PostWithReferer(ctx context.Context, url string, data string, referer string) (string, error) {
	// Use CycleTLS if enabled (GitHub Actions mode)
	if h.useCycle {
		return h.postWithCycleTLSReferer(ctx, url, data, referer)
	}
	
	// Standard HTTP client path
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	
	// Set content type for form data
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.SetRequestHeaders(req)
	// Override Referer with room-specific value
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("client do: %w", err)
	}
	defer resp.Body.Close()

	if server.Config.Debug && resp.StatusCode >= 400 {
		fmt.Printf("[DEBUG] HTTP %d: %s\n", resp.StatusCode, req.URL)
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	return string(b), nil
}

// PostWithCycleTLS sends an HTTP POST request using CycleTLS.
func (h *Req) PostWithCycleTLS(ctx context.Context, url string, data string) (string, error) {
	return h.postWithCycleTLSReferer(ctx, url, data, "")
}

// postWithCycleTLSReferer sends an HTTP POST request using CycleTLS with an optional custom Referer.
func (h *Req) postWithCycleTLSReferer(ctx context.Context, url string, data string, referer string) (string, error) {
	// Build headers map
	headers := make(map[string]string)
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	headers["X-Requested-With"] = "XMLHttpRequest"
	if referer != "" {
		headers["Referer"] = referer
	} else {
		headers["Referer"] = "https://chaturbate.com/"
	}
	
	if server.Config.UserAgent != "" {
		headers["User-Agent"] = server.Config.UserAgent
	}
	
	if server.Config.Cookies != "" {
		cookieStr := strings.TrimSpace(server.Config.Cookies)
		headers["Cookie"] = cookieStr
		
		cookies := ParseCookies(cookieStr)
		if csrfToken, ok := cookies["csrftoken"]; ok {
			headers["X-CSRFToken"] = csrfToken
		}
	}
	
	if server.Config.Debug {
		fmt.Printf("[DEBUG] CycleTLS POST URL: %s\n", url)
		fmt.Printf("[DEBUG] CycleTLS POST data: %s\n", data)
	}
	
	// Uses the global shared CycleTLS instance with mutex protection
	cycleTLSMu.Lock()
	response, err := globalCycleTLS.Do(url, cycletls.Options{
		Body:      data,
		Ja3:       "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-27-17513,29-23-24,0",
		UserAgent: server.Config.UserAgent,
		Headers:   headers,
		Timeout:   10,
	}, "POST")
	cycleTLSMu.Unlock()
	
	if err != nil {
		return "", fmt.Errorf("cycletls post: %w", err)
	}
	
	if response.Status >= 400 {
		fmt.Printf("[DEBUG] HTTP %d: %s\n", response.Status, url)
	}
	
	return response.Body, nil
}

// Get sends an HTTP GET request and returns the response as a string.
func (h *Req) Get(ctx context.Context, url string) (string, error) {
	// FlareSolverr is NOT used for API endpoints
	// It's only useful for HTML pages with Cloudflare challenges
	// API endpoints like /api/chatvideocontext/ don't have Cloudflare challenges
	// They just check cookies and IP reputation
	
	// Original implementation (works fine with valid cookies)
	resp, err := h.GetBytes(ctx, url)
	if err != nil {
		return "", fmt.Errorf("get bytes: %w", err)
	}
	return string(resp), nil
}

// PostJSON sends an HTTP POST request with JSON body and returns the response as a string.
func (h *Req) PostJSON(ctx context.Context, url string, jsonBody string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	
	h.SetRequestHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	
	return string(body), nil
}

// GetBytes sends an HTTP GET request and returns the response as a byte slice.
func (h *Req) GetBytes(ctx context.Context, url string) ([]byte, error) {
	// Use CycleTLS if enabled (GitHub Actions mode)
	if h.useCycle {
		return h.GetBytesWithCycleTLS(ctx, url)
	}
	
	// Standard HTTP client path
	req, cancel, err := h.CreateRequest(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	defer cancel()

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("client do: %w", err)
	}
	defer resp.Body.Close()

	if server.Config.Debug && resp.StatusCode >= 400 {
		fmt.Printf("[DEBUG] HTTP %d: %s\n", resp.StatusCode, req.URL)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Check for Cloudflare protection - multiple indicators
	if resp.StatusCode == 403 || resp.StatusCode == 503 {
		bodyStr := string(b)
		if strings.Contains(bodyStr, "<title>Just a moment...</title>") ||
			strings.Contains(bodyStr, "Just a moment") ||
			strings.Contains(bodyStr, "cloudflare") ||
			resp.Header.Get("Server") == "cloudflare" {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] CF response for %s (status %d)\n", req.URL, resp.StatusCode)
				tmpFile, ferr := os.CreateTemp("", "chaturbate-debug-cf-*.html")
				if ferr == nil {
					if _, werr := tmpFile.Write(b); werr == nil {
						fmt.Printf("[DEBUG]   Full body written to: %s\n", tmpFile.Name())
					}
					tmpFile.Close()
				}
			}
			return nil, ErrCloudflareBlocked
		}
	}
	// Check for Age Verification
	if strings.Contains(string(b), "Verify your age") {
		return nil, ErrAgeVerification
	}

	// For 403 responses, check if it's a known benign response
	if resp.StatusCode == http.StatusForbidden {
		bodyPreview := string(b)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500] + "..."
		}
		
		// "session_duplicated" is a benign Chaturbate API response, not an error
		if strings.Contains(string(b), "session_duplicated") {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] HTTP 403 (session_duplicated) - This is normal, continuing...\n")
			}
			// Return the body for parsing - it's not actually an error
			return b, err
		}
		
		// Log other 403 responses for debugging
		if server.Config.Debug {
			fmt.Printf("[DEBUG] HTTP 403 for %s - Response body: %s\n", req.URL, bodyPreview)
		}
		
		// Only return ErrPrivateStream if we're sure it's actually private
		if strings.Contains(string(b), "private") || strings.Contains(string(b), "Private") {
			return nil, fmt.Errorf("forbidden: %w", ErrPrivateStream)
		}
		// If it's not about private show, return the body for parsing
	}

	return b, err
}

// CreateRequest constructs an HTTP GET request with necessary headers.
func (h *Req) CreateRequest(ctx context.Context, url string) (*http.Request, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second) // timed out after 10 seconds

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, cancel, err
	}
	h.SetRequestHeaders(req)
	return req, cancel, nil
}

// DoRequest executes an already-constructed *http.Request and returns the
// response body as a string. This allows callers to set extra headers on the
// request before executing it (e.g. site-specific Referer or X-Requested-With).
func (h *Req) DoRequest(req *http.Request) (string, error) {
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("client do: %w", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	// Check for Cloudflare protection
	if strings.Contains(string(b), "<title>Just a moment...</title>") {
		return "", ErrCloudflareBlocked
	}
	// Check for Age Verification
	if strings.Contains(string(b), "Verify your age") {
		return "", ErrAgeVerification
	}

	// For 403 responses, check if it's a known benign response
	if resp.StatusCode == http.StatusForbidden {
		bodyPreview := string(b)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500] + "..."
		}
		
		// "session_duplicated" is a benign Chaturbate API response, not an error
		if strings.Contains(string(b), "session_duplicated") {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] HTTP 403 (session_duplicated) - This is normal, continuing...\n")
			}
			// Return the body for parsing - it's not actually an error
			return string(b), nil
		}
		
		// Log other 403 responses for debugging
		if server.Config.Debug {
			fmt.Printf("[DEBUG] HTTP 403 in DoRequest - Response body: %s\n", bodyPreview)
		}
		
		// Only return ErrPrivateStream if we're sure
		if strings.Contains(string(b), "private") || strings.Contains(string(b), "Private") {
			return "", fmt.Errorf("forbidden: %w", ErrPrivateStream)
		}
		// Otherwise return the body for parsing
	}

	return string(b), nil
}

// SetRequestHeaders applies necessary headers to the request.
func (h *Req) SetRequestHeaders(req *http.Request) {
	// CRITICAL: Always add X-Requested-With header to bypass age gate
	// This is the key to bypassing Chaturbate's age verification
	// Source: https://gist.github.com/you-cant-see-me/811ab5f9461b7aa0d69f59db7eed98ec
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	
	if h.isMedia {
		ref := h.referer
		if ref == "" {
			ref = "https://chaturbate.com/"
		}
		req.Header.Set("Referer", ref)
		req.Header.Set("Origin", strings.TrimRight(ref, "/"))
	} else {
		// For API requests, always set Referer
		req.Header.Set("Referer", "https://chaturbate.com/")
	}
	
	if server.Config.UserAgent != "" {
		req.Header.Set("User-Agent", server.Config.UserAgent)
	}
	if server.Config.Cookies != "" {
		cookies := ParseCookies(server.Config.Cookies)
		
		// Add CSRF token as header if present
		// CRITICAL: Chaturbate requires X-CSRFToken header to match csrftoken cookie
		// Source: https://gist.github.com/mywalkb/1c9a26a59018cf1af40eb2fe0e8dea33
		if csrfToken, ok := cookies["csrftoken"]; ok {
			req.Header.Set("X-CSRFToken", csrfToken)
		}
		
		for name, value := range cookies {
			req.AddCookie(&http.Cookie{Name: name, Value: value})
		}
	}
}

// ParseCookies converts a cookie string into a map.
func ParseCookies(cookieStr string) map[string]string {
	cookies := make(map[string]string)
	pairs := strings.Split(cookieStr, ";")

	// Iterate over each cookie pair and extract key-value pairs
	for _, pair := range pairs {
		parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(parts) == 2 {
			// Trim spaces around key and value
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			
			// Validate cookie format
			if key == "" || value == "" {
				if server.Config.Debug {
					fmt.Printf("[WARN] Invalid cookie format (empty key or value): %s\n", pair)
				}
				continue
			}
			
			// Store cookie name and value in the map
			cookies[key] = value
		} else if server.Config.Debug {
			fmt.Printf("[WARN] Invalid cookie format (missing =): %s\n", pair)
		}
	}
	return cookies
}

// GetBytesWithCycleTLS sends an HTTP GET request using CycleTLS to spoof browser TLS fingerprint.
// This bypasses Cloudflare's TLS fingerprint detection in GitHub Actions.
func (h *Req) GetBytesWithCycleTLS(ctx context.Context, url string) ([]byte, error) {
	// Build headers map
	headers := make(map[string]string)
	
	// CRITICAL: Always add X-Requested-With header to bypass age gate
	// This is the key to bypassing Chaturbate's age verification
	// Source: https://gist.github.com/you-cant-see-me/811ab5f9461b7aa0d69f59db7eed98ec
	headers["X-Requested-With"] = "XMLHttpRequest"
	
	if h.isMedia {
		ref := h.referer
		if ref == "" {
			ref = "https://chaturbate.com/"
		}
		headers["Referer"] = ref
		headers["Origin"] = strings.TrimRight(ref, "/")
	} else {
		// For API requests, always set Referer
		headers["Referer"] = "https://chaturbate.com/"
	}
	
	if server.Config.UserAgent != "" {
		headers["User-Agent"] = server.Config.UserAgent
	}
	
	// Add cookies - ensure they're properly formatted
	if server.Config.Cookies != "" {
		// CycleTLS expects cookies in the Cookie header as a semicolon-separated string
		// Make sure there are no extra spaces or formatting issues
		cookieStr := strings.TrimSpace(server.Config.Cookies)
		headers["Cookie"] = cookieStr
		
		// Extract and add CSRF token as header if present
		// CRITICAL: Chaturbate requires X-CSRFToken header to match csrftoken cookie
		// Source: https://gist.github.com/mywalkb/1c9a26a59018cf1af40eb2fe0e8dea33
		cookies := ParseCookies(cookieStr)
		if csrfToken, ok := cookies["csrftoken"]; ok {
			headers["X-CSRFToken"] = csrfToken
		}
		
		// Log CycleTLS request details only in debug mode
		if server.Config.Debug {
			preview := cookieStr
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			fmt.Printf("[DEBUG] CycleTLS URL: %s\n", url)
			fmt.Printf("[DEBUG] CycleTLS cookies: %s\n", preview)
			fmt.Printf("[DEBUG] CycleTLS X-Requested-With: %s\n", headers["X-Requested-With"])
			if csrfToken, ok := headers["X-CSRFToken"]; ok {
				fmt.Printf("[DEBUG] CycleTLS X-CSRFToken: %s\n", csrfToken[:minInt(20, len(csrfToken))]+"...")
			}
			if referer, ok := headers["Referer"]; ok {
				fmt.Printf("[DEBUG] CycleTLS Referer: %s\n", referer)
			}
			fmt.Printf("[DEBUG] CycleTLS User-Agent: %s\n", server.Config.UserAgent[:minInt(80, len(server.Config.UserAgent))])
		}
	}
	
	// Make request with CycleTLS using Chrome 120 profile
	// This spoofs Chrome's TLS/HTTP2 fingerprint to bypass Cloudflare
	// Uses the global shared CycleTLS instance with mutex protection
	cycleTLSMu.Lock()
	response, err := globalCycleTLS.Do(url, cycletls.Options{
		Body:      "",
		Ja3:       "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-27-17513,29-23-24,0",
		UserAgent: server.Config.UserAgent,
		Headers:   headers,
		Timeout:   10,
	}, "GET")
	cycleTLSMu.Unlock()
	
	if err != nil {
		return nil, fmt.Errorf("cycletls request: %w", err)
	}
	
	if server.Config.Debug && response.Status >= 400 {
		fmt.Printf("[DEBUG] HTTP %d: %s\n", response.Status, url)
	}
	
	if response.Status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	
	body := []byte(response.Body)
	
	// Check for Cloudflare protection - multiple indicators
	if response.Status == 403 || response.Status == 503 {
		bodyStr := response.Body
		if strings.Contains(bodyStr, "<title>Just a moment...</title>") ||
			strings.Contains(bodyStr, "Just a moment") ||
			strings.Contains(bodyStr, "cloudflare") {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] CF response for %s (status %d)\n", url, response.Status)
				tmpFile, ferr := os.CreateTemp("", "chaturbate-debug-cf-*.html")
				if ferr == nil {
					if _, werr := tmpFile.Write(body); werr == nil {
						fmt.Printf("[DEBUG]   Full body written to: %s\n", tmpFile.Name())
					}
					tmpFile.Close()
				}
			}
			return nil, ErrCloudflareBlocked
		}
	}
	
	// Check for Age Verification
	if strings.Contains(response.Body, "Verify your age") {
		return nil, ErrAgeVerification
	}
	
	// For 403 responses with CycleTLS, check if it's a known benign response
	if response.Status == http.StatusForbidden {
		bodyPreview := response.Body
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500] + "..."
		}
		
		// "session_duplicated" is a benign Chaturbate API response, not an error
		// It just means the session is already active - this is normal and expected
		if strings.Contains(response.Body, "session_duplicated") {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] HTTP 403 (session_duplicated) - This is normal, continuing...\n")
			}
			// Return the body for parsing - it's not actually an error
			return body, nil
		}
		
		// Log other 403 responses for debugging
		if server.Config.Debug {
			fmt.Printf("[DEBUG] HTTP 403 in CycleTLS - Response body: %s\n", bodyPreview)
		}
		
		// Only return ErrPrivateStream if we're sure
		if strings.Contains(response.Body, "private") || strings.Contains(response.Body, "Private") {
			return nil, fmt.Errorf("forbidden: %w", ErrPrivateStream)
		}
		// Otherwise return the body for parsing
	}
	
	return body, nil
}

// minInt returns the minimum of two integers
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
