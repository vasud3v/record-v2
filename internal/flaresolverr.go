package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// FlareSolverrClient wraps HTTP requests through FlareSolverr proxy
// Note: In GitHub Actions, each matrix job gets its own FlareSolverr service container,
// so there's no need for global request serialization. Each job can make concurrent
// requests to its own dedicated FlareSolverr instance.
type FlareSolverrClient struct {
	baseURL string
	client  *http.Client
}

// FlareSolverrRequest represents a request to FlareSolverr
type FlareSolverrRequest struct {
	Cmd        string            `json:"cmd"`
	URL        string            `json:"url"`
	MaxTimeout int               `json:"maxTimeout"`
	Cookies    []FlareCookie     `json:"cookies,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	PostData   string            `json:"postData,omitempty"`
}

// FlareCookie represents a cookie for FlareSolverr
type FlareCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain,omitempty"`
}

// FlareSolverrResponse represents a response from FlareSolverr
type FlareSolverrResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Solution struct {
		URL      string `json:"url"`
		Status   int    `json:"status"`
		Response string `json:"response"`
		Cookies  []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		} `json:"cookies"`
		UserAgent string `json:"userAgent"`
	} `json:"solution"`
}

// NewFlareSolverrClient creates a new FlareSolverr client
func NewFlareSolverrClient() *FlareSolverrClient {
	baseURL := os.Getenv("FLARESOLVERR_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8191/v1"
	}
	
	// In GitHub Actions, FlareSolverr service container needs more time to start
	timeout := 240 * time.Second // Default: 4 minutes
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		timeout = 360 * time.Second // GitHub Actions: 6 minutes (service startup + challenge solving)
	}
	
	return &FlareSolverrClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Get makes a GET request through FlareSolverr
func (f *FlareSolverrClient) Get(ctx context.Context, url string, cookies map[string]string, headers map[string]string) (string, error) {
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
		return "", fmt.Errorf("marshal request: %w", err)
	}
	
	req, err := http.NewRequestWithContext(ctx, "POST", f.baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	
	var flareResp FlareSolverrResponse
	if err := json.Unmarshal(body, &flareResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}
	
	if flareResp.Status != "ok" {
		return "", fmt.Errorf("flaresolverr error: %s", flareResp.Message)
	}
	
	return flareResp.Solution.Response, nil
}

// IsFlareSolverrEnabled checks if FlareSolverr should be used
func IsFlareSolverrEnabled() bool {
	return os.Getenv("USE_FLARESOLVERR") == "true"
}
