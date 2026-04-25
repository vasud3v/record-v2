package github_actions

import (
	"fmt"
	"os"
	"strconv"
)

// ConfigValidator validates workflow configuration inputs.
// It ensures all required parameters are present and within valid ranges
// before the workflow begins operation.
//
// Requirements: 5.9, 5.11
type ConfigValidator struct{}

// NewConfigValidator creates a new ConfigValidator instance.
func NewConfigValidator() *ConfigValidator {
	return &ConfigValidator{}
}

// ValidationResult contains the results of configuration validation.
type ValidationResult struct {
	Valid  bool
	Errors []string
}

// AddError adds an error message to the validation result.
func (vr *ValidationResult) AddError(err string) {
	vr.Valid = false
	vr.Errors = append(vr.Errors, err)
}

// ValidateWorkflowInputs validates all workflow configuration inputs.
// It checks:
// - Channels list is not empty
// - Matrix job count is between 1 and 20
// - Gofile API key is present (warning only, not required)
// - Filester API key is present (warning only, not required)
// - Polling interval is positive (when implemented)
//
// Returns a ValidationResult with any errors found.
//
// Requirements: 5.9, 5.11
func (cv *ConfigValidator) ValidateWorkflowInputs(channels []string, matrixJobCount int) *ValidationResult {
	result := &ValidationResult{
		Valid:  true,
		Errors: []string{},
	}

	// Validate channels list is not empty
	if len(channels) == 0 {
		result.AddError("channels list cannot be empty")
	}

	// Validate matrix_job_count is between 1 and 20
	if matrixJobCount < 1 {
		result.AddError(fmt.Sprintf("matrix_job_count must be at least 1, got %d", matrixJobCount))
	}
	if matrixJobCount > 20 {
		result.AddError(fmt.Sprintf("matrix_job_count cannot exceed 20 (GitHub Actions limit), got %d", matrixJobCount))
	}

	// Note: API keys are now optional - warnings are logged during initialization
	// Recordings will work locally even without upload capabilities

	// Note: Polling interval validation will be added when polling interval
	// is implemented as a workflow input (Requirement 5.5)

	return result
}

// ValidateEnvironmentVariables validates that all required environment variables are present.
// This is a helper method that can be called early in the workflow to fail fast.
// Note: API keys (GOFILE_API_KEY, FILESTER_API_KEY) are now optional.
//
// Requirements: 5.9, 5.11
func (cv *ConfigValidator) ValidateEnvironmentVariables() *ValidationResult {
	result := &ValidationResult{
		Valid:  true,
		Errors: []string{},
	}

	// Check required environment variables (only GitHub-related ones are truly required)
	requiredEnvVars := map[string]string{
		"GITHUB_TOKEN":      "GitHub API authentication token",
		"GITHUB_REPOSITORY": "GitHub repository identifier",
	}

	for envVar, description := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			result.AddError(fmt.Sprintf("%s environment variable is required (%s)", envVar, description))
		}
	}
	
	// Note: GOFILE_API_KEY and FILESTER_API_KEY are now optional
	// Warnings are logged during initialization if they're missing

	return result
}

// ParseMatrixJobCount parses the matrix job count from a string input.
// It validates that the value is a valid integer and returns an error if not.
//
// Requirements: 5.9
func (cv *ConfigValidator) ParseMatrixJobCount(matrixJobCountStr string) (int, error) {
	if matrixJobCountStr == "" {
		return 0, fmt.Errorf("matrix_job_count cannot be empty")
	}

	matrixJobCount, err := strconv.Atoi(matrixJobCountStr)
	if err != nil {
		return 0, fmt.Errorf("matrix_job_count must be a valid integer, got '%s': %w", matrixJobCountStr, err)
	}

	return matrixJobCount, nil
}

// ValidatePollingInterval validates that the polling interval is positive.
// This method is a placeholder for when polling interval is implemented as a workflow input.
//
// Requirements: 5.5, 5.9
func (cv *ConfigValidator) ValidatePollingInterval(pollingInterval int) error {
	if pollingInterval <= 0 {
		return fmt.Errorf("polling interval must be positive, got %d", pollingInterval)
	}
	return nil
}

// ValidateSetup performs a comprehensive validation of all required secrets and configuration
// for GitHub Actions mode. This is used by the --validate-setup flag to check the environment
// before starting recordings.
//
// It validates:
// - All required environment variables (GITHUB_TOKEN, GITHUB_REPOSITORY, GOFILE_API_KEY, FILESTER_API_KEY)
// - Channels list is not empty
// - Matrix job count is within valid range (1-20)
// - Optional notification configuration (Discord, ntfy)
//
// Returns a ValidationResult with all validation errors found.
//
// Requirements: 10.6
func (cv *ConfigValidator) ValidateSetup(channels []string, matrixJobCount int) *ValidationResult {
	result := &ValidationResult{
		Valid:  true,
		Errors: []string{},
	}

	// Validate all required environment variables
	envResult := cv.ValidateEnvironmentVariables()
	if !envResult.Valid {
		result.Valid = false
		result.Errors = append(result.Errors, envResult.Errors...)
	}

	// Validate workflow inputs
	workflowResult := cv.ValidateWorkflowInputs(channels, matrixJobCount)
	if !workflowResult.Valid {
		result.Valid = false
		result.Errors = append(result.Errors, workflowResult.Errors...)
	}

	// Validate optional notification configuration
	discordWebhook := os.Getenv("DISCORD_WEBHOOK_URL")
	ntfyServer := os.Getenv("NTFY_SERVER_URL")
	ntfyTopic := os.Getenv("NTFY_TOPIC")

	// Check if ntfy is partially configured (missing required fields)
	if ntfyServer != "" && ntfyTopic == "" {
		result.AddError("NTFY_SERVER_URL is set but NTFY_TOPIC is missing")
	}
	if ntfyServer == "" && ntfyTopic != "" {
		result.AddError("NTFY_TOPIC is set but NTFY_SERVER_URL is missing")
	}

	// Log notification configuration status (not an error, just informational)
	if discordWebhook == "" && ntfyServer == "" {
		// No notification services configured - this is valid but worth noting
		// We don't add this as an error since notifications are optional
	}

	return result
}
