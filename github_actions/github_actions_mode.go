package github_actions

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/HeapOfChaos/goondvr/entity"
	"github.com/urfave/cli/v2"
)

// GitHubActionsMode represents the configuration and components for running in GitHub Actions mode.
// It orchestrates all components needed for continuous recording with auto-restart chain pattern.
type GitHubActionsMode struct {
	// Configuration
	MatrixJobID    string
	SessionID      string
	Channels       []string
	MaxQuality     bool
	CostSavingMode bool
	
	// Components
	ChainManager          *ChainManager
	StatePersister        *StatePersister
	MatrixCoordinator     *MatrixCoordinator
	StorageUploader       *StorageUploader
	SupabaseManager       *SupabaseManager
	DatabaseManager       *DatabaseManager
	QualitySelector       *QualitySelector
	HealthMonitor         *HealthMonitor
	GracefulShutdown      *GracefulShutdown
	StreamFailureRecovery *StreamFailureRecovery
	AdaptivePolling       *AdaptivePolling
	Manager               interface{} // Manager interface for starting recordings (will be set externally)
	
	// Runtime state
	ctx          context.Context
	cancel       context.CancelFunc
	startTime    time.Time
}

// NewGitHubActionsMode creates a new GitHubActionsMode instance with the specified configuration.
// It initializes all components needed for GitHub Actions operation.
func NewGitHubActionsMode(matrixJobID, sessionID string, channels []string, maxQuality, costSavingMode bool) (*GitHubActionsMode, error) {
	ctx, cancel := context.WithCancel(context.Background())
	
	mode := &GitHubActionsMode{
		MatrixJobID:    matrixJobID,
		SessionID:      sessionID,
		Channels:       channels,
		MaxQuality:     maxQuality,
		CostSavingMode: costSavingMode,
		ctx:            ctx,
		cancel:         cancel,
		startTime:      time.Now(),
	}
	
	// Initialize components
	if err := mode.initializeComponents(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize components: %w", err)
	}
	
	return mode, nil
}

// initializeComponents initializes all required components for GitHub Actions mode.
// Requirements: 5.1, 5.2, 5.5, 5.6, 5.8
func (gam *GitHubActionsMode) initializeComponents() error {
	log.Println("Initializing GitHub Actions mode components...")
	
	// Get required environment variables
	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		return fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}
	
	repository := os.Getenv("GITHUB_REPOSITORY")
	if repository == "" {
		return fmt.Errorf("GITHUB_REPOSITORY environment variable is required")
	}
	
	gofileAPIKey := os.Getenv("GOFILE_API_KEY")
	filesterAPIKey := os.Getenv("FILESTER_API_KEY")
	
	// Warn if API keys are not configured, but don't fail
	// Recordings will still work, but uploads will be skipped
	if gofileAPIKey == "" {
		log.Println("⚠️  WARNING: GOFILE_API_KEY not configured - uploads to Gofile will be skipped")
	}
	if filesterAPIKey == "" {
		log.Println("⚠️  WARNING: FILESTER_API_KEY not configured - uploads to Filester will be skipped")
	}
	if gofileAPIKey == "" && filesterAPIKey == "" {
		log.Println("⚠️  WARNING: No upload API keys configured - recordings will be saved locally only")
	}
	
	// Initialize Chain Manager
	workflowFile := "continuous-runner.yml"
	gam.ChainManager = NewChainManager(githubToken, repository, workflowFile)
	if gam.SessionID == "" {
		gam.SessionID = gam.ChainManager.GenerateSessionID()
	} else {
		// Set the ChainManager's sessionID to match the provided sessionID
		gam.ChainManager.sessionID = gam.SessionID
	}
	log.Printf("Chain Manager initialized with session ID: %s", gam.SessionID)
	
	// Initialize State Persister
	cacheBaseDir := "./state"
	gam.StatePersister = NewStatePersister(gam.SessionID, gam.MatrixJobID, cacheBaseDir)
	log.Printf("State Persister initialized with cache base dir: %s", cacheBaseDir)
	
	// Initialize Matrix Coordinator
	gam.MatrixCoordinator = NewMatrixCoordinator(gam.SessionID)
	log.Printf("Matrix Coordinator initialized for session: %s", gam.SessionID)
	
	// Initialize Storage Uploader
	gam.StorageUploader = NewStorageUploader(gofileAPIKey, filesterAPIKey)
	log.Println("Storage Uploader initialized with Gofile and Filester API keys")
	
	// Initialize Supabase Manager (optional)
	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseKey := os.Getenv("SUPABASE_KEY")
	
	if supabaseURL != "" && supabaseKey != "" {
		gam.SupabaseManager = NewSupabaseManager(supabaseURL, supabaseKey)
		log.Println("Supabase Manager initialized")
		
		// Test connection
		if err := gam.SupabaseManager.TestConnection(); err != nil {
			log.Printf("⚠️  WARNING: Supabase connection test failed: %v", err)
			log.Println("⚠️  Supabase integration will be disabled. Recordings will only be saved to JSON database.")
			gam.SupabaseManager = nil
		} else {
			log.Println("✅ Supabase connection test successful")
		}
	} else {
		log.Println("Supabase not configured (SUPABASE_URL or SUPABASE_KEY missing) - skipping Supabase integration")
		gam.SupabaseManager = nil
	}
	
	// Initialize Database Manager
	repoPath := "."
	gam.DatabaseManager = NewDatabaseManager(repoPath)
	log.Printf("Database Manager initialized with repo path: %s", repoPath)
	
	// Initialize Quality Selector
	gam.QualitySelector = NewQualitySelector()
	log.Printf("Quality Selector initialized (preferred: %dp @ %dfps)",
		gam.QualitySelector.GetPreferredResolution(),
		gam.QualitySelector.GetPreferredFramerate())
	
	// Initialize Health Monitor
	statusFilePath := "status.json"
	notifiers := []Notifier{}
	
	// Add Discord notifier if webhook URL is provided
	discordWebhook := os.Getenv("DISCORD_WEBHOOK_URL")
	if discordWebhook != "" {
		notifiers = append(notifiers, NewDiscordNotifier(discordWebhook))
		log.Println("Discord notifier configured")
	}
	
	// Add ntfy notifier if configured
	ntfyServer := os.Getenv("NTFY_SERVER_URL")
	ntfyTopic := os.Getenv("NTFY_TOPIC")
	ntfyToken := os.Getenv("NTFY_TOKEN")
	if ntfyServer != "" && ntfyTopic != "" {
		notifiers = append(notifiers, NewNtfyNotifier(ntfyServer, ntfyTopic, ntfyToken))
		log.Println("Ntfy notifier configured")
	}
	
	gam.HealthMonitor = NewHealthMonitor(statusFilePath, notifiers)
	log.Printf("Health Monitor initialized with %d notifiers", len(notifiers))
	
	// Initialize Graceful Shutdown
	gam.GracefulShutdown = NewGracefulShutdown(
		gam.startTime,
		gam.ChainManager,
		gam.StatePersister,
		gam.StorageUploader,
		gam.MatrixCoordinator,
		gam.MatrixJobID,
		"./conf",        // configDir
		"./videos",      // recordingsDir
	)
	log.Println("Graceful Shutdown initialized")
	
	// Initialize Stream Failure Recovery
	retryInterval := 5 * time.Minute // Default retry interval
	gam.StreamFailureRecovery = NewStreamFailureRecovery(
		gam.HealthMonitor,
		gam.SessionID,
		gam.MatrixJobID,
		retryInterval,
	)
	log.Printf("Stream Failure Recovery initialized with retry interval: %s", retryInterval)
	
	// Initialize Adaptive Polling
	// Get the normal polling interval from server config, default to 1 minute if not set
	normalInterval := 1
	if os.Getenv("POLLING_INTERVAL") != "" {
		// Try to parse from environment variable
		if parsed, err := time.ParseDuration(os.Getenv("POLLING_INTERVAL")); err == nil {
			normalInterval = int(parsed.Minutes())
		}
	}
	
	// Create adaptive polling with cost-saving mode support
	gam.AdaptivePolling = NewAdaptivePollingWithCostSaving(normalInterval, gam.CostSavingMode)
	
	if gam.CostSavingMode {
		log.Printf("Adaptive Polling initialized in COST-SAVING MODE (polling: %d min)",
			gam.AdaptivePolling.GetCostSavingInterval())
	} else {
		log.Printf("Adaptive Polling initialized (normal: %d min, reduced: %d min)",
			gam.AdaptivePolling.GetNormalInterval(),
			gam.AdaptivePolling.GetReducedInterval())
	}
	
	log.Println("All components initialized successfully")
	return nil
}

// GetContext returns the context for this GitHub Actions mode instance.
func (gam *GitHubActionsMode) GetContext() context.Context {
	return gam.ctx
}

// Cancel cancels the context for this GitHub Actions mode instance.
func (gam *GitHubActionsMode) Cancel() {
	gam.cancel()
}

// GetStartTime returns the start time of this GitHub Actions mode instance.
func (gam *GitHubActionsMode) GetStartTime() time.Time {
	return gam.startTime
}

// AddGitHubActionsModeFlags adds command-line flags for GitHub Actions mode to the CLI app.
// This should be called when setting up the CLI application to add the necessary flags.
//
// Requirements: 5.1, 5.2, 5.5, 5.6, 5.8, 10.6, 12.5
func AddGitHubActionsModeFlags(app *cli.App) {
	app.Flags = append(app.Flags,
		&cli.StringFlag{
			Name:  "mode",
			Usage: "Operation mode: 'normal' or 'github-actions'",
			Value: "normal",
		},
		&cli.StringFlag{
			Name:  "matrix-job-id",
			Usage: "Matrix job identifier (required for github-actions mode)",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "session-id",
			Usage: "Session identifier for workflow run (auto-generated if not provided)",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "channels",
			Usage: "Comma-separated list of channels to record (required for github-actions mode)",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  "max-quality",
			Usage: "Enable maximum quality recording (4K 60fps with fallback) - DEPRECATED: Maximum quality is now enabled by default",
			Value: true, // Changed to true - maximum quality is now the default
		},
		&cli.BoolFlag{
			Name:  "cost-saving",
			Usage: "Enable cost-saving mode (10-minute polling, limit to 2 concurrent channels)",
			Value: false,
		},
		&cli.BoolFlag{
			Name:  "validate-setup",
			Usage: "Validate configuration and secrets without starting recordings",
			Value: false,
		},
	)
}

// ParseGitHubActionsModeConfig parses command-line flags and environment variables
// to create a GitHubActionsMode configuration. It validates required parameters
// and returns an error if any are missing.
//
// Requirements: 5.1, 5.2, 5.5, 5.6, 5.8, 5.9, 5.11
func ParseGitHubActionsModeConfig(c *cli.Context) (*GitHubActionsMode, error) {
	// Check if we're in GitHub Actions mode
	mode := c.String("mode")
	if mode != "github-actions" {
		return nil, fmt.Errorf("not in github-actions mode")
	}
	
	// Check if we're in validate-setup mode
	if c.Bool("validate-setup") {
		return nil, ValidateSetupMode(c)
	}
	
	// Parse matrix job ID
	matrixJobID := c.String("matrix-job-id")
	if matrixJobID == "" {
		// Try to get from environment variable
		matrixJobID = os.Getenv("MATRIX_JOB_ID")
		if matrixJobID == "" {
			return nil, fmt.Errorf("--matrix-job-id flag or MATRIX_JOB_ID environment variable is required")
		}
	}
	
	// Parse session ID (optional, will be auto-generated if not provided)
	sessionID := c.String("session-id")
	if sessionID == "" {
		sessionID = os.Getenv("SESSION_ID")
	}
	
	// Parse channels
	channelsStr := c.String("channels")
	if channelsStr == "" {
		// Try to get from environment variable
		channelsStr = os.Getenv("CHANNELS")
		if channelsStr == "" {
			return nil, fmt.Errorf("--channels flag or CHANNELS environment variable is required")
		}
	}
	
	// Split channels by comma and trim whitespace
	channels := []string{}
	for _, ch := range strings.Split(channelsStr, ",") {
		ch = strings.TrimSpace(ch)
		if ch != "" {
			channels = append(channels, ch)
		}
	}
	
	// Parse matrix job count from environment variable (set by workflow)
	matrixJobCountStr := os.Getenv("MATRIX_JOB_COUNT")
	matrixJobCount := len(channels) // Default to number of channels if not set
	if matrixJobCountStr != "" {
		validator := NewConfigValidator()
		parsed, err := validator.ParseMatrixJobCount(matrixJobCountStr)
		if err != nil {
			return nil, fmt.Errorf("invalid matrix_job_count: %w", err)
		}
		matrixJobCount = parsed
	}
	
	// Validate workflow inputs using ConfigValidator
	log.Println("Validating workflow configuration inputs...")
	validator := NewConfigValidator()
	validationResult := validator.ValidateWorkflowInputs(channels, matrixJobCount)
	if !validationResult.Valid {
		log.Println("Configuration validation failed:")
		for _, err := range validationResult.Errors {
			log.Printf("  - %s", err)
		}
		return nil, fmt.Errorf("configuration validation failed: %d errors found", len(validationResult.Errors))
	}
	log.Println("Configuration validation passed")
	
	// Parse max quality flag (kept for backwards compatibility, but always true now)
	maxQuality := true // Maximum quality is now ALWAYS enabled by default
	
	// Parse cost-saving flag
	costSavingMode := c.Bool("cost-saving")
	
	// Log cost-saving mode status
	if costSavingMode {
		log.Println("Cost-saving mode ENABLED: polling frequency will be 10 minutes, concurrent recordings limited to 2 channels")
	}
	
	// Create and initialize GitHubActionsMode
	log.Printf("Creating GitHub Actions mode with matrix job ID: %s, channels: %v, max quality: ENABLED (default), cost-saving: %v",
		matrixJobID, channels, costSavingMode)
	
	gam, err := NewGitHubActionsMode(matrixJobID, sessionID, channels, maxQuality, costSavingMode)
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub Actions mode: %w", err)
	}
	
	return gam, nil
}

// ValidateSetupMode performs comprehensive validation of all required secrets and configuration
// for GitHub Actions mode without starting recordings. This is used by the --validate-setup flag.
//
// It validates:
// - All required environment variables (GITHUB_TOKEN, GITHUB_REPOSITORY, GOFILE_API_KEY, FILESTER_API_KEY)
// - Channels list is not empty
// - Matrix job count is within valid range (1-20)
// - Optional notification configuration (Discord, ntfy)
//
// Returns an error if validation fails, nil if all checks pass.
//
// Requirements: 10.6
func ValidateSetupMode(c *cli.Context) error {
	log.Println("=== GitHub Actions Setup Validation ===")
	log.Println()
	
	// Parse channels
	channelsStr := c.String("channels")
	if channelsStr == "" {
		channelsStr = os.Getenv("CHANNELS")
	}
	
	// Split channels by comma and trim whitespace
	channels := []string{}
	for _, ch := range strings.Split(channelsStr, ",") {
		ch = strings.TrimSpace(ch)
		if ch != "" {
			channels = append(channels, ch)
		}
	}
	
	// Parse matrix job count from environment variable (set by workflow)
	matrixJobCountStr := os.Getenv("MATRIX_JOB_COUNT")
	matrixJobCount := len(channels) // Default to number of channels if not set
	if matrixJobCountStr != "" {
		validator := NewConfigValidator()
		parsed, err := validator.ParseMatrixJobCount(matrixJobCountStr)
		if err != nil {
			log.Printf("❌ Invalid MATRIX_JOB_COUNT: %v", err)
			matrixJobCount = 0 // Set to invalid value to trigger validation error
		} else {
			matrixJobCount = parsed
		}
	}
	
	// Perform comprehensive validation
	validator := NewConfigValidator()
	validationResult := validator.ValidateSetup(channels, matrixJobCount)
	
	// Display validation results
	log.Println("Validation Results:")
	log.Println("-------------------")
	
	if validationResult.Valid {
		log.Println("✅ All validation checks passed!")
		log.Println()
		log.Println("Configuration Summary:")
		log.Printf("  - Channels: %v", channels)
		log.Printf("  - Matrix Job Count: %d", matrixJobCount)
		log.Printf("  - GITHUB_TOKEN: %s", maskSecret(os.Getenv("GITHUB_TOKEN")))
		log.Printf("  - GITHUB_REPOSITORY: %s", os.Getenv("GITHUB_REPOSITORY"))
		log.Printf("  - GOFILE_API_KEY: %s", maskSecret(os.Getenv("GOFILE_API_KEY")))
		log.Printf("  - FILESTER_API_KEY: %s", maskSecret(os.Getenv("FILESTER_API_KEY")))
		
		// Display optional notification configuration
		discordWebhook := os.Getenv("DISCORD_WEBHOOK_URL")
		ntfyServer := os.Getenv("NTFY_SERVER_URL")
		ntfyTopic := os.Getenv("NTFY_TOPIC")
		
		log.Println()
		log.Println("Optional Notification Configuration:")
		if discordWebhook != "" {
			log.Printf("  - Discord Webhook: %s", maskSecret(discordWebhook))
		} else {
			log.Println("  - Discord Webhook: Not configured")
		}
		
		if ntfyServer != "" && ntfyTopic != "" {
			log.Printf("  - Ntfy Server: %s", ntfyServer)
			log.Printf("  - Ntfy Topic: %s", ntfyTopic)
			ntfyToken := os.Getenv("NTFY_TOKEN")
			if ntfyToken != "" {
				log.Printf("  - Ntfy Token: %s", maskSecret(ntfyToken))
			} else {
				log.Println("  - Ntfy Token: Not configured (optional)")
			}
		} else {
			log.Println("  - Ntfy: Not configured")
		}
		
		log.Println()
		log.Println("✅ Setup validation completed successfully!")
		log.Println("You can now run the workflow without --validate-setup to start recordings.")
		return nil
	}
	
	// Validation failed - display all errors
	log.Printf("❌ Validation failed with %d error(s):", len(validationResult.Errors))
	log.Println()
	for i, err := range validationResult.Errors {
		log.Printf("  %d. %s", i+1, err)
	}
	log.Println()
	log.Println("Please fix the above errors before running the workflow.")
	
	return fmt.Errorf("setup validation failed: %d errors found", len(validationResult.Errors))
}

// maskSecret masks a secret string for display, showing only the first 4 and last 4 characters.
// If the secret is empty or too short, it returns appropriate placeholder text.
func maskSecret(secret string) string {
	if secret == "" {
		return "<not set>"
	}
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "****" + secret[len(secret)-4:]
}

// ApplyQualityToChannelConfig applies the maximum quality settings to a channel configuration.
// This is a helper method that uses the QualitySelector to determine and apply the best quality.
//
// This method ALWAYS applies maximum quality settings (enabled by default):
// 1. Determines the best available quality using the quality selector
// 2. Overrides any existing resolution and framerate settings in the channel config
// 3. Logs the actual quality being applied
//
// The quality selector uses a fallback chain to prioritize the highest available quality
// up to 4K 60fps: 2160p60 → 1080p60 → 720p60 → highest available
//
// Requirements: 16.1, 16.2, 16.8, 16.10
func (gam *GitHubActionsMode) ApplyQualityToChannelConfig(config *entity.ChannelConfig) error {
	// Maximum quality is now ALWAYS enabled by default
	// The --max-quality flag is kept for backwards compatibility but has no effect
	
	// For now, we'll use a default set of available qualities
	// In a real implementation, this would detect qualities from the stream
	// using DetectAvailableQualities() when the stream URL is available
	availableQualities := []Quality{
		{Resolution: 2160, Framerate: 60},
		{Resolution: 1080, Framerate: 60},
		{Resolution: 720, Framerate: 60},
		{Resolution: 720, Framerate: 30},
	}
	
	// Select the best quality using the quality selector's fallback chain
	// Priority: 2160p60 → 1080p60 → 720p60 → highest available
	settings := gam.QualitySelector.SelectQuality(availableQualities)
	
	// Apply the quality settings to the channel config
	// This overrides any existing resolution and framerate settings (Requirement 16.10)
	gam.QualitySelector.ApplyQualitySettings(config, settings)
	
	log.Printf("Applied maximum quality settings to channel %s: %s (resolution: %dp, framerate: %dfps)", 
		config.Username, settings.Actual, settings.Resolution, settings.Framerate)
	return nil
}

// CreateChannelConfigWithQuality creates a channel configuration with quality settings applied.
// This method creates a base configuration and then applies maximum quality settings.
//
// Maximum quality is ALWAYS enabled by default, prioritizing the highest available quality
// up to 4K 60fps using the fallback chain: 2160p60 → 1080p60 → 720p60 → highest available
//
// The base configuration includes:
// - Username and site from parameters
// - Default pattern for file naming
// - Default max duration and filesize limits
// - Initial resolution and framerate (will be overridden by quality selector)
//
// Requirements: 16.1, 16.2, 16.8, 16.10
func (gam *GitHubActionsMode) CreateChannelConfigWithQuality(username, site string) (*entity.ChannelConfig, error) {
	// Create base channel configuration with default settings
	config := &entity.ChannelConfig{
		IsPaused:    false,
		Username:    username,
		Site:        site,
		Framerate:   30,  // Default framerate (will be overridden by quality selector)
		Resolution:  1080, // Default resolution (will be overridden by quality selector)
		Pattern:     "videos/{{if ne .Site \"chaturbate\"}}{{.Site}}/{{end}}{{.Username}}_{{.Year}}-{{.Month}}-{{.Day}}_{{.Hour}}-{{.Minute}}-{{.Second}}{{if .Sequence}}_{{.Sequence}}{{end}}",
		MaxDuration: 0, // CHANGED: Disable file splitting - let recordings run continuously
		MaxFilesize: 0,  // No filesize limit by default
	}
	
	// Apply maximum quality settings (ALWAYS enabled by default)
	// This will override the default resolution and framerate with the highest available
	if err := gam.ApplyQualityToChannelConfig(config); err != nil {
		return nil, fmt.Errorf("failed to apply quality settings: %w", err)
	}
	
	return config, nil
}

// GetAssignedChannel returns the channel assigned to this matrix job.
// Each matrix job handles exactly one channel.
func (gam *GitHubActionsMode) GetAssignedChannel() (string, error) {
	// In the matrix strategy, each job gets one channel
	// The matrix job ID determines which channel this job handles
	// Matrix job IDs can be in format "matrix-job-N" or just "N" where N is 1-indexed
	
	var jobIndex int
	var err error
	
	// Try parsing as "matrix-job-N" format first
	_, err = fmt.Sscanf(gam.MatrixJobID, "matrix-job-%d", &jobIndex)
	if err != nil {
		// If that fails, try parsing as just a number
		_, err = fmt.Sscanf(gam.MatrixJobID, "%d", &jobIndex)
		if err != nil {
			return "", fmt.Errorf("invalid matrix job ID format: %s (expected 'matrix-job-N' or 'N')", gam.MatrixJobID)
		}
	}
	
	// Convert to 0-indexed
	jobIndex--
	
	if jobIndex < 0 || jobIndex >= len(gam.Channels) {
		return "", fmt.Errorf("matrix job index %d out of range for %d channels", jobIndex+1, len(gam.Channels))
	}
	
	return gam.Channels[jobIndex], nil
}

// ParseChannelString parses a channel string in the format "site:username" or just "username".
// If no site prefix is provided, defaults to "chaturbate".
//
// Supported formats:
//   - "chaturbate:username" -> site="chaturbate", username="username"
//   - "stripchat:username" -> site="stripchat", username="username"
//   - "username" -> site="chaturbate", username="username" (default)
//
// Returns:
//   - site: The site name (chaturbate, stripchat, etc.)
//   - username: The channel username
//   - error: Error if the format is invalid
func ParseChannelString(channelStr string) (site string, username string, err error) {
	channelStr = strings.TrimSpace(channelStr)
	if channelStr == "" {
		return "", "", fmt.Errorf("channel string is empty")
	}
	
	// Check if the channel string contains a site prefix (site:username)
	parts := strings.SplitN(channelStr, ":", 2)
	
	if len(parts) == 2 {
		// Format: site:username
		site = strings.TrimSpace(strings.ToLower(parts[0]))
		username = strings.TrimSpace(parts[1])
		
		// Validate site
		validSites := map[string]bool{
			"chaturbate": true,
			"stripchat":  true,
		}
		
		if !validSites[site] {
			return "", "", fmt.Errorf("unsupported site: %s (supported: chaturbate, stripchat)", site)
		}
		
		if username == "" {
			return "", "", fmt.Errorf("username is empty for site: %s", site)
		}
		
		return site, username, nil
	}
	
	// Format: username (no site prefix, default to chaturbate)
	username = channelStr
	site = "chaturbate"
	
	return site, username, nil
}

// GetAssignedChannelWithSite returns the channel and site assigned to this matrix job.
// It parses the channel string to extract the site and username.
//
// Returns:
//   - site: The site name (chaturbate, stripchat, etc.)
//   - username: The channel username
//   - error: Error if parsing fails
func (gam *GitHubActionsMode) GetAssignedChannelWithSite() (site string, username string, err error) {
	channelStr, err := gam.GetAssignedChannel()
	if err != nil {
		return "", "", err
	}
	
	return ParseChannelString(channelStr)
}

// GetActiveRecordingsCount returns the count of currently active recordings.
// This is used by the adaptive polling monitor to determine if the polling interval
// should be reduced or restored to normal.
//
// In the GitHub Actions mode, each matrix job handles exactly one channel,
// so this method checks if that channel is currently recording.
//
// Returns:
//   - int: Number of active recordings (0 or 1 for a single matrix job)
//
// Requirements: 9.1
func (gam *GitHubActionsMode) GetActiveRecordingsCount() int {
	// TODO: Implement actual logic to check if the assigned channel is recording
	// This would require integration with the channel manager or recording state
	// For now, we return 0 as a placeholder
	
	// In a real implementation, this would:
	// 1. Get the assigned channel for this matrix job
	// 2. Check if that channel is currently online and recording
	// 3. Return 1 if recording, 0 if not
	
	return 0
}

// ShouldLimitConcurrentRecordings returns whether concurrent recordings should be limited
// based on cost-saving mode. When cost-saving mode is enabled, only 2 concurrent recordings
// are allowed across all matrix jobs.
//
// Returns:
//   - bool: true if concurrent recordings should be limited, false otherwise
//
// Requirements: 12.5, 12.7
func (gam *GitHubActionsMode) ShouldLimitConcurrentRecordings() bool {
	return gam.CostSavingMode
}

// GetMaxConcurrentRecordings returns the maximum number of concurrent recordings allowed.
// In cost-saving mode, this is limited to 2 channels. Otherwise, it returns the total
// number of channels (no limit).
//
// Returns:
//   - int: Maximum number of concurrent recordings allowed
//
// Requirements: 12.7
func (gam *GitHubActionsMode) GetMaxConcurrentRecordings() int {
	if gam.CostSavingMode {
		return 2 // Limit to 2 concurrent recordings in cost-saving mode
	}
	return len(gam.Channels) // No limit in normal mode
}

// IsCostSavingMode returns whether cost-saving mode is enabled.
//
// Returns:
//   - bool: true if cost-saving mode is enabled, false otherwise
//
// Requirements: 12.5
func (gam *GitHubActionsMode) IsCostSavingMode() bool {
	return gam.CostSavingMode
}

// StartWorkflowLifecycle implements the workflow lifecycle management for GitHub Actions mode.
// It performs the following operations:
// 1. Restores state from cache on startup
// 2. Creates and starts recording for the assigned channel
// 3. Starts Chain Manager runtime monitoring in background goroutine
// 4. Starts Health Monitor disk space monitoring in background goroutine
// 5. Starts Graceful Shutdown monitoring in background goroutine
// 6. Registers matrix job with Matrix Coordinator
//
// This method should be called after initializing GitHubActionsMode to start the workflow.
// It returns an error if any critical initialization step fails.
//
// Parameters:
//   - configDir: Directory for configuration files
//   - recordingsDir: Directory for recordings
//   - manager: Manager instance for starting recordings (must implement CreateChannel method)
//
// Requirements: 2.1, 4.1, 7.1, 13.4, 17.9
func (gam *GitHubActionsMode) StartWorkflowLifecycle(configDir, recordingsDir string, manager interface{}) error {
	log.Println("Starting workflow lifecycle management...")
	
	// Step 1: Restore state from cache on startup
	log.Println("Restoring state from cache...")
	err := gam.StatePersister.RestoreState(gam.ctx, configDir, recordingsDir)
	if err != nil {
		if IsCacheMiss(err) {
			log.Println("Cache miss detected (expected for first run), initializing with default state")
			// This is expected for the first workflow run - continue with default state
		} else {
			log.Printf("Warning: cache restoration failed: %v", err)
			log.Println("Continuing with default state")
			// Continue operation even if cache restoration fails (Requirement 2.7)
		}
	} else {
		log.Println("State restored successfully from cache")
	}

	// Step 1.5: Sync database before starting new recording
	log.Println("Syncing database before starting new recording...")
	if err := gam.DatabaseManager.SyncDatabase(); err != nil {
		log.Printf("Warning: database sync failed: %v", err)
		log.Println("Continuing with workflow startup - database will sync on first write")
		// Continue anyway - this is not critical, the AtomicUpdate will handle conflicts
	} else {
		log.Println("Database synced successfully with remote repository")
	}
	
	// Step 2: Get assigned channel and create channel configuration
	log.Printf("Getting assigned channel for matrix job %s...", gam.MatrixJobID)
	assignedChannel, err := gam.GetAssignedChannel()
	if err != nil {
		return fmt.Errorf("failed to get assigned channel: %w", err)
	}
	
	// Parse the channel to get site and username
	site, username, err := ParseChannelString(assignedChannel)
	if err != nil {
		return fmt.Errorf("failed to parse channel string '%s': %w", assignedChannel, err)
	}
	
	log.Printf("Matrix job %s assigned to channel: %s (site: %s, username: %s)", 
		gam.MatrixJobID, assignedChannel, site, username)
	
	// Create channel configuration with quality settings
	log.Printf("Creating channel configuration with maximum quality for %s...", username)
	channelConfig, err := gam.CreateChannelConfigWithQuality(username, site)
	if err != nil {
		return fmt.Errorf("failed to create channel configuration: %w", err)
	}
	
	log.Printf("Channel configuration created: site=%s, username=%s, resolution=%dp, framerate=%dfps",
		channelConfig.Site, channelConfig.Username, channelConfig.Resolution, channelConfig.Framerate)
	
	// Step 3: Register matrix job with Matrix Coordinator
	log.Printf("Registering matrix job %s with Matrix Coordinator...", gam.MatrixJobID)
	err = gam.MatrixCoordinator.RegisterJob(gam.MatrixJobID, assignedChannel)
	if err != nil {
		return fmt.Errorf("failed to register matrix job: %w", err)
	}
	log.Printf("Matrix job %s registered successfully", gam.MatrixJobID)
	
	// Step 4: Start recording for the assigned channel using the Manager
	log.Printf("Starting recording for channel %s (site: %s)...", username, site)
	
	// Store the manager reference
	gam.Manager = manager
	
	// Type assert to get the CreateChannel method
	type ChannelCreator interface {
		CreateChannel(conf *entity.ChannelConfig, shouldSave bool) error
	}
	
	if mgr, ok := manager.(ChannelCreator); ok {
		// Start recording - don't save to config file (shouldSave = false)
		// because GitHub Actions mode manages channels dynamically
		if err := mgr.CreateChannel(channelConfig, false); err != nil {
			return fmt.Errorf("failed to start recording for channel %s: %w", username, err)
		}
		log.Printf("✅ Successfully started recording for channel %s", username)
		log.Printf("   - Site: %s", channelConfig.Site)
		log.Printf("   - Resolution: %dp", channelConfig.Resolution)
		log.Printf("   - Framerate: %dfps", channelConfig.Framerate)
		log.Printf("   - Pattern: %s", channelConfig.Pattern)
	} else {
		return fmt.Errorf("manager does not implement CreateChannel method")
	}
	
	// Step 5: Start Chain Manager runtime monitoring in background goroutine
	log.Println("Starting Chain Manager runtime monitoring in background...")
	go func() {
		// Create a state provider function that returns current session state
		stateProvider := func() SessionState {
			return SessionState{
				SessionID:         gam.SessionID,
				StartTime:         gam.startTime,
				Channels:          gam.Channels,
				PartialRecordings: []PartialRecording{}, // TODO: populate with actual partial recordings
				Configuration:     make(map[string]interface{}),
				MatrixJobCount:    len(gam.Channels),
			}
		}
		
		err := gam.ChainManager.MonitorRuntime(gam.ctx, stateProvider)
		if err != nil && err != context.Canceled {
			log.Printf("Chain Manager monitoring error: %v", err)
		} else {
			log.Println("Chain Manager monitoring completed successfully")
		}
	}()
	log.Println("Chain Manager runtime monitoring started")
	
	// Step 6: Start Health Monitor disk space monitoring in background goroutine
	log.Println("Starting Health Monitor disk space monitoring in background...")
	go func() {
		// For now, we'll use the recordings directory for disk monitoring
		// In a real implementation, this would be configurable
		monitoringDir := recordingsDir
		if monitoringDir == "" {
			monitoringDir = "./videos" // Default recordings directory
		}
		
		// Create a function to stop the oldest recording (placeholder for now)
		stopOldestRecordingFunc := func() error {
			log.Println("Stop oldest recording requested by Health Monitor")
			// TODO: implement actual recording stop logic
			return nil
		}
		
		err := gam.HealthMonitor.MonitorDiskSpace(gam.ctx, monitoringDir, gam.StorageUploader, stopOldestRecordingFunc)
		if err != nil && err != context.Canceled {
			log.Printf("Health Monitor disk space monitoring error: %v", err)
		} else {
			log.Println("Health Monitor disk space monitoring completed")
		}
	}()
	log.Println("Health Monitor disk space monitoring started")
	
	// Step 7: Start Graceful Shutdown monitoring in background goroutine
	log.Println("Starting Graceful Shutdown monitoring in background...")
	go func() {
		config := DefaultShutdownConfig()
		err := gam.GracefulShutdown.MonitorAndShutdown(gam.ctx, config)
		if err != nil && err != context.Canceled {
			log.Printf("Graceful Shutdown monitoring error: %v", err)
		} else {
			log.Println("Graceful Shutdown monitoring completed successfully")
		}
	}()
	log.Println("Graceful Shutdown monitoring started")
	
	// Step 8: Start Adaptive Polling monitoring in background goroutine
	log.Println("Starting Adaptive Polling monitoring in background...")
	go func() {
		// Use the GitHubActionsMode's GetActiveRecordingsCount method
		err := gam.AdaptivePolling.MonitorAndAdjust(gam.ctx, gam.GetActiveRecordingsCount)
		if err != nil && err != context.Canceled {
			log.Printf("Adaptive Polling monitoring error: %v", err)
		} else {
			log.Println("Adaptive Polling monitoring completed")
		}
	}()
	log.Println("Adaptive Polling monitoring started")
	
	// Send workflow start notification
	err = gam.HealthMonitor.SendNotification(
		"Workflow Started",
		fmt.Sprintf("Matrix job %s started for channel %s (Session: %s)",
			gam.MatrixJobID, assignedChannel, gam.SessionID),
	)
	if err != nil {
		log.Printf("Warning: failed to send workflow start notification: %v", err)
		// Continue even if notification fails
	}
	
	log.Println("Workflow lifecycle management started successfully")
	return nil
}

// UploadCompletedRecordings scans the recordings directory for completed files
// and uploads them to external storage. This is used during emergency shutdown
// when the workflow is cancelled to ensure recordings are not lost.
//
// Parameters:
//   - ctx: Context for cancellation and timeout control
//   - recordingsDir: Directory containing completed recordings
//
// Returns an error if the upload process fails critically.
//
// Requirements: 3.1, 3.7, 14.1
func (gam *GitHubActionsMode) UploadCompletedRecordings(ctx context.Context, recordingsDir string) error {
	log.Printf("[UploadCompletedRecordings] Scanning %s for completed recordings...", recordingsDir)
	
	// Scan the recordings directory for video files
	entries, err := os.ReadDir(recordingsDir)
	if err != nil {
		return fmt.Errorf("failed to read recordings directory: %w", err)
	}
	
	uploadCount := 0
	errorCount := 0
	
	for _, entry := range entries {
		// Skip directories
		if entry.IsDir() {
			continue
		}
		
		// Only process video files (.ts, .mp4, .mkv)
		name := entry.Name()
		if !isVideoFile(name) {
			continue
		}
		
		filePath := recordingsDir + "/" + name
		
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			log.Printf("[UploadCompletedRecordings] Upload cancelled by context after %d files", uploadCount)
			return ctx.Err()
		default:
		}
		
		// Check if this file was already uploaded by checking Supabase
		// Extract file info to check against database
		fileInfo, err := entry.Info()
		if err != nil {
			log.Printf("[UploadCompletedRecordings] Failed to get file info for %s: %v", name, err)
			errorCount++
			continue
		}
		
		// Check if recording already exists in Supabase by file size and approximate timestamp
		// This prevents duplicate uploads during emergency shutdown
		if gam.SupabaseManager != nil {
			log.Printf("[UploadCompletedRecordings] Checking if %s was already uploaded (size: %d bytes)...", name, fileInfo.Size())
			
			// Get file modification time as a proxy for recording time
			modTime := fileInfo.ModTime()
			date := modTime.Format("2006-01-02")
			
			// Check for existing recording with similar file size (1% tolerance)
			existingRec, err := gam.SupabaseManager.CheckRecordingExists(date, fileInfo.Size(), 1)
			if err != nil {
				log.Printf("[UploadCompletedRecordings] Warning: Failed to check for duplicates in Supabase: %v", err)
				// Continue with upload if we can't check - better to have duplicates than lose recordings
			} else if existingRec != nil {
				log.Printf("[UploadCompletedRecordings] SKIPPING %s - already uploaded (Supabase ID: %s, size: %d bytes)", name, existingRec.ID, existingRec.FileSizeBytes)
				
				// Delete the local file since it's already uploaded
				if err := os.Remove(filePath); err != nil {
					log.Printf("[UploadCompletedRecordings] Warning: Failed to delete duplicate file %s: %v", filePath, err)
				} else {
					log.Printf("[UploadCompletedRecordings] Deleted duplicate local file: %s", filePath)
				}
				continue
			}
		}
		
		// Upload the file
		log.Printf("[UploadCompletedRecordings] Uploading %s...", name)
		uploadResult, err := gam.StorageUploader.UploadRecording(ctx, filePath)
		if err != nil {
			log.Printf("[UploadCompletedRecordings] Failed to upload %s: %v", name, err)
			errorCount++
			continue
		}
		
		log.Printf("[UploadCompletedRecordings] Successfully uploaded %s", name)
		log.Printf("  - Gofile URL: %s", uploadResult.GofileURL)
		log.Printf("  - Filester URL: %s", uploadResult.FilesterURL)
		
		// Add recording metadata to Supabase (if available)
		if gam.SupabaseManager != nil {
			// Parse filename to extract metadata
			// Filename format: username_YYYY-MM-DD_HH-MM-SS[_sequence][_quality].ext
			// For emergency uploads, we may not have all metadata, so we'll use file info
			modTime := fileInfo.ModTime()
			
			supabaseRecording := SupabaseRecording{
				Site:           "unknown", // Can't determine from filename alone
				Channel:        "unknown", // Can't determine from filename alone
				Timestamp:      modTime,
				Date:           modTime.Format("2006-01-02"),
				DurationSec:    0, // Unknown for emergency uploads
				FileSizeBytes:  fileInfo.Size(),
				Quality:        "unknown",
				GofileURL:      uploadResult.GofileURL,
				FilesterURL:    uploadResult.FilesterURL,
				FilesterChunks: uploadResult.FilesterChunks,
				SessionID:      gam.SessionID,
				MatrixJob:      gam.MatrixJobID,
			}
			
			insertedRecord, err := gam.SupabaseManager.InsertRecording(supabaseRecording)
			if err != nil {
				log.Printf("[UploadCompletedRecordings] Warning: Failed to add recording to Supabase: %v", err)
				// Continue even if Supabase insert fails - recording is uploaded
			} else {
				log.Printf("[UploadCompletedRecordings] Recording added to Supabase (ID: %s)", insertedRecord.ID)
			}
		}
		
		uploadCount++
		
		// Delete the local file after successful upload
		if err := os.Remove(filePath); err != nil {
			log.Printf("[UploadCompletedRecordings] Warning: Failed to delete local file %s: %v", filePath, err)
		} else {
			log.Printf("[UploadCompletedRecordings] Deleted local file: %s", filePath)
		}
	}
	
	log.Printf("[UploadCompletedRecordings] Upload complete: %d successful, %d failed", uploadCount, errorCount)
	
	if errorCount > 0 {
		return fmt.Errorf("failed to upload %d file(s)", errorCount)
	}
	
	return nil
}

// isVideoFile checks if a filename has a video file extension
func isVideoFile(filename string) bool {
	// Check for common video file extensions
	extensions := []string{".ts", ".mp4", ".mkv", ".flv", ".avi", ".mov", ".webm"}
	for _, ext := range extensions {
		if len(filename) >= len(ext) && filename[len(filename)-len(ext):] == ext {
			return true
		}
	}
	return false
}
