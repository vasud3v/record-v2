package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HeapOfChaos/goondvr/config"
	"github.com/HeapOfChaos/goondvr/entity"
	"github.com/HeapOfChaos/goondvr/github_actions"
	"github.com/HeapOfChaos/goondvr/internal"
	"github.com/HeapOfChaos/goondvr/manager"
	"github.com/HeapOfChaos/goondvr/router"
	"github.com/HeapOfChaos/goondvr/server"
	"github.com/urfave/cli/v2"
)

const logo = `
 ██████╗  ██████╗  ██████╗ ███╗   ██╗██████╗ ██╗   ██╗██████╗
██╔════╝ ██╔═══██╗██╔═══██╗████╗  ██║██╔══██╗██║   ██║██╔══██╗
██║  ███╗██║   ██║██║   ██║██╔██╗ ██║██║  ██║██║   ██║██████╔╝
██║   ██║██║   ██║██║   ██║██║╚██╗██║██║  ██║╚██╗ ██╔╝██╔══██╗
╚██████╔╝╚██████╔╝╚██████╔╝██║ ╚████║██████╔╝ ╚████╔╝ ██║  ██║
 ╚═════╝  ╚═════╝  ╚═════╝ ╚═╝  ╚═══╝╚═════╝   ╚═══╝  ╚═╝  ╚═╝`

func main() {
	app := &cli.App{
		Name:    "goondvr",
		Version: "3.1.1",
		Usage:   "Record your favorite streams automatically. 😎🫵",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "username",
				Aliases: []string{"u"},
				Usage:   "The username of the channel to record",
				Value:   "",
			},
			&cli.StringFlag{
				Name:  "site",
				Usage: "Site to record from: chaturbate or stripchat",
				Value: "chaturbate",
			},
			&cli.StringFlag{
				Name:  "admin-username",
				Usage: "Username for web authentication (optional)",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "admin-password",
				Usage: "Password for web authentication (optional)",
				Value: "",
			},
			&cli.IntFlag{
				Name:  "framerate",
				Usage: "Desired framerate (FPS)",
				Value: 30,
			},
			&cli.IntFlag{
				Name:  "resolution",
				Usage: "Desired resolution (e.g., 1080 for 1080p)",
				Value: 1080,
			},
			&cli.StringFlag{
				Name:  "pattern",
				Usage: "Template for naming recorded videos",
				Value: "videos/{{if ne .Site \"chaturbate\"}}{{.Site}}/{{end}}{{.Username}}_{{.Year}}-{{.Month}}-{{.Day}}_{{.Hour}}-{{.Minute}}-{{.Second}}{{if .Sequence}}_{{.Sequence}}{{end}}",
			},
			&cli.IntFlag{
				Name:  "max-duration",
				Usage: "Split video into segments every N minutes ('0' to disable)",
				Value: 0,
			},
			&cli.IntFlag{
				Name:  "max-filesize",
				Usage: "Split video into segments every N MB ('0' to disable)",
				Value: 0,
			},
			&cli.StringFlag{
				Name:    "port",
				Aliases: []string{"p"},
				Usage:   "Port for the web interface and API",
				Value:   "8080",
			},
			&cli.IntFlag{
				Name:  "interval",
				Usage: "Check if the channel is online every N minutes",
				Value: 1,
			},
			&cli.StringFlag{
				Name:  "cookies",
				Usage: "Cookies to use in the request (format: key=value; key2=value2)",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "user-agent",
				Usage: "Custom User-Agent for the request",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "domain",
				Usage: "Chaturbate domain to use",
				Value: "https://chaturbate.com/",
			},
			&cli.StringFlag{
				Name:  "completed-dir",
				Usage: "Directory to move fully closed recordings into (default: <recording dir>/completed)",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "finalize-mode",
				Usage: "Post-process closed recordings: none, remux, or transcode",
				Value: "remux",
			},
			&cli.StringFlag{
				Name:  "ffmpeg-encoder",
				Usage: "FFmpeg video encoder for transcode mode (e.g. libx264, libx265, h264_nvenc)",
				Value: "libx264",
			},
			&cli.StringFlag{
				Name:  "ffmpeg-container",
				Usage: "FFmpeg output container for remux/transcode mode (mp4 or mkv)",
				Value: "mp4",
			},
			&cli.IntFlag{
				Name:  "ffmpeg-quality",
				Usage: "FFmpeg quality value (CRF for software encoders, CQ for many hardware encoders)",
				Value: 23,
			},
			&cli.StringFlag{
				Name:  "ffmpeg-preset",
				Usage: "FFmpeg preset for transcode mode",
				Value: "medium",
			},
			&cli.BoolFlag{
				Name:  "debug",
				Usage: "Write full HTML response to a temp file when stream detection fails",
				Value: false,
			},
			&cli.StringFlag{
				Name:  "stripchat-pdkey",
				Usage: "Stripchat MOUFLON v2 decryption key (auto-extracted if omitted)",
				Value: "",
			},
		},
		Action: func(c *cli.Context) error {
			// Check if running in GitHub Actions mode
			if c.String("mode") == "github-actions" {
				return startGitHubActionsMode(c)
			}
			// Otherwise run normal mode
			return start(c)
		},
	}
	
	// Add GitHub Actions mode flags
	github_actions.AddGitHubActionsModeFlags(app)
	
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

// startGitHubActionsMode handles the GitHub Actions continuous runner mode
func startGitHubActionsMode(c *cli.Context) error {
	fmt.Println(logo)
	fmt.Println("\n🚀 Starting GitHub Actions Continuous Runner Mode\n")
	
	// Initialize server config (needed by Manager)
	var err error
	server.Config, err = config.New(c)
	if err != nil {
		return fmt.Errorf("failed to create config: %w", err)
	}
	
	// Load settings (needed by Manager)
	if err := manager.LoadSettings(); err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	
	// Refresh cookies using FlareSolverr if in GitHub Actions
	// This gets fresh cookies valid for the GitHub Actions runner's IP
	if internal.ShouldRefreshCookies() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second) // 5 minutes - increased for FlareSolverr startup time
		defer cancel()
		if err := internal.RefreshCookiesWithFlareSolverr(ctx); err != nil {
			log.Printf("⚠️  Warning: Failed to refresh cookies with FlareSolverr: %v", err)
			log.Println("   Continuing with existing cookies from settings.json")
		}
	}
	
	// Initialize Manager (needed to start recordings)
	server.Manager, err = manager.New()
	if err != nil {
		return fmt.Errorf("failed to create manager: %w", err)
	}
	
	// Parse and validate GitHub Actions mode configuration
	gam, err := github_actions.ParseGitHubActionsModeConfig(c)
	if err != nil {
		return fmt.Errorf("failed to parse GitHub Actions mode config: %w", err)
	}
	
	// Start the workflow lifecycle (this will create channel config and start recording)
	configDir := "./conf"
	recordingsDir := "./videos"
	
	if err := gam.StartWorkflowLifecycle(configDir, recordingsDir, server.Manager); err != nil {
		return fmt.Errorf("failed to start workflow lifecycle: %w", err)
	}
	
	// Handle SIGINT / SIGTERM for GitHub Actions cancellation
	// This ensures recordings are saved even when the workflow is manually cancelled
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		fmt.Println("\n⚠️  Workflow cancellation detected - initiating emergency shutdown...")
		fmt.Println("📼 Saving in-progress recordings...")
		
		// Cancel the GitHub Actions mode context to stop all background goroutines
		gam.Cancel()
		
		// Gracefully shutdown the manager to finalize recordings
		server.Manager.Shutdown()
		
		// Save state to cache before exiting
		fmt.Println("💾 Saving state to cache...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		
		if err := gam.StatePersister.SaveState(ctx, configDir, recordingsDir); err != nil {
			log.Printf("⚠️  Warning: Failed to save state: %v", err)
		} else {
			fmt.Println("✅ State saved successfully")
		}
		
		// Upload any completed recordings
		fmt.Println("📤 Uploading completed recordings...")
		if gam.StorageUploader != nil {
			// Give uploads 60 seconds to complete
			uploadCtx, uploadCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer uploadCancel()
			
			// Scan for completed recordings and upload them
			if err := gam.UploadCompletedRecordings(uploadCtx, recordingsDir); err != nil {
				log.Printf("⚠️  Warning: Failed to upload recordings: %v", err)
			} else {
				fmt.Println("✅ Recordings uploaded successfully")
			}
		}
		
		fmt.Println("✅ Emergency shutdown complete - recordings saved!")
		os.Exit(0)
	}()
	
	// Keep the process running
	fmt.Println("\n✅ GitHub Actions mode started successfully - process will run until graceful shutdown")
	fmt.Println("💡 Tip: If you cancel the workflow, recordings will be automatically saved")
	select {}
}

func start(c *cli.Context) error {
	fmt.Println(logo)

	var err error
	server.Config, err = config.New(c)
	if err != nil {
		return fmt.Errorf("new config: %w", err)
	}
	if err := manager.LoadSettings(); err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	server.Manager, err = manager.New()
	if err != nil {
		return fmt.Errorf("new manager: %w", err)
	}

	// Handle SIGINT / SIGTERM so in-progress recordings are cleanly closed and
	// seek-indexed before the process exits.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		fmt.Println("Shutting down, waiting for recordings to close and finalize...")
		server.Manager.Shutdown()
		os.Exit(0)
	}()

	// init web interface if username is not provided
	if server.Config.Username == "" {
		fmt.Printf("👋 Visit http://localhost:%s to use the Web UI\n\n\n", c.String("port"))

		if err := server.Manager.LoadConfig(); err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		return router.SetupRouter().Run(":" + c.String("port"))
	}

	// else create a channel with the provided username
	if err := server.Manager.CreateChannel(&entity.ChannelConfig{
		IsPaused:    false,
		Username:    c.String("username"),
		Site:        server.Config.Site,
		Framerate:   c.Int("framerate"),
		Resolution:  c.Int("resolution"),
		Pattern:     c.String("pattern"),
		MaxDuration: c.Int("max-duration"),
		MaxFilesize: c.Int("max-filesize"),
	}, false); err != nil {
		return fmt.Errorf("create channel: %w", err)
	}

	// block forever
	select {}
}
