package chaturbate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HeapOfChaos/goondvr/internal"
	"github.com/HeapOfChaos/goondvr/server"
	"github.com/HeapOfChaos/goondvr/site"
	"github.com/HeapOfChaos/goondvr/stripchat"
	"github.com/avast/retry-go/v4"
	"github.com/grafov/m3u8"
	"github.com/samber/lo"
)

// Chaturbate implements site.Site for the Chaturbate platform.
type Chaturbate struct{}

// New returns a new Chaturbate site implementation.
func New() *Chaturbate {
	return &Chaturbate{}
}

// FetchStream implements site.Site. It returns *site.StreamInfo if online, nil if offline.
func (cb *Chaturbate) FetchStream(ctx context.Context, req *internal.Req, username string) (*site.StreamInfo, error) {
	stream, err := FetchStream(ctx, req, username)
	if err != nil {
		info := &site.StreamInfo{}
		if stream != nil {
			info.RoomTitle = stream.RoomTitle
			info.Gender = stream.Gender
			info.NumViewers = stream.NumViewers
			info.SummaryCardImage = stream.SummaryCardImage
		}

		// Preserve metadata on offline/private/hidden responses so the UI can
		// still show room title/profile imagery for channels that aren't live.
		if errors.Is(err, internal.ErrChannelOffline) ||
			errors.Is(err, internal.ErrPrivateStream) ||
			errors.Is(err, internal.ErrHiddenStream) {
			return info, err
		}
		return info, err
	}
	if stream == nil || stream.HLSSource == "" {
		return nil, nil
	}
	return &site.StreamInfo{
		HLSURL:           stream.HLSSource,
		RoomTitle:        stream.RoomTitle,
		Gender:           stream.Gender,
		NumViewers:       stream.NumViewers,
		SummaryCardImage: stream.SummaryCardImage,
	}, nil
}

// FetchLastBroadcast implements site.Site.
func (cb *Chaturbate) FetchLastBroadcast(ctx context.Context, req *internal.Req, username string) (int64, error) {
	return FetchLastBroadcast(ctx, req, username)
}

type Client struct {
	Req *internal.Req
}

func NewClient() *Client {
	return &Client{Req: internal.NewReq()}
}

func (c *Client) GetStream(ctx context.Context, username string) (*Stream, error) {
	return FetchStream(ctx, c.Req, username)
}

type apiResponse struct {
	URL          string `json:"url"`
	RoomStatus   string `json:"room_status"`
	HLSSource    string `json:"hls_source"`
	Success      bool   `json:"success"`
	Code         string `json:"code"`
	RoomTitle    string `json:"room_title"`
	Gender       string `json:"broadcaster_gender"`
	NumViewers   int    `json:"num_viewers"`
	EdgeRegion   string `json:"edge_region"`
	SummaryCardImage string `json:"summary_card_image"`
}

func FetchStream(ctx context.Context, client *internal.Req, username string) (*Stream, error) {
	// Generate CSRF token
	csrfToken := fmt.Sprintf("%032x", time.Now().UnixNano())
	
	// Use the correct POST API
	body, err := internal.PostChaturbateAPI(ctx, username, csrfToken)
	if err != nil {
		// If Cloudflare blocked us, try scraping with FlareSolverr
		if errors.Is(err, internal.ErrCloudflareBlocked) {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] Cloudflare block detected, trying FlareSolverr scraping...\n")
			}
			
			// Try scraping the public page with retries and different strategies
			var hlsURL, status string
			var scrapeErr error
			
			for attempt := 1; attempt <= 5; attempt++ {
				if server.Config.Debug {
					fmt.Printf("[DEBUG] FlareSolverr attempt %d/5...\n", attempt)
				}
				
				// Create a context with longer timeout for FlareSolverr (independent of recording duration)
				attemptCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
				
				// Try different approaches based on attempt number
				if attempt <= 3 {
					// First 3 attempts: Use FlareSolverr with sessions
					hlsURL, status, scrapeErr = internal.ScrapeChaturbateStreamWithFlareSolverr(attemptCtx, username)
				} else {
					// Last 2 attempts: Try direct scraping (might work if CF protection is lighter)
					if server.Config.Debug {
						fmt.Printf("[DEBUG] Switching to direct scraping for attempt %d\n", attempt)
					}
					hlsURL, status, scrapeErr = internal.ScrapeChaturbateStream(attemptCtx, username)
				}
				cancel()
				
				if scrapeErr == nil {
					break
				}
				
				if server.Config.Debug {
					fmt.Printf("[DEBUG] FlareSolverr attempt %d failed: %v\n", attempt, scrapeErr)
				}
				
				// Exponential backoff with jitter to avoid FlareSolverr congestion
				if attempt < 5 {
					baseDelay := time.Duration(15+attempt*15) * time.Second
					jitter := time.Duration(attempt*5) * time.Second
					delay := baseDelay + jitter
					if server.Config.Debug {
						nextMethod := "FlareSolverr"
						if attempt >= 3 {
							nextMethod = "direct scraping"
						}
						fmt.Printf("[DEBUG] Waiting %v before retry (attempt %d will use %s)...\n", 
							delay, attempt+1, nextMethod)
					}
					
					// Check if context is cancelled during wait
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(delay):
					}
				}
			}
			
			if scrapeErr != nil {
				if server.Config.Debug {
					fmt.Printf("[DEBUG] All FlareSolverr attempts failed, returning Cloudflare error\n")
				}
				return nil, fmt.Errorf("failed to get stream info: %w", err)
			}
			
			meta := &Stream{}
			
			if status == "offline" {
				return meta, internal.ErrChannelOffline
			}
			
			if status == "private" {
				return meta, internal.ErrPrivateStream
			}
			
			if hlsURL == "" {
				return meta, internal.ErrChannelOffline
			}
			
			meta.HLSSource = hlsURL
			if server.Config.Debug {
				fmt.Printf("[DEBUG] Successfully scraped HLS URL: %s\n", hlsURL)
			}
			return meta, nil
		}
		
		return nil, fmt.Errorf("failed to get stream info: %w", err)
	}
	
	if server.Config.Debug {
		fmt.Printf("[DEBUG] API response body: %s\n", body)
	}

	var resp apiResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse stream info: %w", err)
	}

	if server.Config.Debug {
		fmt.Printf("[DEBUG] Parsed response - success=%v, url_present=%v, room_status=%s\n", 
			resp.Success, resp.URL != "", resp.RoomStatus)
	}

	// Always populate static metadata so the caller can update it even when offline.
	meta := &Stream{
		RoomTitle:        resp.RoomTitle,
		Gender:           resp.Gender,
		EdgeRegion:       resp.EdgeRegion,
		SummaryCardImage: resp.SummaryCardImage,
	}

	// If we have a URL, the stream is accessible regardless of room_status
	if resp.URL != "" {
		meta.HLSSource = resp.URL
		meta.NumViewers = resp.NumViewers
		return meta, nil
	}

	// If success is true but no URL, might be offline
	if resp.Success {
		return meta, internal.ErrChannelOffline
	}

	// Check room status only if no URL and not successful
	switch resp.RoomStatus {
	case "private":
		return meta, internal.ErrPrivateStream
	case "hidden":
		return meta, internal.ErrHiddenStream
	default:
		return meta, internal.ErrChannelOffline
	}
}

// fetchStreamViaFlareSolverr uses FlareSolverr's real Chrome browser to visit
// the room page and extract the HLS stream URL from initialRoomDossier.
// This is the only reliable method from data center IPs because CycleTLS's TLS
// fingerprint doesn't match the cf_clearance cookie, causing Cloudflare to return
// fake responses. FlareSolverr's Chrome has a consistent session+fingerprint.
func fetchStreamViaFlareSolverr(ctx context.Context, username string) (*Stream, error) {
	flare := internal.NewFlareSolverrClient()

	// Clean username to prevent trailing spaces/newlines from corrupting the URL
	cleanUsername := strings.TrimSpace(username)

	// Ensure domain has exactly one trailing slash
	domain := strings.TrimRight(server.Config.Domain, "/")
	roomURL := fmt.Sprintf("%s/%s/", domain, cleanUsername)

	// CRITICAL: Do NOT pass cookies to FlareSolverr for room page requests
	// Let FlareSolverr's Chrome browser establish its own fresh session
	// Passing pre-existing cookies causes Chaturbate to redirect to homepage
	// because the session isn't properly established for that specific room
	
	// Headers for the request
	// CRITICAL: X-Requested-With header bypasses age gate
	// Source: https://gist.github.com/you-cant-see-me/811ab5f9461b7aa0d69f59db7eed98ec
	headers := map[string]string{
		"Accept":           "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language":  "en-US,en;q=0.5",
		"X-Requested-With": "XMLHttpRequest", // CRITICAL: Bypass age gate
	}

	// Fetch room page through FlareSolverr's real Chrome browser
	// Pass nil for cookies to let FlareSolverr establish a fresh session
	fmt.Printf("[DEBUG] %s: Fetching room page via FlareSolverr: %s\n", cleanUsername, roomURL)
	htmlBody, _, _, err := flare.GetWithCookiesAndUA(ctx, roomURL, nil, headers)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr room fetch failed: %w", err)
	}

	fmt.Printf("[DEBUG] %s: Room page received (length: %d)\n", cleanUsername, len(htmlBody))

	// Check for Chaturbate's 404 page (cancelled/deleted broadcaster)
	if strings.Contains(htmlBody, "HTTP 404") || strings.Contains(htmlBody, "cancelled broadcaster") || strings.Contains(htmlBody, "Page Not Found") {
		fmt.Printf("[INFO] %s: Room page returned 404 (broadcaster may be cancelled or deleted)\n", cleanUsername)
		return &Stream{}, internal.ErrChannelOffline
	}
	
	// Check for offline indicators in HTML (before trying to parse)
	if strings.Contains(htmlBody, "offline_tipping") || strings.Contains(htmlBody, "tip_offline") {
		fmt.Printf("[INFO] %s: Channel is offline (detected from HTML markers)\n", cleanUsername)
		return &Stream{}, internal.ErrChannelOffline
	}
	
	// Check for private show indicators
	if strings.Contains(htmlBody, "This room is currently in a private show") {
		fmt.Printf("[INFO] %s: Channel is in private show\n", cleanUsername)
		return &Stream{}, internal.ErrPrivateStream
	}

	// Parse initialRoomDossier from HTML
	// Format: window.initialRoomDossier = "...escaped JSON..."
	stream, err := parseInitialRoomDossier(htmlBody, cleanUsername)
	if err != nil {
		fmt.Printf("[INFO] %s: Could not parse initialRoomDossier: %v\n", username, err)
		
		// If we can't parse the dossier, the channel is likely offline or doesn't exist
		// No need to try fallbacks - just return offline
		fmt.Printf("[INFO] %s: Channel appears to be offline (no room data found)\n", username)
		return &Stream{}, internal.ErrChannelOffline
	}

	return stream, nil
}

// parseInitialRoomDossier extracts stream info from the initialRoomDossier
// JSON embedded in the Chaturbate room page HTML.
func parseInitialRoomDossier(html, username string) (*Stream, error) {
	// Look for: window.initialRoomDossier = "...";
	// The value is a JSON string that's been escaped (quotes, unicode)
	
	// Try multiple patterns in order of likelihood
	patterns := []string{
		"window.initialRoomDossier = \"",
		"initialRoomDossier = \"",
		"window.initialRoomDossier=\"",
		"initialRoomDossier=\"",
		"\"initialRoomDossier\":\"", // JSON format
	}
	
	startIdx := -1
	
	for _, pattern := range patterns {
		idx := strings.Index(html, pattern)
		if idx != -1 {
			startIdx = idx + len(pattern)
			fmt.Printf("[DEBUG] %s: Found initialRoomDossier using pattern: %q\n", username, pattern)
			break
		}
	}
	
	if startIdx == -1 {
		// Debug: Show what we're actually getting
		fmt.Printf("[DEBUG] %s: HTML length: %d bytes\n", username, len(html))
		
		// Check if we got a Cloudflare challenge page
		if strings.Contains(html, "cf-challenge") || strings.Contains(html, "Checking your browser") {
			fmt.Printf("[ERROR] %s: Received Cloudflare challenge page - cookies may be invalid\n", username)
			return nil, fmt.Errorf("cloudflare challenge detected - check cookies")
		}
		
		// Check if we got a real 404 page (more specific check)
		// Real 404 pages are usually small (<50KB) and contain specific error messages
		if len(html) < 50000 && (strings.Contains(html, "<title>404") || strings.Contains(html, "Page Not Found")) {
			fmt.Printf("[ERROR] %s: Received 404 page - channel may not exist\n", username)
			return nil, fmt.Errorf("channel not found (404)")
		}
		
		// Extract meaningful snippets from different parts of the HTML
		// Look for script tags, title, and body content
		fmt.Printf("[DEBUG] %s: Analyzing HTML structure...\n", username)
		
		// Check for title tag
		if titleStart := strings.Index(html, "<title>"); titleStart != -1 {
			titleEnd := strings.Index(html[titleStart:], "</title>")
			if titleEnd != -1 {
				title := html[titleStart+7 : titleStart+titleEnd]
				fmt.Printf("[DEBUG] %s: Page title: %s\n", username, title)
			}
		}
		
		// Look for any script tags that might contain room data
		scriptCount := strings.Count(html, "<script")
		fmt.Printf("[DEBUG] %s: Found %d script tags in HTML\n", username, scriptCount)
		
		// Check if this looks like a valid Chaturbate room page
		hasRoomPage := strings.Contains(html, "room_page") || strings.Contains(html, "roomPage")
		hasChaturbate := strings.Contains(html, "chaturbate")
		fmt.Printf("[DEBUG] %s: Has room_page markers: %v, Has chaturbate markers: %v\n", username, hasRoomPage, hasChaturbate)
		
		// Show a snippet of the HTML around where we'd expect to find the data
		// Look for common Chaturbate page markers
		markers := []string{"window.", "var ", "const ", "<script>"}
		for _, marker := range markers {
			if idx := strings.Index(html, marker); idx != -1 {
				start := idx
				if start > 200 {
					start = idx - 200
				}
				end := idx + 800
				if end > len(html) {
					end = len(html)
				}
				snippet := html[start:end]
				fmt.Printf("[DEBUG] %s: HTML snippet around '%s': %s\n", username, marker, snippet)
				break // Only show one snippet
			}
		}
		
		// Also search for any mention of "initialRoomDossier" without the exact pattern
		if idx := strings.Index(html, "initialRoomDossier"); idx != -1 {
			start := idx - 100
			if start < 0 {
				start = 0
			}
			end := idx + 500
			if end > len(html) {
				end = len(html)
			}
			snippet := html[start:end]
			fmt.Printf("[DEBUG] %s: Found 'initialRoomDossier' text at position %d: %s\n", username, idx, snippet)
		} else {
			fmt.Printf("[DEBUG] %s: String 'initialRoomDossier' not found anywhere in HTML\n", username)
		}
		
		return nil, fmt.Errorf("initialRoomDossier not found in HTML (tried %d patterns)", len(patterns))
	}

	// Find the closing quote - it's escaped JSON inside a string literal
	// so we need to find an unescaped closing quote
	endIdx := -1
	for i := startIdx; i < len(html); i++ {
		if html[i] == '"' && (i == 0 || html[i-1] != '\\') {
			endIdx = i
			break
		}
	}
	if endIdx == -1 {
		return nil, fmt.Errorf("could not find end of initialRoomDossier string")
	}

	// The content is a JSON string that was escaped for embedding in a JS string literal.
	// It uses \u0022 for quotes and \/ for slashes.
	// We can let Go's strconv.Unquote evaluate the Javascript string literal syntax
	// perfectly by wrapping it back in quotes.
	rawJSON := html[startIdx:endIdx]
	
	unquoted, err := strconv.Unquote(`"` + rawJSON + `"`)
	if err != nil {
		// Fallback to manual replacement if unquote fails for some reason
		rawJSON = strings.ReplaceAll(rawJSON, "\\u0022", "\"")
		rawJSON = strings.ReplaceAll(rawJSON, "\\\"", "\"")
		rawJSON = strings.ReplaceAll(rawJSON, "\\/", "/")
		rawJSON = strings.ReplaceAll(rawJSON, "\\\\", "\\")
		unquoted = rawJSON
	}

	// Parse the JSON
	var dossier struct {
		HLSSource        string `json:"hls_source"`
		RoomStatus       string `json:"room_status"`
		RoomTitle        string `json:"room_title"`
		BroadcasterGender string `json:"broadcaster_gender"`
		NumViewers       int    `json:"num_viewers"`
		SummaryCardImage string `json:"summary_card_image"`
	}

	if err := json.Unmarshal([]byte(unquoted), &dossier); err != nil {
		// Log first 500 chars of raw JSON for debugging
		preview := unquoted
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		fmt.Printf("[DEBUG] %s: Failed to parse dossier JSON: %v\n", username, err)
		fmt.Printf("[DEBUG] %s: Raw JSON preview: %s\n", username, preview)
		return nil, fmt.Errorf("parse initialRoomDossier JSON: %w", err)
	}

	fmt.Printf("[INFO] %s: Room dossier - room_status=%q, hls_source_present=%v, viewers=%d\n",
		username, dossier.RoomStatus, dossier.HLSSource != "", dossier.NumViewers)

	meta := &Stream{
		RoomTitle:        dossier.RoomTitle,
		Gender:           dossier.BroadcasterGender,
		NumViewers:       dossier.NumViewers,
		SummaryCardImage: dossier.SummaryCardImage,
	}

	if dossier.HLSSource != "" {
		// Clean up fast_start=true which limits resolution to 540p or lower for fast initial playback
		hlsURL := dossier.HLSSource
		hlsURL = strings.ReplaceAll(hlsURL, "?fast_start=true&", "?")
		hlsURL = strings.ReplaceAll(hlsURL, "&fast_start=true", "")
		hlsURL = strings.ReplaceAll(hlsURL, "?fast_start=true", "")
		
		if hlsURL != dossier.HLSSource {
			fmt.Printf("[DEBUG] %s: Removed fast_start=true from HLS URL to allow maximum quality\n", username)
		}
		
		meta.HLSSource = hlsURL
		fmt.Printf("[INFO] %s: ✅ Stream detected via FlareSolverr! HLS URL found\n", username)
		return meta, nil
	}

	switch dossier.RoomStatus {
	case "public":
		// Room is public but no HLS source - age gate issue even with FlareSolverr
		fmt.Printf("[WARN] %s: Room is PUBLIC but no HLS source in dossier\n", username)
		return meta, internal.ErrAgeVerification
	case "private":
		return meta, internal.ErrPrivateStream
	case "hidden":
		return meta, internal.ErrHiddenStream
	case "offline":
		return meta, internal.ErrChannelOffline
	default:
		fmt.Printf("[INFO] %s: Unknown room_status=%q, treating as offline\n", username, dossier.RoomStatus)
		return meta, internal.ErrChannelOffline
	}
}

// bioResponse is the subset of fields we care about from the biocontext API.
type bioResponse struct {
	LastBroadcast string `json:"last_broadcast"`
}

// FetchLastBroadcast calls the biocontext API and returns the last_broadcast
// time as a Unix timestamp, or 0 if unavailable.
func FetchLastBroadcast(ctx context.Context, req *internal.Req, username string) (int64, error) {
	// biocontext API also requires age verification cookies — skip in CI mode
	// where the AG_Key cookie is session-bound to FlareSolverr's Chrome
	if os.Getenv("USE_FLARESOLVERR") == "true" {
		return 0, nil
	}
	apiURL := fmt.Sprintf("%sapi/biocontext/%s/", server.Config.Domain, username)
	body, err := req.Get(ctx, apiURL)
	if err != nil {
		return 0, fmt.Errorf("fetch biocontext: %w", err)
	}
	var bio bioResponse
	if err := json.Unmarshal([]byte(body), &bio); err != nil {
		return 0, fmt.Errorf("parse biocontext: %w", err)
	}
	if bio.LastBroadcast == "" {
		return 0, nil
	}
	t, err := time.Parse("2006-01-02T15:04:05.999", bio.LastBroadcast)
	if err != nil {
		return 0, fmt.Errorf("parse last_broadcast: %w", err)
	}
	return t.Unix(), nil
}

type Stream struct {
	HLSSource        string
	RoomTitle        string
	Gender           string
	NumViewers       int
	EdgeRegion       string
	SummaryCardImage string
}

func (s *Stream) GetPlaylist(ctx context.Context, resolution, framerate int) (*Playlist, error) {
	return FetchPlaylist(ctx, s.HLSSource, resolution, framerate, "", "")
}

func FetchPlaylist(ctx context.Context, hlsSource string, resolution, framerate int, cdnReferer, mouflonPDKey string) (*Playlist, error) {
	if hlsSource == "" {
		// The page loaded but the stream is not active — treat as offline.
		return nil, internal.ErrChannelOffline
	}

	// Clean up fast_start=true which restricts the playlist to lower qualities
	hlsSource = strings.ReplaceAll(hlsSource, "?fast_start=true&", "?")
	hlsSource = strings.ReplaceAll(hlsSource, "&fast_start=true", "")
	hlsSource = strings.ReplaceAll(hlsSource, "?fast_start=true", "")

	var client *internal.Req
	if cdnReferer != "" {
		client = internal.NewMediaReqWithReferer(cdnReferer)
	} else {
		client = internal.NewMediaReq()
	}
	resp, err := client.Get(ctx, hlsSource)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch HLS source: %w", err)
	}

	if server.Config.Debug {
		fmt.Printf("[DEBUG] master playlist response for %s:\n%s\n", hlsSource, resp)
	}

	// Extract Stripchat's custom MOUFLON tag which carries the CDN pkey.
	// Format: #EXT-X-MOUFLON:PSCH:v2:{pkey}
	// The variant URLs in the master omit the pkey; it must be appended when fetching.
	var mouflonSuffix string
	pkey := stripchat.ParsePKeyFromMaster(resp)
	if pkey != "" {
		// Build the query suffix needed for variant playlist URLs.
		mouflonSuffix = fmt.Sprintf("&psch=v2&pkey=%s", pkey)

		// Resolve the actual decryption key (pdkey) from the pkey.
		if mouflonPDKey == "auto" {
			mouflonPDKey = stripchat.ResolvePDKey(ctx, pkey)
			if mouflonPDKey == "pending" {
				if server.Config.Debug {
					fmt.Println("[DEBUG] mouflon: candidate keys extracted; will test against first encrypted segment")
				}
			} else if mouflonPDKey != "" {
				if server.Config.Debug {
					fmt.Printf("[DEBUG] mouflon: pdkey resolved for pkey=%s (%d chars)\n", pkey, len(mouflonPDKey))
				}
			} else {
				fmt.Printf("[WARN] mouflon: no pdkey for pkey=%s; segments will 404. Use --stripchat-pdkey to set manually.\n", pkey)
			}
		}
	}

	playlist, err := ParsePlaylist(resp, hlsSource, resolution, framerate)
	if err != nil {
		return nil, err
	}
	if mouflonSuffix != "" {
		playlist.PlaylistURL += mouflonSuffix
		if playlist.AudioPlaylistURL != "" {
			playlist.AudioPlaylistURL += mouflonSuffix
		}
	}
	playlist.Client = client
	playlist.MouflonPDKey = mouflonPDKey
	return playlist, nil
}

func ParsePlaylist(resp, hlsSource string, resolution, framerate int) (*Playlist, error) {
	p, _, err := m3u8.DecodeFrom(strings.NewReader(resp), true)
	if err != nil {
		if server.Config.Debug {
			fmt.Printf("[DEBUG] master playlist parse failed: %v\n", err)
			fmt.Printf("[DEBUG]   HLS source URL: %s\n", hlsSource)
			end := len(resp)
			if end > 2000 {
				end = 2000
			}
			fmt.Printf("[DEBUG]   Response (first 2000 chars):\n%s\n", resp[:end])
		}
		return nil, fmt.Errorf("failed to decode m3u8 playlist: %w", err)
	}

	masterPlaylist, ok := p.(*m3u8.MasterPlaylist)
	if !ok {
		return nil, errors.New("invalid master playlist format")
	}

	return PickPlaylist(masterPlaylist, hlsSource, resolution, framerate)
}

// Playlist represents an HLS playlist containing variant streams.
type Playlist struct {
	PlaylistURL      string
	AudioPlaylistURL string // LL-HLS audio rendition URL; empty for legacy streams
	RootURL          string // base for resolving video segment URIs
	Resolution       int
	Framerate        int
	FileExt          string        // ".ts" for legacy HLS, ".mp4" for LL-HLS fMP4
	Client           *internal.Req // reuse the same client that fetched the master playlist
	MouflonPDKey     string        // Stripchat MOUFLON v2 decryption key; empty for Chaturbate
}

// VideoResolution represents a video resolution and its corresponding framerate URLs.
type VideoResolution struct {
	Framerate map[int]string // [framerate]url
	Width     int
}

// Resolution is a type alias kept for compatibility.
type Resolution = VideoResolution

func resolveHLSURL(base, ref string) string {
	baseClean := strings.SplitN(base, "?", 2)[0]
	baseURL, err := url.Parse(baseClean)
	if err != nil {
		return base + ref
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return base + ref
	}
	return baseURL.ResolveReference(refURL).String()
}

func PickPlaylist(masterPlaylist *m3u8.MasterPlaylist, baseURL string, resolution, framerate int) (*Playlist, error) {
	resolutions := map[int]*VideoResolution{}

	for _, v := range masterPlaylist.Variants {
		parts := strings.Split(v.Resolution, "x")
		if len(parts) != 2 {
			continue
		}
		width, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("parse resolution: %w", err)
		}
		framerateVal := 30
		if strings.Contains(v.Name, "FPS:60.0") {
			framerateVal = 60
		}
		if _, exists := resolutions[width]; !exists {
			resolutions[width] = &VideoResolution{Framerate: map[int]string{}, Width: width}
		}
		resolutions[width].Framerate[framerateVal] = v.URI
	}

	variant, exists := resolutions[resolution]
	if !exists {
		candidates := lo.Filter(lo.Values(resolutions), func(r *VideoResolution, _ int) bool {
			return r.Width < resolution
		})
		variant = lo.MaxBy(candidates, func(a, b *VideoResolution) bool {
			return a.Width > b.Width
		})
	}
	if variant == nil {
		return nil, fmt.Errorf("resolution not found")
	}

	var (
		finalResolution = variant.Width
		finalFramerate  = framerate
	)
	playlistURL, exists := variant.Framerate[framerate]
	if !exists {
		for fr, u := range variant.Framerate {
			playlistURL = u
			finalFramerate = fr
			break
		}
	}

	fileExt := ".ts"
	if strings.Contains(playlistURL, "llhls") || strings.HasSuffix(strings.SplitN(playlistURL, "?", 2)[0], ".m4s") {
		fileExt = ".mp4"
	}

	// Stripchat uses fMP4 segments (.mp4) even though the playlist URL
	// doesn't contain "llhls" or end in ".m4s". Detect by checking the
	// master playlist for EXT-X-MAP (init segment indicator) in any variant.
	if fileExt == ".ts" && strings.Contains(baseURL, "doppiocdn") {
		fileExt = ".mp4"
	}

	// For LL-HLS streams, find the audio rendition from the selected variant's EXT-X-MEDIA alternatives.
	var audioPlaylistURL string
	if fileExt == ".mp4" {
		for _, v := range masterPlaylist.Variants {
			if v.URI == playlistURL {
				for _, alt := range v.Alternatives {
					if alt != nil && alt.Type == "AUDIO" && alt.URI != "" {
						audioPlaylistURL = resolveHLSURL(baseURL, alt.URI)
						break
					}
				}
				break
			}
		}
		if server.Config.Debug {
			if audioPlaylistURL != "" {
				fmt.Printf("[DEBUG] LL-HLS audio rendition: %s\n", audioPlaylistURL)
			} else {
				fmt.Printf("[DEBUG] LL-HLS stream has no separate audio rendition\n")
			}
		}
	}

	resolvedPlaylist := resolveHLSURL(baseURL, playlistURL)
	return &Playlist{
		PlaylistURL:      resolvedPlaylist,
		AudioPlaylistURL: audioPlaylistURL,
		RootURL:          strings.SplitN(resolvedPlaylist, "?", 2)[0],
		Resolution:       finalResolution,
		Framerate:        finalFramerate,
		FileExt:          fileExt,
	}, nil
}

// WatchHandler is a function type that processes video segments.
type WatchHandler func(b []byte, duration float64) error

// WatchSegments continuously fetches and processes video segments.
// For LL-HLS streams with a separate audio rendition it automatically muxes
// audio and video into a single fragmented MP4 output stream.
func (p *Playlist) WatchSegments(ctx context.Context, handler WatchHandler) error {
	if p.AudioPlaylistURL != "" {
		return p.watchMuxedSegments(ctx, handler)
	}
	return p.watchVideoOnlySegments(ctx, handler)
}

// safeDecodeFrom wraps m3u8.DecodeFrom with a recover so that library panics
// (e.g. nil-pointer on unknown LL-HLS tags) are returned as errors instead of
// crashing the process.
func safeDecodeFrom(r io.Reader) (pl m3u8.Playlist, listType m3u8.ListType, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("m3u8 decode panic: %v", rec)
		}
	}()
	return m3u8.DecodeFrom(r, true)
}

// decodeMouflon rewrites a Stripchat media playlist that uses the proprietary
// #EXT-X-MOUFLON:URI: tag to hide real segment URLs behind a generic placeholder
// (e.g. https://.../media.mp4). Each MOUFLON URI tag is consumed and its real
// URL replaces the following non-comment placeholder line.
//
// When pdkey is non-empty, the encrypted token in each URI is decrypted using
// the MOUFLON v2 algorithm (reverse -> base64-decode -> XOR SHA256(pdkey)).
// If pdkey is "pending", the first encrypted URI is used to brute-force the
// correct key from candidate strings extracted from the player JS.
func decodeMouflon(resp, pdkey string) string {
	if !strings.Contains(resp, "#EXT-X-MOUFLON:URI:") {
		return resp
	}

	// If pdkey is "pending", try to find the working key from candidates
	// using the first MOUFLON URI as a test sample.
	if pdkey == "pending" {
		for _, line := range strings.Split(resp, "\n") {
			trimmed := strings.TrimRight(line, "\r")
			if strings.HasPrefix(trimmed, "#EXT-X-MOUFLON:URI:") {
				sampleURI := strings.TrimPrefix(trimmed, "#EXT-X-MOUFLON:URI:")
				found := stripchat.TryFindWorkingKey(sampleURI)
				if found != "" {
					pdkey = found
				} else {
					pdkey = "" // no working key found
				}
				break
			}
		}
	}

	lines := strings.Split(resp, "\n")
	out := make([]string, 0, len(lines))
	var pendingURI string
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, "#EXT-X-MOUFLON:URI:") {
			uri := strings.TrimPrefix(trimmed, "#EXT-X-MOUFLON:URI:")
			if pdkey != "" {
				decrypted, err := stripchat.DecryptMouflonURI(uri, pdkey)
				if err != nil {
					if server.Config.Debug {
						fmt.Printf("[DEBUG] mouflon decrypt failed for URI: %v\n", err)
					}
				} else {
					uri = decrypted
				}
			}
			pendingURI = uri
			continue // drop the MOUFLON tag line entirely
		}
		if pendingURI != "" && !strings.HasPrefix(trimmed, "#") && trimmed != "" {
			out = append(out, pendingURI) // real URI replaces placeholder
			pendingURI = ""
			continue // drop the placeholder line
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// normalizeM3U8 fixes non-standard #EXTINF lines that lack a trailing comma,
// and strips LL-HLS extension tags that cause the m3u8 library to panic.
// Some CDNs (e.g. Stripchat) emit "#EXTINF:2.000" instead of "#EXTINF:2.000,".
func normalizeM3U8(resp string) string {
	// LL-HLS tags the grafov/m3u8 library cannot handle without panicking.
	stripPrefixes := []string{
		"#EXT-X-PART:",
		"#EXT-X-PART-INF:",
		"#EXT-X-PRELOAD-HINT:",
		"#EXT-X-SERVER-CONTROL:",
		"#EXT-X-RENDITION-REPORT:",
		"#EXT-X-MOUFLON:",
	}
	lines := strings.Split(resp, "\n")
	out := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		skip := false
		for _, pfx := range stripPrefixes {
			if strings.HasPrefix(trimmed, pfx) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		if strings.HasPrefix(trimmed, "#EXTINF:") && !strings.Contains(trimmed, ",") {
			trimmed = trimmed + ","
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// watchVideoOnlySegments is the original single-track segment loop.
func (p *Playlist) watchVideoOnlySegments(ctx context.Context, handler WatchHandler) error {
	client := p.Client
	if client == nil {
		client = internal.NewMediaReq()
	}
	lastSeq := -1
	lastSegURI := ""
	lastMapURI := ""
	consecutiveErrors := 0

	// For fMP4 streams, normalise tfdt timestamps so the recording starts
	// at 0:00 instead of the CDN's absolute stream uptime. Always attempt
	// this — extractAllTrackBaseTimes returns nil on non-fMP4 (.ts) data.
	var trackBaseTimes map[uint32]uint64

	// Track when we last received a new segment to detect stale streams
	lastSegmentTime := time.Now()
	const staleTimeout = 90 * time.Second // If no new segments for 90s, consider stream ended

	for {
		resp, err := client.Get(ctx, p.PlaylistURL)
		if err != nil {
			if consecutiveErrors++; consecutiveErrors >= 5 {
				return fmt.Errorf("get playlist: %w", err)
			}
			<-time.After(2 * time.Second)
			continue
		}
		pl, _, err := safeDecodeFrom(strings.NewReader(normalizeM3U8(decodeMouflon(resp, p.MouflonPDKey))))
		if err != nil {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] variant playlist parse failed: %v\n", err)
				fmt.Printf("[DEBUG]   Playlist URL: %s\n", p.PlaylistURL)
				end := len(resp)
				if end > 2000 {
					end = 2000
				}
				fmt.Printf("[DEBUG]   Response (first 2000 chars):\n%s\n", resp[:end])
			}
			if consecutiveErrors++; consecutiveErrors >= 5 {
				return fmt.Errorf("decode from: %w", err)
			}
			<-time.After(2 * time.Second)
			continue
		}
		playlist, ok := pl.(*m3u8.MediaPlaylist)
		if !ok {
			return fmt.Errorf("cast to media playlist")
		}
		consecutiveErrors = 0

		if server.Config.Debug {
			var count int
			for _, v := range playlist.Segments {
				if v != nil {
					count++
				}
			}
			fmt.Printf("[DEBUG] playlist poll: mediaSeq=%d segments=%d lastSeq=%d url=%s\n",
				playlist.SeqNo, count, lastSeq, p.PlaylistURL)
		}

		for _, v := range playlist.Segments {
			if v == nil {
				continue
			}
			seq := internal.SegmentSeq(v.URI)
			// Fall back to the HLS media sequence number (v.SeqId) when the URI
			// doesn't contain a parseable sequence (e.g. Stripchat .mp4 segments).
			if seq == -1 && v.SeqId > 0 {
				seq = int(v.SeqId)
			}
			if server.Config.Debug && lastSeq == -1 && lastSegURI == "" {
				fmt.Printf("[DEBUG] first segment URI: %s (seq=%d)\n", v.URI, seq)
			}
			if seq != -1 {
				if seq <= lastSeq {
					continue
				}
				lastSeq = seq
			} else {
				if v.URI == lastSegURI {
					continue
				}
			}
			lastSegURI = v.URI
			if v.Map != nil && v.Map.URI != lastMapURI {
				mapURL := resolveHLSURL(p.RootURL, v.Map.URI)
				initBytes, err := client.GetBytes(ctx, mapURL)
				if err != nil {
					return fmt.Errorf("get init segment: %w", err)
				}
				if err := handler(initBytes, 0); err != nil {
					return fmt.Errorf("handler init segment: %w", err)
				}
				lastMapURI = v.Map.URI
			}

			lastSeq = seq

			pipeline := func() ([]byte, error) {
				return client.GetBytes(ctx, resolveHLSURL(p.RootURL, v.URI))
			}
			resp, err := retry.DoWithData(
				pipeline,
				retry.Context(ctx),
				retry.Attempts(3),
				retry.Delay(600*time.Millisecond),
				retry.DelayType(retry.FixedDelay),
				retry.RetryIf(func(err error) bool {
					return !errors.Is(err, internal.ErrNotFound)
				}),
			)
			if err != nil {
				if errors.Is(err, internal.ErrNotFound) {
					if server.Config.Debug {
						fmt.Printf("[DEBUG] segment 404 (skipping): seq=%d %s\n", seq, resolveHLSURL(p.RootURL, v.URI))
					}
					continue // segment expired on CDN; move on to next
				}
				if server.Config.Debug {
					fmt.Printf("[DEBUG] segment error (breaking inner loop): seq=%d err=%v\n", seq, err)
				}
				break
			}
			// Normalise fMP4 tfdt so playback starts at 0:00 (all tracks).
			if trackBaseTimes == nil {
				trackBaseTimes = extractAllTrackBaseTimes(resp)
			}
			if trackBaseTimes != nil {
				resp = shiftSegmentAllTracks(resp, trackBaseTimes)
			}
			if err := handler(resp, v.Duration); err != nil {
				return fmt.Errorf("handler: %w", err)
			}
			
			// Update last segment time when we successfully process a new segment
			lastSegmentTime = time.Now()
		}

		// Check if playlist has gone stale (no new segments for staleTimeout duration)
		if time.Since(lastSegmentTime) > staleTimeout {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] playlist stale: no new segments for %v, stream likely ended\n", staleTimeout)
			}
			return internal.ErrChannelOffline
		}

		<-time.After(1 * time.Second)
	}
}

// watchMuxedSegments polls both video and audio LL-HLS playlists, combines their
// init segments into a single fMP4 init, then writes interleaved video and
// audio moof+mdat fragments. Audio track_id is renumbered to 2.
// tfdt decode times are normalised to start from zero so players display the
// correct recording position rather than the CDN stream uptime offset.
func (p *Playlist) watchMuxedSegments(ctx context.Context, handler WatchHandler) error {
	client := p.Client
	if client == nil {
		client = internal.NewMediaReq()
	}

	lastVideoSeq := -1
	lastAudioSeq := -1
	lastVideoURI := ""
	lastAudioURI := ""
	lastVideoMapURI := ""
	lastAudioMapURI := ""
	var videoInitBytes []byte
	var audioInitBytes []byte
	initWritten := false
	consecutiveErrors := 0

	// Per-track tfdt base times captured from the first segment of each track.
	// Subtracting these normalises timestamps to start from zero.
	var videoTimeBase uint64
	var audioTimeBase uint64
	videoBaseSet := false
	audioBaseSet := false

	// Track when we last received a new segment to detect stale streams
	lastSegmentTime := time.Now()
	const staleTimeout = 90 * time.Second // If no new segments for 90s, consider stream ended

	for {
		// Fetch video playlist
		videoResp, err := client.Get(ctx, p.PlaylistURL)
		if err != nil {
			if consecutiveErrors++; consecutiveErrors >= 5 {
				return fmt.Errorf("get video playlist: %w", err)
			}
			<-time.After(2 * time.Second)
			continue
		}
		vpl, _, err := safeDecodeFrom(strings.NewReader(normalizeM3U8(decodeMouflon(videoResp, p.MouflonPDKey))))
		if err != nil {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] muxed: video playlist parse failed: %v\n", err)
			}
			if consecutiveErrors++; consecutiveErrors >= 5 {
				return fmt.Errorf("decode video playlist: %w", err)
			}
			<-time.After(2 * time.Second)
			continue
		}
		videoPlaylist, ok := vpl.(*m3u8.MediaPlaylist)
		if !ok {
			return fmt.Errorf("cast video playlist to media playlist")
		}

		// Fetch audio playlist
		audioResp, err := client.Get(ctx, p.AudioPlaylistURL)
		if err != nil {
			if consecutiveErrors++; consecutiveErrors >= 5 {
				return fmt.Errorf("get audio playlist: %w", err)
			}
			<-time.After(2 * time.Second)
			continue
		}
		apl, _, err := safeDecodeFrom(strings.NewReader(normalizeM3U8(decodeMouflon(audioResp, p.MouflonPDKey))))
		if err != nil {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] muxed: audio playlist parse failed: %v\n", err)
			}
			if consecutiveErrors++; consecutiveErrors >= 5 {
				return fmt.Errorf("decode audio playlist: %w", err)
			}
			<-time.After(2 * time.Second)
			continue
		}
		audioPlaylist, ok := apl.(*m3u8.MediaPlaylist)
		if !ok {
			return fmt.Errorf("cast audio playlist to media playlist")
		}
		consecutiveErrors = 0

		// Collect video init segment (EXT-X-MAP)
		for _, v := range videoPlaylist.Segments {
			if v == nil {
				continue
			}
			if v.Map != nil && v.Map.URI != lastVideoMapURI {
				mapURL := resolveHLSURL(p.RootURL, v.Map.URI)
				b, err := client.GetBytes(ctx, mapURL)
				if err != nil {
					return fmt.Errorf("get video init segment: %w", err)
				}
				videoInitBytes = b
				lastVideoMapURI = v.Map.URI
				initWritten = false
			}
			break
		}

		// Collect audio init segment (EXT-X-MAP)
		for _, v := range audioPlaylist.Segments {
			if v == nil {
				continue
			}
			if v.Map != nil && v.Map.URI != lastAudioMapURI {
				mapURL := resolveHLSURL(p.AudioPlaylistURL, v.Map.URI)
				b, err := client.GetBytes(ctx, mapURL)
				if err != nil {
					return fmt.Errorf("get audio init segment: %w", err)
				}
				audioInitBytes = b
				lastAudioMapURI = v.Map.URI
				initWritten = false
			}
			break
		}

		// Write combined init once we have both init segments
		if !initWritten && videoInitBytes != nil && audioInitBytes != nil {
			combined, err := buildCombinedInit(videoInitBytes, audioInitBytes)
			if err != nil {
				return fmt.Errorf("build combined init: %w", err)
			}
			if err := handler(combined, 0); err != nil {
				return fmt.Errorf("handler combined init: %w", err)
			}
			initWritten = true
		}
		if !initWritten {
			<-time.After(1 * time.Second)
			continue
		}

		// Collect new segment URLs. Pre-resolve URLs to avoid closure capture
		// issues, and fall back to URI-string dedup when seq is unavailable.
		type segInfo struct {
			url      string
			duration float64
		}
		var newVideoSegs []segInfo
		for _, v := range videoPlaylist.Segments {
			if v == nil {
				continue
			}
			seq := internal.SegmentSeq(v.URI)
			if server.Config.Debug && lastVideoSeq == -1 && lastVideoURI == "" {
				fmt.Printf("[DEBUG] muxed: first video segment URI: %s (seq=%d)\n", v.URI, seq)
			}
			if seq != -1 {
				if seq <= lastVideoSeq {
					continue
				}
				lastVideoSeq = seq
			} else {
				if v.URI == lastVideoURI {
					continue
				}
			}
			lastVideoURI = v.URI
			newVideoSegs = append(newVideoSegs, segInfo{
				url:      resolveHLSURL(p.RootURL, v.URI),
				duration: v.Duration,
			})
		}
		var newAudioSegs []segInfo
		for _, v := range audioPlaylist.Segments {
			if v == nil {
				continue
			}
			seq := internal.SegmentSeq(v.URI)
			if server.Config.Debug && lastAudioSeq == -1 && lastAudioURI == "" {
				fmt.Printf("[DEBUG] muxed: first audio segment URI: %s (seq=%d)\n", v.URI, seq)
			}
			if seq != -1 {
				if seq <= lastAudioSeq {
					continue
				}
				lastAudioSeq = seq
			} else {
				if v.URI == lastAudioURI {
					continue
				}
			}
			lastAudioURI = v.URI
			newAudioSegs = append(newAudioSegs, segInfo{
				url:      resolveHLSURL(p.AudioPlaylistURL, v.URI),
				duration: v.Duration,
			})
		}

		if server.Config.Debug {
			fmt.Printf("[DEBUG] muxed: cycle video=%d audio=%d\n", len(newVideoSegs), len(newAudioSegs))
		}

		isStripchatMux := strings.Contains(p.PlaylistURL, "doppiocdn") || strings.Contains(p.AudioPlaylistURL, "doppiocdn")

		// Stripchat can expose video/audio playlists with different cadence,
		// and index-based pairing can produce files that begin with a long
		// video-only run after a split. Keep Chaturbate on the original paired
		// write order because it was already behaving correctly there.
		if !isStripchatMux {
			maxLen := len(newVideoSegs)
			if len(newAudioSegs) > maxLen {
				maxLen = len(newAudioSegs)
			}
			for i := 0; i < maxLen; i++ {
				var chunk []byte
				var chunkDuration float64

				if i < len(newVideoSegs) {
					vseg := newVideoSegs[i]
					vsegURL := vseg.url
					segBytes, err := retry.DoWithData(
						func() ([]byte, error) { return client.GetBytes(ctx, vsegURL) },
						retry.Context(ctx),
						retry.Attempts(3),
						retry.Delay(600*time.Millisecond),
						retry.DelayType(retry.FixedDelay),
					)
					if err == nil {
						if !videoBaseSet {
							if t, ok := extractMoofFirstTfdt(segBytes); ok {
								videoTimeBase = t
								videoBaseSet = true
							}
						}
						segBytes = shiftSegmentTfdt(segBytes, 1, videoTimeBase)
						chunk = append(chunk, segBytes...)
						chunkDuration = vseg.duration
					}
				}
				if i < len(newAudioSegs) {
					aseg := newAudioSegs[i]
					asegURL := aseg.url
					segBytes, err := retry.DoWithData(
						func() ([]byte, error) { return client.GetBytes(ctx, asegURL) },
						retry.Context(ctx),
						retry.Attempts(3),
						retry.Delay(600*time.Millisecond),
						retry.DelayType(retry.FixedDelay),
					)
					if err != nil {
						fmt.Printf("[WARN] audio seg download failed: %v\n", err)
					} else {
						if !audioBaseSet {
							if t, ok := extractMoofFirstTfdt(segBytes); ok {
								audioTimeBase = t
								audioBaseSet = true
								if server.Config.Debug {
									fmt.Printf("[DEBUG] muxed: audio base=%d\n", audioTimeBase)
								}
							}
						}
						if server.Config.Debug {
							if rawTfdt, ok := extractMoofFirstTfdt(segBytes); ok {
								var normalised uint64
								if audioTimeBase > 0 && rawTfdt >= audioTimeBase {
									normalised = rawTfdt - audioTimeBase
								}
								fmt.Printf("[DEBUG] muxed: audio seg dur=%.3f raw_tfdt=%d norm=%d\n", aseg.duration, rawTfdt, normalised)
							}
						}
						segBytes = rewriteAudioMoofTrackID(segBytes)
						segBytes = shiftSegmentTfdt(segBytes, 2, audioTimeBase)
						chunk = append(chunk, segBytes...)
					}
				}
				if len(chunk) > 0 {
					if err := handler(chunk, chunkDuration); err != nil {
						return fmt.Errorf("handler muxed segment group: %w", err)
					}
					// Update last segment time when we successfully write a chunk
					lastSegmentTime = time.Now()
				}
			}

			// Check if playlist has gone stale (no new segments for staleTimeout duration)
			if time.Since(lastSegmentTime) > staleTimeout {
				if server.Config.Debug {
					fmt.Printf("[DEBUG] muxed playlist stale: no new segments for %v, stream likely ended\n", staleTimeout)
				}
				return internal.ErrChannelOffline
			}

			<-time.After(1 * time.Second)
			continue
		}

		// Merge Stripchat by actual fragment decode time rather than playlist index.
		type pendingSeg struct {
			track    string
			time     uint64
			duration float64
			data     []byte
		}
		var pending []pendingSeg

		for _, vseg := range newVideoSegs {
			vsegURL := vseg.url
			segBytes, err := retry.DoWithData(
				func() ([]byte, error) { return client.GetBytes(ctx, vsegURL) },
				retry.Context(ctx),
				retry.Attempts(3),
				retry.Delay(600*time.Millisecond),
				retry.DelayType(retry.FixedDelay),
			)
			if err != nil {
				fmt.Printf("[WARN] video seg download failed: %v\n", err)
				continue
			}

			rawTfdt, ok := extractMoofFirstTfdt(segBytes)
			if !videoBaseSet && ok {
				videoTimeBase = rawTfdt
				videoBaseSet = true
			}

			normalisedTime := rawTfdt
			if videoBaseSet && rawTfdt >= videoTimeBase {
				normalisedTime = rawTfdt - videoTimeBase
			}
			segBytes = shiftSegmentTfdt(segBytes, 1, videoTimeBase)
			pending = append(pending, pendingSeg{
				track:    "video",
				time:     normalisedTime,
				duration: vseg.duration,
				data:     segBytes,
			})
		}

		for _, aseg := range newAudioSegs {
			asegURL := aseg.url
			segBytes, err := retry.DoWithData(
				func() ([]byte, error) { return client.GetBytes(ctx, asegURL) },
				retry.Context(ctx),
				retry.Attempts(3),
				retry.Delay(600*time.Millisecond),
				retry.DelayType(retry.FixedDelay),
			)
			if err != nil {
				fmt.Printf("[WARN] audio seg download failed: %v\n", err)
				continue
			}

			rawTfdt, ok := extractMoofFirstTfdt(segBytes)
			if !audioBaseSet && ok {
				audioTimeBase = rawTfdt
				audioBaseSet = true
				if server.Config.Debug {
					fmt.Printf("[DEBUG] muxed: audio base=%d\n", audioTimeBase)
				}
			}

			normalisedTime := rawTfdt
			if audioBaseSet && rawTfdt >= audioTimeBase {
				normalisedTime = rawTfdt - audioTimeBase
			}
			if server.Config.Debug && ok {
				fmt.Printf("[DEBUG] muxed: audio seg dur=%.3f raw_tfdt=%d norm=%d\n", aseg.duration, rawTfdt, normalisedTime)
			}

			segBytes = rewriteAudioMoofTrackID(segBytes)
			segBytes = shiftSegmentTfdt(segBytes, 2, audioTimeBase)
			pending = append(pending, pendingSeg{
				track:    "audio",
				time:     normalisedTime,
				duration: 0,
				data:     segBytes,
			})
		}

		sort.SliceStable(pending, func(i, j int) bool {
			if pending[i].time != pending[j].time {
				return pending[i].time < pending[j].time
			}
			return pending[i].track < pending[j].track
		})

		for _, seg := range pending {
			if err := handler(seg.data, seg.duration); err != nil {
				return fmt.Errorf("handler muxed segment: %w", err)
			}
		}

		// Update last segment time if we processed any new segments
		if len(newVideoSegs) > 0 || len(newAudioSegs) > 0 {
			lastSegmentTime = time.Now()
		}

		// Check if playlist has gone stale (no new segments for staleTimeout duration)
		if time.Since(lastSegmentTime) > staleTimeout {
			if server.Config.Debug {
				fmt.Printf("[DEBUG] muxed playlist stale: no new segments for %v, stream likely ended\n", staleTimeout)
			}
			return internal.ErrChannelOffline
		}

		<-time.After(1 * time.Second)
	}
}


// truncate returns the first n characters of s, or s if len(s) <= n
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}


