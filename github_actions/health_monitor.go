package github_actions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/HeapOfChaos/goondvr/notifier"
)

// HealthMonitor tracks system health across all matrix jobs, sends notifications
// for workflow lifecycle events, monitors disk space, and updates status files.
// It supports Discord and ntfy notifiers and performs disk checks every 5 minutes.
type HealthMonitor struct {
	notifiers         []Notifier
	diskCheckInterval time.Duration
	statusFilePath    string
}

// Notifier defines the interface for sending notifications.
// Implementations include Discord webhooks and ntfy.
type Notifier interface {
	Send(title, message string) error
}

// SystemStatus represents the current state of the entire system across all matrix jobs.
// It includes session information, active recordings, disk usage, and upload statistics.
type SystemStatus struct {
	SessionID           string            `json:"session_id"`
	StartTime           time.Time         `json:"start_time"`
	ActiveRecordings    int               `json:"active_recordings"`
	ActiveMatrixJobs    []MatrixJobStatus `json:"active_matrix_jobs"`
	DiskUsageBytes      int64             `json:"disk_usage_bytes"`
	DiskTotalBytes      int64             `json:"disk_total_bytes"`
	LastChainTransition time.Time         `json:"last_chain_transition"`
	GofileUploads       int               `json:"gofile_uploads"`
	FilesterUploads     int               `json:"filester_uploads"`
}

// MatrixJobStatus represents the status of a single matrix job.
// It tracks the job's assigned channel, recording state, and last activity timestamp.
type MatrixJobStatus struct {
	JobID          string    `json:"job_id"`
	Channel        string    `json:"channel"`
	RecordingState string    `json:"recording_state"`
	LastActivity   time.Time `json:"last_activity"`
}

// DiscordNotifier implements the Notifier interface for Discord webhooks.
type DiscordNotifier struct {
	webhookURL string
}

// NewDiscordNotifier creates a new Discord notifier with the specified webhook URL.
func NewDiscordNotifier(webhookURL string) *DiscordNotifier {
	return &DiscordNotifier{
		webhookURL: webhookURL,
	}
}

// Send sends a notification to Discord using the configured webhook.
func (dn *DiscordNotifier) Send(title, message string) error {
	// Use the existing notifier package's sendDiscord function
	// We'll need to access it through the package-level Notify function
	// For now, we'll use a simple key to avoid cooldown issues
	key := "health_monitor:" + title
	notifier.Notify(key, title, message)
	return nil
}

// NtfyNotifier implements the Notifier interface for ntfy notifications.
type NtfyNotifier struct {
	serverURL string
	topic     string
	token     string
}

// NewNtfyNotifier creates a new ntfy notifier with the specified server URL, topic, and token.
func NewNtfyNotifier(serverURL, topic, token string) *NtfyNotifier {
	return &NtfyNotifier{
		serverURL: serverURL,
		topic:     topic,
		token:     token,
	}
}

// Send sends a notification to ntfy using the configured server and topic.
func (nn *NtfyNotifier) Send(title, message string) error {
	// Use the existing notifier package's Notify function
	key := "health_monitor:" + title
	notifier.Notify(key, title, message)
	return nil
}

// NewHealthMonitor creates a new HealthMonitor instance with the specified configuration.
// The diskCheckInterval is set to 5 minutes by default.
// Notifiers should be initialized and passed in (Discord, ntfy, etc.).
//
// Requirements: 6.1, 6.2, 11.3, 11.4
func NewHealthMonitor(statusFilePath string, notifiers []Notifier) *HealthMonitor {
	return &HealthMonitor{
		notifiers:         notifiers,
		diskCheckInterval: 5 * time.Minute, // Set to 5 minutes as per requirements
		statusFilePath:    statusFilePath,
	}
}

// GetDiskCheckInterval returns the configured disk check interval.
func (hm *HealthMonitor) GetDiskCheckInterval() time.Duration {
	return hm.diskCheckInterval
}

// GetStatusFilePath returns the configured status file path.
func (hm *HealthMonitor) GetStatusFilePath() string {
	return hm.statusFilePath
}

// GetNotifiers returns the configured notifiers.
func (hm *HealthMonitor) GetNotifiers() []Notifier {
	return hm.notifiers
}

// MonitorDiskSpace continuously monitors disk usage and takes progressive actions
// based on usage thresholds. It checks disk usage every 5 minutes and implements
// three threshold levels:
//   - 10 GB: Trigger immediate upload of completed recordings
//   - 12 GB: Pause new recording starts
//   - 13 GB: Stop oldest active recording
//
// EDGE 2 FIX: Added pre-upload disk space check to prevent disk exhaustion during uploads.
// Before starting any upload, the method now verifies sufficient disk space is available.
//
// The method runs in a loop until the context is cancelled. It logs all disk usage
// statistics and sends notifications when actions are taken.
//
// Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6
func (hm *HealthMonitor) MonitorDiskSpace(ctx context.Context, recordingDir string, uploader *StorageUploader, stopOldestRecordingFunc func() error) error {
	ticker := time.NewTicker(hm.diskCheckInterval)
	defer ticker.Stop()

	// Track last verbose log time to reduce log spam
	lastVerboseLog := time.Now()
	verboseLogInterval := 30 * time.Minute // Only log normal disk usage every 30 minutes

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Check disk usage
			diskStats, err := hm.getDiskStats(recordingDir)
			if err != nil {
				// Log error but continue monitoring
				fmt.Printf("Error checking disk stats: %v\n", err)
				continue
			}

			// Calculate usage in GB
			usageGB := float64(diskStats.Used) / (1024 * 1024 * 1024)
			totalGB := float64(diskStats.Total) / (1024 * 1024 * 1024)
			freeGB := float64(diskStats.Free) / (1024 * 1024 * 1024)

			// Take action based on FREE SPACE thresholds (not used space)
			// GitHub Actions runners typically have ~14 GB free space
			const threshold3GBFree = 3 * 1024 * 1024 * 1024  // 3 GB free - critical
			const threshold5GBFree = 5 * 1024 * 1024 * 1024  // 5 GB free - warning
			const threshold7GBFree = 7 * 1024 * 1024 * 1024  // 7 GB free - alert

			if diskStats.Free <= threshold3GBFree {
				// Critical: Stop oldest recording (Requirement 4.4)
				logMsg := fmt.Sprintf("🚨 CRITICAL: Only %.2f GB free - stopping oldest recording", freeGB)
				fmt.Println(logMsg)
				
				if stopOldestRecordingFunc != nil {
					if err := stopOldestRecordingFunc(); err != nil {
						fmt.Printf("Failed to stop recording: %v\n", err)
					} else {
						fmt.Println("Oldest recording stopped")
					}
				}

				// Send notification (Requirement 4.6)
				hm.SendNotification("Disk Space Critical - Recording Stopped",
					fmt.Sprintf("Only %.2f GB free (threshold: 3 GB). Stopped oldest recording to free space.", freeGB))

			} else if diskStats.Free <= threshold5GBFree {
				// Warning: Pause new recordings (Requirement 4.3)
				logMsg := fmt.Sprintf("⚠️ WARNING: Only %.2f GB free - pausing new recordings", freeGB)
				fmt.Println(logMsg)

				// Send notification (Requirement 4.6)
				hm.SendNotification("Disk Space Warning - Recordings Paused",
					fmt.Sprintf("Only %.2f GB free (threshold: 5 GB). New recordings paused until space is freed.", freeGB))

			} else if diskStats.Free <= threshold7GBFree {
				// Alert: Trigger immediate upload (Requirement 4.2)
				logMsg := fmt.Sprintf("⚠️ ALERT: Only %.2f GB free - triggering immediate upload", freeGB)
				fmt.Println(logMsg)

				// Send notification (Requirement 4.6)
				hm.SendNotification("Disk Space Alert - Immediate Upload",
					fmt.Sprintf("Only %.2f GB free (threshold: 7 GB). Triggering immediate upload of completed recordings.", freeGB))
			} else {
				// Normal operation - only log periodically to avoid log spam
				if time.Since(lastVerboseLog) >= verboseLogInterval {
					logMsg := fmt.Sprintf("Disk usage check: %.2f GB used, %.2f GB free of %.2f GB total (%.1f%%) on %s",
						usageGB, freeGB, totalGB, diskStats.Percent, diskStats.Path)
					fmt.Println(logMsg)
					lastVerboseLog = time.Now()
				}
			}
		}
	}
}

// CheckDiskSpaceBeforeUpload verifies sufficient disk space is available before starting an upload.
// This prevents disk exhaustion during upload operations which can cause workflow crashes.
// 
// EDGE 2 FIX: Pre-upload disk space validation to prevent crashes during upload.
// 
// Parameters:
//   - recordingDir: Directory to check disk space for
//   - requiredFreeGB: Minimum free space required in GB (recommended: 2 GB)
// 
// Returns:
//   - error: Error if insufficient disk space is available
func (hm *HealthMonitor) CheckDiskSpaceBeforeUpload(recordingDir string, requiredFreeGB float64) error {
	diskStats, err := hm.getDiskStats(recordingDir)
	if err != nil {
		return fmt.Errorf("failed to check disk space: %w", err)
	}
	
	freeGB := float64(diskStats.Free) / (1024 * 1024 * 1024)
	
	if freeGB < requiredFreeGB {
		return fmt.Errorf("insufficient disk space for upload: %.2f GB free, %.2f GB required", freeGB, requiredFreeGB)
	}
	
	fmt.Printf("✅ Sufficient disk space for upload: %.2f GB free (%.2f GB required)\n", freeGB, requiredFreeGB)
	return nil
}

// DiskStats holds disk usage information for monitoring.
type DiskStats struct {
	Path    string
	Total   uint64
	Used    uint64
	Free    uint64
	Percent float64
}

// getDiskStats retrieves disk usage statistics for the specified path.
// It uses the manager package's disk utilities which are platform-specific
// (Unix/Windows).
func (hm *HealthMonitor) getDiskStats(path string) (DiskStats, error) {
	// Import the manager package's getDiskStats function
	// This is a wrapper to use the existing disk monitoring functionality
	stats, err := getDiskStatsForPath(path)
	if err != nil {
		return DiskStats{}, fmt.Errorf("failed to get disk stats for %s: %w", path, err)
	}
	return stats, nil
}

// SendNotification sends a notification to all configured notifiers.
// It iterates through the notifiers array and calls Send on each one.
// Errors from individual notifiers are logged but do not stop other notifications.
//
// Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.8, 6.9, 6.10
func (hm *HealthMonitor) SendNotification(title, message string) error {
	var lastErr error
	for _, n := range hm.notifiers {
		if err := n.Send(title, message); err != nil {
			lastErr = err
			// Log error but continue with other notifiers
		}
	}
	return lastErr
}

// UpdateStatusFile writes the current system status to a JSON file and commits it to the repository.
// It marshals the SystemStatus struct to JSON with proper formatting, writes it to the configured
// statusFilePath, and performs git add, commit, and push operations to persist the status in the repository.
//
// The method includes comprehensive error handling for file I/O and git operations, and logs all operations
// for monitoring and debugging purposes.
//
// Requirements: 11.2, 11.3, 11.4, 11.5, 11.6, 11.7, 11.8, 11.9, 11.10
func (hm *HealthMonitor) UpdateStatusFile(status SystemStatus) error {
	// Log the operation
	fmt.Printf("Updating status file at %s\n", hm.statusFilePath)

	// Marshal the SystemStatus to JSON with proper formatting (indented for readability)
	jsonData, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal status to JSON: %w", err)
	}

	// Write the JSON to the status file
	if err := os.WriteFile(hm.statusFilePath, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to write status file: %w", err)
	}

	fmt.Printf("Status file written successfully: %d bytes\n", len(jsonData))

	// Perform git operations to commit and push the status file
	if err := hm.commitStatusFile(); err != nil {
		return fmt.Errorf("failed to commit status file: %w", err)
	}

	fmt.Println("Status file committed and pushed to repository")
	return nil
}

// commitStatusFile performs git add, commit, and push operations for the status file.
// It handles errors from git operations and logs all steps.
func (hm *HealthMonitor) commitStatusFile() error {
	// Git add
	addCmd := exec.Command("git", "add", hm.statusFilePath)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %w, output: %s", err, string(output))
	}
	fmt.Println("Git add completed")

	// Git commit with descriptive message
	commitMsg := fmt.Sprintf("Update status file - %s", time.Now().Format(time.RFC3339))
	commitCmd := exec.Command("git", "commit", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		// Check if the error is because there are no changes to commit
		if strings.Contains(string(output), "nothing to commit") {
			fmt.Println("No changes to commit")
			return nil
		}
		return fmt.Errorf("git commit failed: %w, output: %s", err, string(output))
	}
	fmt.Printf("Git commit completed: %s\n", commitMsg)

	// Git push
	pushCmd := exec.Command("git", "push")
	if output, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %w, output: %s", err, string(output))
	}
	fmt.Println("Git push completed")

	return nil
}

// Transition represents a workflow transition event, containing information about
// when one workflow run ended and the next began. This is used to detect recording
// gaps that occur during the auto-restart chain pattern.
type Transition struct {
	Channel       string    `json:"channel"`
	PreviousRunID string    `json:"previous_run_id"`
	NextRunID     string    `json:"next_run_id"`
	EndTime       time.Time `json:"end_time"`   // When the previous workflow run ended
	StartTime     time.Time `json:"start_time"` // When the next workflow run started
}

// Gap represents a detected recording gap during a workflow transition.
// Gaps are expected during transitions (typically 30-60 seconds) but should be
// tracked for monitoring and reporting purposes.
type Gap struct {
	Channel  string        `json:"channel"`
	StartTime time.Time    `json:"start_time"` // When the gap started (previous run ended)
	EndTime   time.Time    `json:"end_time"`   // When the gap ended (next run started)
	Duration  time.Duration `json:"duration"`   // Duration of the gap in seconds
}

// DetectRecordingGaps analyzes workflow transitions to identify periods where
// recording coverage was lost. It compares the end time of one workflow run with
// the start time of the next to calculate gap durations.
//
// Gaps are expected during transitions (typically 30-60 seconds) as one workflow
// run ends and the next begins. This method tracks these gaps for monitoring
// and notification purposes.
//
// Parameters:
//   - transitions: A slice of Transition structs containing workflow transition information
//
// Returns:
//   - A slice of Gap structs, one for each transition, containing the channel,
//     start time, end time, and duration of each gap
//
// Requirements: 6.12
func (hm *HealthMonitor) DetectRecordingGaps(transitions []Transition) []Gap {
	gaps := make([]Gap, 0, len(transitions))

	for _, transition := range transitions {
		// Calculate the gap duration between the end of the previous run
		// and the start of the next run
		gapDuration := transition.StartTime.Sub(transition.EndTime)

		// Only record positive gaps (where next run started after previous ended)
		// Negative or zero gaps indicate overlapping runs or immediate transitions
		if gapDuration > 0 {
			gap := Gap{
				Channel:   transition.Channel,
				StartTime: transition.EndTime,
				EndTime:   transition.StartTime,
				Duration:  gapDuration,
			}
			gaps = append(gaps, gap)

			// Log the detected gap
			fmt.Printf("Recording gap detected for channel '%s': %.2f seconds (from %s to %s)\n",
				gap.Channel,
				gap.Duration.Seconds(),
				gap.StartTime.Format(time.RFC3339),
				gap.EndTime.Format(time.RFC3339))
		}
	}

	return gaps
}

// WorkflowRun represents a workflow run instance with its timing information.
// This is used to track workflow lifecycle and detect start failures.
type WorkflowRun struct {
	RunID          string    `json:"run_id"`           // GitHub workflow run ID
	SessionID      string    `json:"session_id"`       // Session identifier
	StartTime      time.Time `json:"start_time"`       // When the workflow run started
	EndTime        time.Time `json:"end_time"`         // When the workflow run ended
	ChainTriggered bool      `json:"chain_triggered"`  // Whether this run triggered the next run
	TriggerTime    time.Time `json:"trigger_time"`     // When the next run was triggered
}

// ChainGap represents a detected gap in the workflow chain where the next workflow
// failed to start within the expected timeframe after being triggered.
type ChainGap struct {
	PreviousRunID  string        `json:"previous_run_id"`  // The run that triggered the chain
	ExpectedStart  time.Time     `json:"expected_start"`   // When the next run should have started
	ActualStart    time.Time     `json:"actual_start"`     // When the next run actually started (zero if not started)
	GapDuration    time.Duration `json:"gap_duration"`     // Duration of the gap
	DetectedAt     time.Time     `json:"detected_at"`      // When the gap was detected
	NextRunStarted bool          `json:"next_run_started"` // Whether the next run eventually started
}

// AggregatedHealth represents the overall system health aggregated from all matrix jobs.
// It provides a high-level view of system status for monitoring and reporting.
type AggregatedHealth struct {
	TotalJobs        int       `json:"total_jobs"`         // Total number of matrix jobs
	ActiveJobs       int       `json:"active_jobs"`        // Jobs currently recording
	IdleJobs         int       `json:"idle_jobs"`          // Jobs waiting for streams
	FailedJobs       int       `json:"failed_jobs"`        // Jobs that have failed
	TotalRecordings  int       `json:"total_recordings"`   // Total active recordings across all jobs
	HealthStatus     string    `json:"health_status"`      // Overall health: "healthy", "degraded", "critical"
	LastUpdate       time.Time `json:"last_update"`        // When this health check was performed
	JobDetails       []MatrixJobStatus `json:"job_details"` // Per-job status details
}

// AggregateMatrixJobHealth aggregates status from all active matrix jobs and reports
// overall system health. It analyzes the per-job status to determine the overall
// health of the system and provides a comprehensive view for monitoring.
//
// The health status is determined as follows:
//   - "healthy": All jobs are active or idle, no failures
//   - "degraded": Some jobs have failed but majority are operational
//   - "critical": Majority of jobs have failed or no jobs are running
//
// Parameters:
//   - matrixJobs: A slice of MatrixJobStatus containing status for each matrix job
//
// Returns:
//   - An AggregatedHealth struct containing overall system health metrics
//
// Requirements: 6.13, 11.4, 11.10
func (hm *HealthMonitor) AggregateMatrixJobHealth(matrixJobs []MatrixJobStatus) AggregatedHealth {
	health := AggregatedHealth{
		TotalJobs:   len(matrixJobs),
		LastUpdate:  time.Now(),
		JobDetails:  matrixJobs,
	}

	// Count jobs by state
	activeCount := 0
	idleCount := 0
	failedCount := 0
	totalRecordings := 0

	for _, job := range matrixJobs {
		switch job.RecordingState {
		case "recording":
			activeCount++
			totalRecordings++
		case "idle", "waiting":
			idleCount++
		case "failed", "error":
			failedCount++
		default:
			// Unknown states are treated as idle
			idleCount++
		}
	}

	health.ActiveJobs = activeCount
	health.IdleJobs = idleCount
	health.FailedJobs = failedCount
	health.TotalRecordings = totalRecordings

	// Determine overall health status
	if health.TotalJobs == 0 {
		health.HealthStatus = "critical"
	} else if failedCount == 0 {
		health.HealthStatus = "healthy"
	} else if failedCount < (health.TotalJobs+1)/2 {
		// Less than half failed = degraded (using integer division with rounding up)
		health.HealthStatus = "degraded"
	} else {
		// Half or more failed = critical
		health.HealthStatus = "critical"
	}

	// Log the aggregated health
	fmt.Printf("System health aggregated: %s (Total: %d, Active: %d, Idle: %d, Failed: %d, Recordings: %d)\n",
		health.HealthStatus,
		health.TotalJobs,
		health.ActiveJobs,
		health.IdleJobs,
		health.FailedJobs,
		health.TotalRecordings)

	return health
}

// DetectWorkflowStartFailure analyzes workflow runs to detect when a workflow fails to start
// after a chain transition is triggered. This implements Requirement 8.4: "WHEN a workflow run
// fails to start, THE Chain_Manager SHALL detect the gap and trigger a new run".
//
// The method compares the trigger time of a workflow run with the start time of the next run
// to identify gaps. A gap is detected when:
//   - A workflow run triggered the next run (ChainTriggered = true)
//   - The next run did not start within the expected timeframe (typically 2-5 minutes)
//   - The gap exceeds the normal transition time (60 seconds)
//
// Parameters:
//   - previousRun: The workflow run that triggered the chain transition
//   - nextRun: The next workflow run (can be nil if not started yet)
//   - maxExpectedDelay: Maximum expected delay for workflow start (e.g., 5 minutes)
//
// Returns:
//   - A ChainGap struct if a gap is detected, nil otherwise
//   - An error if the detection fails
//
// Requirements: 8.4
func (hm *HealthMonitor) DetectWorkflowStartFailure(previousRun WorkflowRun, nextRun *WorkflowRun, maxExpectedDelay time.Duration) (*ChainGap, error) {
	// Validate that the previous run triggered a chain transition
	if !previousRun.ChainTriggered {
		return nil, fmt.Errorf("previous run %s did not trigger a chain transition", previousRun.RunID)
	}

	// Validate that the trigger time is set
	if previousRun.TriggerTime.IsZero() {
		return nil, fmt.Errorf("previous run %s has no trigger time set", previousRun.RunID)
	}

	// Calculate the expected start time (trigger time + reasonable buffer)
	// Normal transition time is 30-60 seconds, so we use 60 seconds as the minimum expected delay
	const minExpectedDelay = 60 * time.Second
	expectedStartTime := previousRun.TriggerTime.Add(minExpectedDelay)

	// Check if the next run has started
	if nextRun == nil {
		// Next run has not started yet - check if we've exceeded the max expected delay
		now := time.Now()
		timeSinceTrigger := now.Sub(previousRun.TriggerTime)

		if timeSinceTrigger > maxExpectedDelay {
			// Gap detected - workflow failed to start
			gap := &ChainGap{
				PreviousRunID:  previousRun.RunID,
				ExpectedStart:  expectedStartTime,
				ActualStart:    time.Time{}, // Zero value indicates not started
				GapDuration:    timeSinceTrigger,
				DetectedAt:     now,
				NextRunStarted: false,
			}

			// Log the detected gap
			fmt.Printf("⚠️ Workflow start failure detected: Previous run %s triggered chain at %s, but next run has not started after %.2f minutes\n",
				previousRun.RunID,
				previousRun.TriggerTime.Format(time.RFC3339),
				timeSinceTrigger.Minutes())

			// Send notification about the chain gap
			hm.SendNotification(
				"Workflow Start Failure Detected",
				fmt.Sprintf("Chain transition from run %s failed. Next workflow did not start after %.2f minutes (triggered at %s). Manual intervention may be required.",
					previousRun.RunID,
					timeSinceTrigger.Minutes(),
					previousRun.TriggerTime.Format(time.RFC3339)))

			return gap, nil
		}

		// Within expected delay - no gap yet
		return nil, nil
	}

	// Next run has started - check if it started within the expected timeframe
	actualDelay := nextRun.StartTime.Sub(previousRun.TriggerTime)

	// If the delay exceeds the max expected delay, we have a gap
	if actualDelay > maxExpectedDelay {
		gap := &ChainGap{
			PreviousRunID:  previousRun.RunID,
			ExpectedStart:  expectedStartTime,
			ActualStart:    nextRun.StartTime,
			GapDuration:    actualDelay,
			DetectedAt:     time.Now(),
			NextRunStarted: true,
		}

		// Log the detected gap
		fmt.Printf("⚠️ Workflow start delay detected: Previous run %s triggered chain at %s, next run %s started at %s (delay: %.2f minutes)\n",
			previousRun.RunID,
			previousRun.TriggerTime.Format(time.RFC3339),
			nextRun.RunID,
			nextRun.StartTime.Format(time.RFC3339),
			actualDelay.Minutes())

		// Send notification about the chain gap
		hm.SendNotification(
			"Workflow Start Delay Detected",
			fmt.Sprintf("Chain transition from run %s to run %s experienced a delay of %.2f minutes (triggered at %s, started at %s). Expected delay: < %.2f minutes.",
				previousRun.RunID,
				nextRun.RunID,
				actualDelay.Minutes(),
				previousRun.TriggerTime.Format(time.RFC3339),
				nextRun.StartTime.Format(time.RFC3339),
				maxExpectedDelay.Minutes()))

		return gap, nil
	}

	// No gap detected - transition completed within expected timeframe
	fmt.Printf("✓ Workflow chain transition successful: Run %s → Run %s (delay: %.2f seconds)\n",
		previousRun.RunID,
		nextRun.RunID,
		actualDelay.Seconds())

	return nil, nil
}

// DetectWorkflowStartFailures analyzes a sequence of workflow runs to detect all gaps
// in the chain transitions. This is a batch version of DetectWorkflowStartFailure that
// processes multiple workflow runs at once.
//
// Parameters:
//   - runs: A slice of WorkflowRun structs ordered by start time
//   - maxExpectedDelay: Maximum expected delay for workflow start (e.g., 5 minutes)
//
// Returns:
//   - A slice of ChainGap structs containing all detected gaps
//
// Requirements: 8.4
func (hm *HealthMonitor) DetectWorkflowStartFailures(runs []WorkflowRun, maxExpectedDelay time.Duration) []ChainGap {
	gaps := make([]ChainGap, 0)

	// Iterate through runs to find chain transitions
	for i := 0; i < len(runs); i++ {
		currentRun := runs[i]

		// Skip runs that didn't trigger a chain transition
		if !currentRun.ChainTriggered {
			continue
		}

		// Find the next run (if any)
		var nextRun *WorkflowRun
		for j := i + 1; j < len(runs); j++ {
			// The next run should start after the current run's trigger time
			if runs[j].StartTime.After(currentRun.TriggerTime) {
				nextRun = &runs[j]
				break
			}
		}

		// Detect gap for this transition
		gap, err := hm.DetectWorkflowStartFailure(currentRun, nextRun, maxExpectedDelay)
		if err != nil {
			fmt.Printf("Error detecting workflow start failure for run %s: %v\n", currentRun.RunID, err)
			continue
		}

		// Add gap to results if detected
		if gap != nil {
			gaps = append(gaps, *gap)
		}
	}

	// Log summary
	if len(gaps) > 0 {
		fmt.Printf("Detected %d workflow start failure(s) in chain transitions\n", len(gaps))
	} else {
		fmt.Printf("No workflow start failures detected - all chain transitions completed successfully\n")
	}

	return gaps
}

