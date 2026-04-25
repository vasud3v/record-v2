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
	
	// Prepare headers - use a standard Chrome User-Agent
	// FlareSolverr will use its own User-Agent from the real Chrome browser
	headers := make(map[string]string)

	// Make request through FlareSolverr (it will solve Cloudflare challenge)
	log.Printf("   Visiting %s through FlareSolverr...", chaturbateURL)
	_, cookies, userAgent, err := flare.GetWithCookiesAndUA(ctx, chaturbateURL, nil, headers)
	if err != nil {
		return fmt.Errorf("flaresolverr request failed: %w", err)
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
	var cookiePairs []string
	for name, value := range cookies {
		cookiePairs = append(cookiePairs, fmt.Sprintf("%s=%s", name, value))
	}
	
	// CRITICAL: Update BOTH cookies AND User-Agent
	// Cloudflare ties the cookies to the User-Agent that was used to get them
	server.Config.Cookies = strings.Join(cookiePairs, "; ")
	server.Config.UserAgent = userAgent
	
	log.Println("✅ Successfully refreshed Cloudflare cookies!")
	log.Printf("   New cf_clearance: %s...", cfClearance[:50])
	log.Printf("   Total cookies received: %d", len(cookies))
	log.Printf("   Cookie names: %v", getCookieNames(cookies))
	log.Printf("   User-Agent: %s...", userAgent[:80])
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
		MaxTimeout: 120000, // 120 seconds (2 minutes) for Cloudflare challenge
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
