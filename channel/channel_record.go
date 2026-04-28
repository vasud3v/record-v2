package channel

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/HeapOfChaos/goondvr/chaturbate"
	"github.com/HeapOfChaos/goondvr/internal"
	"github.com/HeapOfChaos/goondvr/notifier"
	"github.com/HeapOfChaos/goondvr/server"
	"github.com/HeapOfChaos/goondvr/site"
	"github.com/HeapOfChaos/goondvr/stripchat"
	"github.com/avast/retry-go/v4"
)

// resolveSite returns the site.Site implementation for the given site name.
// An empty or unrecognised name defaults to Chaturbate.
func resolveSite(siteName string) site.Site {
	switch siteName {
	case "stripchat":
		return stripchat.New()
	default:
		return chaturbate.New()
	}
}

// Monitor starts monitoring the channel for live streams and records them.
func (ch *Channel) Monitor(runID uint64) {
	defer ch.finishMonitor()

	s := resolveSite(ch.Config.Site)
	req := internal.NewReq()
	ch.Info("starting to record `%s`", ch.Config.Username)

	// Seed total disk usage in the background so the UI shows it immediately.
	go ch.ScanTotalDiskUsage()

	// Seed StreamedAt from the site API if we haven't seen this channel stream yet.
	if ch.StreamedAt == 0 {
		if ts, err := s.FetchLastBroadcast(context.Background(), req, ch.Config.Username); err == nil && ts > 0 {
			ch.StreamedAt = ts
			ch.Config.StreamedAt = ts
			_ = server.Manager.SaveConfig()
			ch.Update()
		}
	}

	// Create a new context with a cancel function,
	// the CancelFunc will be stored in the channel's CancelFunc field
	// and will be called by `Pause` or `Stop` functions
	ctx, _ := ch.WithCancel(context.Background())

	var err error
	for {
		if err = ctx.Err(); err != nil {
			break
		}

		pipeline := func() error {
			return ch.RecordStream(ctx, runID, s, req)
		}
		// isExpectedOffline returns true for errors where the full interval delay is appropriate.
		// Transient errors (502, decode errors, network hiccups) should retry quickly.
		// Stream ended should also retry quickly to catch if they go live again.
		// Cloudflare blocks should also retry quickly when cookies are configured.
		isExpectedOffline := func(err error) bool {
			// If stream just ended after recording, check again quickly
			if errors.Is(err, internal.ErrStreamEnded) {
				return false
			}
			// If Cloudflare blocked but we have cookies configured, treat as transient (retry quickly)
			if errors.Is(err, internal.ErrCloudflareBlocked) && server.Config.Cookies != "" {
				return false // Retry in 10 seconds instead of waiting full interval
			}
			
			return errors.Is(err, internal.ErrChannelOffline) ||
				errors.Is(err, internal.ErrPrivateStream) ||
				errors.Is(err, internal.ErrHiddenStream) ||
				errors.Is(err, internal.ErrAgeVerification) ||
				errors.Is(err, internal.ErrCloudflareBlocked) ||
				errors.Is(err, internal.ErrRoomPasswordRequired) ||
				errors.Is(err, internal.ErrDiskSpaceCritical)
		}
		onRetry := func(_ uint, err error) {
			ch.UpdateOnlineStatus(false)

			// Reset CF block count whenever a non-CF response is received.
			if !errors.Is(err, internal.ErrCloudflareBlocked) && ch.CFBlockCount > 0 {
				ch.CFBlockCount = 0
				server.Manager.ResetCFBlock(ch.Config.Username)
				notifier.Default.ResetCooldown(fmt.Sprintf(notifier.KeyCFChannel, ch.Config.Username))
			}

			// Don't log context cancellation as an error (user intentionally paused)
			if errors.Is(err, context.Canceled) {
				return
			}

			if errors.Is(err, internal.ErrStreamEnded) {
				ch.Info("stream ended, checking again in 10s")
			} else if errors.Is(err, internal.ErrChannelOffline) {
				ch.Info("channel is offline, try again in %d min(s)", server.Config.Interval)
			} else if errors.Is(err, internal.ErrDiskSpaceCritical) {
				ch.Info("disk space critical, try again in %d min(s)", server.Config.Interval)
			} else if errors.Is(err, internal.ErrPrivateStream) {
				ch.Info("channel is in a private show, try again in %d min(s)", server.Config.Interval)
			} else if errors.Is(err, internal.ErrHiddenStream) {
				ch.Info("channel is hidden, try again in %d min(s)", server.Config.Interval)
			} else if errors.Is(err, internal.ErrCloudflareBlocked) {
				ch.CFBlockCount++
				cfThresh := server.Config.CFChannelThreshold
				if cfThresh <= 0 {
					cfThresh = 5
				}
				if ch.CFBlockCount >= cfThresh {
					notifier.Notify(
						fmt.Sprintf(notifier.KeyCFChannel, ch.Config.Username),
						"⚠️ Cloudflare Block",
						fmt.Sprintf("`%s` has been blocked by Cloudflare %d times consecutively", ch.Config.Username, ch.CFBlockCount),
					)
				}
				server.Manager.ReportCFBlock(ch.Config.Username)
				
				// If cookies are configured, retry quickly (10s). Otherwise wait full interval.
				if server.Config.Cookies != "" {
					ch.Info("channel was blocked by Cloudflare (cookies configured); retrying in 10s")
				} else {
					ch.Info("channel was blocked by Cloudflare; try with `-cookies` and `-user-agent`? try again in %d min(s)", server.Config.Interval)
				}
			} else if errors.Is(err, internal.ErrAgeVerification) {
				ch.Info("age verification required; pass cookies with `-cookies` to authenticate, try again in %d min(s)", server.Config.Interval)
			} else if errors.Is(err, internal.ErrRoomPasswordRequired) {
				ch.Info("room requires a password, try again in %d min(s)", server.Config.Interval)
			} else {
				ch.Error("on retry: %s: retrying in 10s", err.Error())
			}
		}
		delayFn := func(_ uint, err error, _ *retry.Config) time.Duration {
			if isExpectedOffline(err) {
				base := time.Duration(server.Config.Interval) * time.Minute
				
				// Apply exponential backoff for Cloudflare blocks
				if errors.Is(err, internal.ErrCloudflareBlocked) && ch.CFBlockCount > 1 {
					// Exponential backoff: 5min, 10min, 20min, 30min (capped)
					multiplier := 1 << (ch.CFBlockCount - 1) // 2^(n-1): 1, 2, 4, 8...
					if multiplier > 6 {
						multiplier = 6 // Cap at 6x = 30 minutes for 5-minute interval
					}
					base = base * time.Duration(multiplier)
					ch.Info("applying exponential backoff for CF block #%d: %v", ch.CFBlockCount, base)
				}
				
				jitter := time.Duration(rand.Int63n(int64(base/5))) - base/10 // ±10% of interval
				return base + jitter
			}
			// Transient error (502, decode failure, network hiccup, stream ended) - recover quickly
			return 10 * time.Second
		}
		if err = retry.Do(
			pipeline,
			retry.Context(ctx),
			retry.Attempts(0), // 0 = unlimited attempts - monitor should run indefinitely
			retry.DelayType(delayFn),
			retry.OnRetry(onRetry),
		); err != nil {
			break
		}
	}

	// Always cleanup when monitor exits, regardless of error
	if err := ch.Cleanup(); err != nil {
		ch.Error("cleanup on monitor exit: %s", err.Error())
	}

	// Log error if it's not a context cancellation
	if err != nil && !errors.Is(err, context.Canceled) {
		ch.Error("record stream: %s", err.Error())
	}
}

// Update sends an update signal to the channel's update channel.
// This notifies the Server-sent Event to boradcast the channel information to the client.
func (ch *Channel) Update() {
	select {
	case <-ch.done:
		return
	case ch.UpdateCh <- true:
	}
}

// RecordStream records the stream of the channel using the provided site and HTTP client.
// It retrieves the stream information and starts watching the segments.
func (ch *Channel) RecordStream(ctx context.Context, runID uint64, s site.Site, req *internal.Req) error {
	// Pre-flight disk space check
	diskPercent := server.Manager.CheckDiskSpace()
	if diskPercent > 0 {
		critThresh := float64(server.Config.DiskCriticalPercent)
		if critThresh <= 0 {
			critThresh = 95
		}
		if diskPercent >= critThresh {
			return fmt.Errorf("disk space critical (%.0f%% used): %w", diskPercent, internal.ErrDiskSpaceCritical)
		}
	}

	ch.fileMu.Lock()
	ch.mp4InitSegment = nil
	ch.fileMu.Unlock()

	streamInfo, err := s.FetchStream(ctx, req, ch.Config.Username)

	// Update static metadata whenever the site API returns it, even if the room
	// is currently offline/private/hidden.
	changed := false
	thumbChanged := false
	if streamInfo != nil {
		if streamInfo.RoomTitle != "" && streamInfo.RoomTitle != ch.RoomTitle {
			ch.RoomTitle = streamInfo.RoomTitle
			ch.Config.RoomTitle = streamInfo.RoomTitle
			changed = true
		}
		if streamInfo.Gender != "" && streamInfo.Gender != ch.Gender {
			ch.Gender = streamInfo.Gender
			ch.Config.Gender = streamInfo.Gender
			changed = true
		}
		if streamInfo.SummaryCardImage != "" && streamInfo.SummaryCardImage != ch.SummaryCardImage {
			ch.SummaryCardImage = streamInfo.SummaryCardImage
			ch.Config.SummaryCardImage = streamInfo.SummaryCardImage
			changed = true
			thumbChanged = true
		}
		if changed {
			_ = server.Manager.SaveConfig()
			if thumbChanged {
				ch.UpdateThumb()
			}
			ch.Update()
		}
	}

	if err != nil {
		return fmt.Errorf("get stream: %w", err)
	}
	if streamInfo == nil {
		// Site returned nil, nil — channel is offline.
		return fmt.Errorf("get stream: %w", internal.ErrChannelOffline)
	}

	ch.StreamedAt = time.Now().Unix()
	ch.Config.StreamedAt = ch.StreamedAt
	_ = server.Manager.SaveConfig()
	ch.Sequence = 0
	ch.NumViewers = streamInfo.NumViewers
	if ch.LiveThumbURL != streamInfo.LiveThumbURL {
		ch.LiveThumbURL = streamInfo.LiveThumbURL
		ch.UpdateThumb()
	}

	playlist, err := chaturbate.FetchPlaylist(ctx, streamInfo.HLSURL, ch.Config.Resolution, ch.Config.Framerate, streamInfo.CDNReferer, streamInfo.MouflonPDKey)
	if err != nil {
		return fmt.Errorf("get playlist: %w", err)
	}

	ch.FileExt = playlist.FileExt
	if err := ch.NextFile(playlist.FileExt); err != nil {
		return fmt.Errorf("next file: %w", err)
	}

	// Ensure file is cleaned up when this function exits in any case
	defer func() {
		if err := ch.Cleanup(); err != nil {
			ch.Error("cleanup on record stream exit: %s", err.Error())
		}
	}()

	ch.UpdateOnlineStatus(true) // Update online status after playlist is OK

	// Reset CF state on successful stream start.
	ch.CFBlockCount = 0
	notifier.Default.ResetCooldown(fmt.Sprintf(notifier.KeyCFChannel, ch.Config.Username))
	server.Manager.ResetCFBlock(ch.Config.Username)
	// Notify stream online if enabled.
	if server.Config.NotifyStreamOnline {
		title := fmt.Sprintf("📡 %s is live!", ch.Config.Username)
		body := ch.RoomTitle
		if body == "" {
			body = ch.Config.Username
		}
		notifier.Notify(fmt.Sprintf(notifier.KeyStreamOnline, ch.Config.Username), title, body)
	}

	streamType := "HLS"
	if playlist.FileExt == ".mp4" {
		if playlist.AudioPlaylistURL != "" {
			streamType = "LL-HLS (video+audio)"
		} else if playlist.MouflonPDKey != "" {
			streamType = "HLS (fMP4)"
		} else {
			streamType = "LL-HLS (video only)"
		}
	}
	ch.Info("stream type: %s, resolution %dp (target: %dp), framerate %dfps (target: %dfps)", streamType, playlist.Resolution, ch.Config.Resolution, playlist.Framerate, ch.Config.Framerate)

	// WatchSegments will block here while recording, and return when stream ends
	err = playlist.WatchSegments(ctx, func(b []byte, duration float64) error {
		return ch.handleSegmentForMonitor(runID, b, duration)
	})

	// If we successfully started recording and it ended, return a special error
	// to signal that we should check again immediately (10s retry)
	if err == nil || errors.Is(err, internal.ErrChannelOffline) {
		return internal.ErrStreamEnded
	}

	return err
}

// handleSegmentForMonitor processes and writes segment data for a specific
// monitor run, ignoring stale late-arriving segments from older runs.
func (ch *Channel) handleSegmentForMonitor(runID uint64, b []byte, duration float64) error {
	ch.fileMu.Lock()
	defer ch.fileMu.Unlock()
	
	ch.monitorMu.Lock()
	isPaused := ch.Config.IsPaused
	isCurrentRun := ch.monitorRunID == runID
	ch.monitorMu.Unlock()

	if isPaused || !isCurrentRun {
		return retry.Unrecoverable(internal.ErrPaused)
	}

	if ch.File == nil {
		return fmt.Errorf("write file: no active file")
	}

	if isMP4InitSegment(b) {
		ch.mp4InitSegment = append(ch.mp4InitSegment[:0], b...)
	}
	if ch.FileExt == ".mp4" && ch.Filesize == 0 && !isMP4InitSegment(b) && len(ch.mp4InitSegment) > 0 {
		n, err := ch.File.Write(ch.mp4InitSegment)
		if err != nil {
			return fmt.Errorf("write mp4 init segment: %w", err)
		}
		ch.Filesize += int64(n)
		
		// CRITICAL: Sync init segment immediately to ensure file is playable
		// even if process is killed (e.g., workflow cancellation)
		if err := ch.File.Sync(); err != nil && !errors.Is(err, os.ErrClosed) {
			ch.Error("init segment sync failed: %v", err)
		}
	}

	n, err := ch.File.Write(b)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	ch.Filesize += int64(n)
	ch.Duration += duration
	ch.segmentCount++
	
	// CRITICAL FIX: Sync every 3 segments (~3 seconds) to minimize data loss on crashes
	// This ensures data is written to disk more frequently, reducing corruption risk
	if ch.segmentCount%3 == 0 {
		if err := ch.File.Sync(); err != nil && !errors.Is(err, os.ErrClosed) {
			// Log but don't fail - sync is best-effort for crash protection
			ch.Error("periodic sync failed (segment %d): %v", ch.segmentCount, err)
		}
	}
	
	formattedDuration := internal.FormatDuration(ch.Duration)
	formattedFilesize := internal.FormatFilesize(ch.Filesize)
	shouldSwitch := ch.shouldSwitchFileLocked()

	var newFilename string
	if shouldSwitch {
		if err := ch.cleanupLocked(); err != nil {
			return fmt.Errorf("next file: %w", err)
		}
		filename, err := ch.generateFilenameLocked()
		if err != nil {
			return err
		}
		if err := ch.createNewFileLocked(filename, ch.FileExt); err != nil {
			return fmt.Errorf("next file: %w", err)
		}
		ch.Sequence++
		ch.segmentCount = 0 // Reset counter for new file
		newFilename = ch.File.Name()
	}

	if os.Getenv("GITHUB_ACTIONS") == "true" {
		// Live streams have no predetermined length, so a percentage progress bar is misleading.
		// Instead, we use a sparse time-based heartbeat (e.g., every 5 minutes) to show it's active.
		minutes := int(ch.Duration) / 60
		reportInterval := 5 // Report every 5 minutes
		
		if minutes > 0 && minutes%reportInterval == 0 && minutes > ch.lastReportedProgress {
			ch.Info("—— Recording Active | Duration: %s | Current File: %s", formattedDuration, formattedFilesize)
			ch.lastReportedProgress = minutes
		}
	} else {
		ch.Verbose("duration: %s, filesize: %s", formattedDuration, formattedFilesize)
	}

	// Send an SSE update to update the view
	ch.Update()

	if newFilename != "" {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			ch.lastReportedProgress = 0 // Reset for the next file
		}
		ch.Info("max filesize or duration exceeded, new file created: %s", newFilename)
		return nil
	}
	return nil
}
