package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/HeapOfChaos/goondvr/server"
)

// RefreshCookiesWithFlareSolverr uses FlareSolverr to get fresh cookies from Chaturbate
// This is needed in GitHub Actions because cookies from your local browser won't work
// with GitHub Actions' IP address (Cloudflare ties cookies to IP)
func RefreshCookiesWithFlareSolverr(ctx context.Context) error {
	if !IsFlareSolverrEnabled() {
		return nil // FlareSolverr not enabled, skip
	}

	log.Println("🔄 Refreshing Cloudflare cookies using FlareSolverr...")
	log.Println("   This is needed because GitHub Actions has a different IP than your browser")

	flare := NewFlareSolverrClient()

	// Visit Chaturbate homepage to get fresh cf_clearance cookie
	chaturbateURL := strings.TrimSuffix(server.Config.Domain, "/")
	
	// Prepare headers - CRITICAL: Add X-Requested-With to bypass age gate
	// This is the key insight from https://gist.github.com/you-cant-see-me/811ab5f9461b7aa0d69f59db7eed98ec
	headers := make(map[string]string)
	headers["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"
	headers["Accept-Language"] = "en-US,en;q=0.5"
	headers["X-Requested-With"] = "XMLHttpRequest" // CRITICAL: Bypass age gate

	var cookies map[string]string
	var userAgent string
	var err error
	
	// Retry up to 3 times if FlareSolverr fails
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			log.Printf("   Retry attempt %d/%d...", attempt, maxRetries)
		}
		
		// Step 1: Visit homepage to get initial cookies and solve Cloudflare
		log.Printf("   Step 1: Visiting %s through FlareSolverr...", chaturbateURL)
		_, cookies, userAgent, err = flare.GetWithCookiesAndUA(ctx, chaturbateURL, nil, headers)
		if err != nil {
			if attempt < maxRetries {
				log.Printf("   ⚠️  Attempt %d failed: %v", attempt, err)
				continue
			}
			return fmt.Errorf("flaresolverr homepage request failed after %d attempts: %w", maxRetries, err)
		}
		
		// Success, break out of retry loop
		break
	}

	// Step 2: Visit a public room page to establish a proper session AND accept age gate
	// This ensures we get all necessary session cookies including age verification
	// Using a well-known always-online room to ensure success
	testRoomURL := chaturbateURL + "/siswet19/"  // Popular room, usually online
	log.Printf("   Step 2: Visiting room to accept age gate and establish session...")
	_, cookies2, _, err := flare.GetWithCookiesAndUA(ctx, testRoomURL, cookies, headers)
	if err != nil {
		// Try alternative room if first fails
		log.Printf("   Warning: First room visit failed, trying alternative...")
		testRoomURL = chaturbateURL + "/tessa_swan/"
		_, cookies2, _, err = flare.GetWithCookiesAndUA(ctx, testRoomURL, cookies, headers)
		if err != nil {
			// Room visit might fail, but we can continue with cookies from step 1
			log.Printf("   Warning: Room visits failed (continuing): %v", err)
		} else {
			// Merge cookies from both requests
			for name, value := range cookies2 {
				cookies[name] = value
			}
			log.Println("   ✅ Session established with age verification")
		}
	} else {
		// Merge cookies from both requests
		for name, value := range cookies2 {
			cookies[name] = value
		}
		log.Println("   ✅ Session established with age verification")
	}

	// Extract cf_clearance cookie
	cfClearance := ""
	for name, value := range cookies {
		if name == "cf_clearance" {
			cfClearance = value
			break
		}
	}

	if cfClearance == "" {
		return fmt.Errorf("no cf_clearance cookie received from FlareSolverr")
	}

	// Build complete cookie string with ALL cookies from FlareSolverr
	// Chaturbate needs more than just cf_clearance to access HLS sources
	// IMPORTANT: Preserve cookie order and ensure proper formatting
	var cookiePairs []string
	
	// Add cf_clearance first (most important)
	cookiePairs = append(cookiePairs, fmt.Sprintf("cf_clearance=%s", cfClearance))
	
	// Add all other cookies from FlareSolverr in a consistent order
	// Sort cookie names for consistency
	var cookieNames []string
	for name := range cookies {
		if name != "cf_clearance" {
			cookieNames = append(cookieNames, name)
		}
	}
	sort.Strings(cookieNames)
	
	// Add cookies in sorted order
	for _, name := range cookieNames {
		cookiePairs = append(cookiePairs, fmt.Sprintf("%s=%s", name, cookies[name]))
	}
	
	// CRITICAL: Update BOTH cookies AND User-Agent
	// Cloudflare ties the cookies to the User-Agent that was used to get them
	cookieString := strings.Join(cookiePairs, "; ")
	server.Config.Cookies = cookieString
	server.Config.UserAgent = userAgent
	
	if server.Config.Debug {
		log.Printf("   Final cookie string length: %d characters", len(cookieString))
		log.Printf("   Cookie preview: %s...", cookieString[:min(200, len(cookieString))])
	}
	
	log.Println("✅ Successfully refreshed Cloudflare cookies!")
	log.Printf("   New cf_clearance: %s...", cfClearance[:min(50, len(cfClearance))])
	log.Printf("   Total cookies received: %d", len(cookies))
	log.Printf("   Cookie names: %v", getCookieNames(cookies))
	log.Printf("   User-Agent: %s...", userAgent[:min(80, len(userAgent))])
	log.Println("   These cookies are valid for this GitHub Actions runner's IP")

	return nil
}

// GetWithCookies makes a request and returns both response and cookies
func (f *FlareSolverrClient) GetWithCookies(ctx context.Context, url string, cookies map[string]string, headers map[string]string) (string, map[string]string, error) {
	response, cookiesMap, _, err := f.GetWithCookiesAndUA(ctx, url, cookies, headers)
	return response, cookiesMap, err
}

// GetWithCookiesAndUA makes a request and returns response, cookies, and User-Agent
func (f *FlareSolverrClient) GetWithCookiesAndUA(ctx context.Context, url string, cookies map[string]string, headers map[string]string) (string, map[string]string, string, error) {
	// Convert cookies to FlareSolverr format
	var flareCookies []FlareCookie
	for name, value := range cookies {
		flareCookies = append(flareCookies, FlareCookie{
			Name:  name,
			Value: value,
		})
	}

	reqData := FlareSolverrRequest{
		Cmd:        "request.get",
		URL:        url,
		MaxTimeout: 180000, // 180 seconds (3 minutes) - increased for difficult challenges
		Cookies:    flareCookies,
		Headers:    headers,
	}

	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return "", nil, "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", f.baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", nil, "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, "", fmt.Errorf("read response: %w", err)
	}

	var flareResp FlareSolverrResponse
	if err := json.Unmarshal(body, &flareResp); err != nil {
		return "", nil, "", fmt.Errorf("unmarshal response: %w", err)
	}

	if flareResp.Status != "ok" {
		return "", nil, "", fmt.Errorf("flaresolverr error: %s", flareResp.Message)
	}

	// Extract cookies from response
	resultCookies := make(map[string]string)
	for _, cookie := range flareResp.Solution.Cookies {
		resultCookies[cookie.Name] = cookie.Value
	}

	// Extract User-Agent from response
	userAgent := flareResp.Solution.UserAgent

	return flareResp.Solution.Response, resultCookies, userAgent, nil
}

// PostWithCookiesAndUA makes a POST request and returns response, cookies, and User-Agent
func (f *FlareSolverrClient) PostWithCookiesAndUA(ctx context.Context, url string, postData string, cookies map[string]string, headers map[string]string) (string, map[string]string, string, error) {
	// Convert cookies to FlareSolverr format
	var flareCookies []FlareCookie
	for name, value := range cookies {
		flareCookies = append(flareCookies, FlareCookie{
			Name:  name,
			Value: value,
		})
	}

	reqData := FlareSolverrRequest{
		Cmd:        "request.post",
		URL:        url,
		MaxTimeout: 180000, // 180 seconds (3 minutes) - increased for difficult challenges
		Cookies:    flareCookies,
		Headers:    headers,
		PostData:   postData,
	}

	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return "", nil, "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", f.baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", nil, "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, "", fmt.Errorf("read response: %w", err)
	}

	var flareResp FlareSolverrResponse
	if err := json.Unmarshal(body, &flareResp); err != nil {
		return "", nil, "", fmt.Errorf("unmarshal response: %w", err)
	}

	if flareResp.Status != "ok" {
		return "", nil, "", fmt.Errorf("flaresolverr error: %s", flareResp.Message)
	}

	// Extract cookies from response
	resultCookies := make(map[string]string)
	for _, cookie := range flareResp.Solution.Cookies {
		resultCookies[cookie.Name] = cookie.Value
	}

	// Extract User-Agent from response
	userAgent := flareResp.Solution.UserAgent

	return flareResp.Solution.Response, resultCookies, userAgent, nil
}

// ShouldRefreshCookies checks if cookies need to be refreshed
// In GitHub Actions, we should refresh cookies on startup
func ShouldRefreshCookies() bool {
	// Only refresh in GitHub Actions with FlareSolverr enabled
	if !IsFlareSolverrEnabled() {
		return false
	}

	// Check if we're in GitHub Actions
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return false
	}

	// Always refresh on startup in GitHub Actions
	return true
}

// getCookieNames returns a list of cookie names for logging
func getCookieNames(cookies map[string]string) []string {
	names := make([]string, 0, len(cookies))
	for name := range cookies {
		names = append(names, name)
	}
	return names
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
