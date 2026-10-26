package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mitry/ytbloader/pkg/agent"
	"github.com/mitry/ytbloader/pkg/api"
	"github.com/mitry/ytbloader/pkg/config"
	"github.com/mitry/ytbloader/pkg/db"
	"github.com/mitry/ytbloader/pkg/downloader"
	"github.com/mitry/ytbloader/pkg/messenger/discord"
	"github.com/mitry/ytbloader/pkg/messenger/mattermost"
	"github.com/mitry/ytbloader/pkg/messenger/telegram"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	database, err := db.New(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()

	dl := downloader.New(cfg.YTDL.BinaryPath, cfg.YTDL.OutputDir, cfg.YTDL.MaxConcurrent, downloader.Options{
		JSRuntime:        cfg.YTDL.JSRuntime,
		RemoteComponents: cfg.YTDL.RemoteComponents,
		POTScript:        cfg.YTDL.POTScript,
		PlayerClient:     cfg.YTDL.PlayerClient,
		MarkWatched:      cfg.YTDL.MarkWatched,
		Cookies:          cfg.YTDL.Cookies,
	})

	agt := agent.New(database, dl)
	agt.SetRetention(agent.Retention{
		Dir:      cfg.YTDL.OutputDir,
		MaxBytes: int64(cfg.Downloads.MaxGB * (1 << 30)),
		MaxAge:   time.Duration(cfg.Downloads.MaxAgeDays) * 24 * time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if n, err := agt.ResumeInterrupted(ctx); err != nil {
		log.Printf("Failed to resume interrupted downloads: %v", err)
	} else if n > 0 {
		log.Printf("Resumed %d interrupted download(s)", n)
	}

	if cfg.Telegram.BotToken != "" {
		tg, err := telegram.New(cfg.Telegram.BotToken, agt)
		if err != nil {
			log.Printf("Failed to initialize Telegram bot: %v", err)
		} else {
			go tg.Start(ctx)
			log.Println("Telegram bot started")
		}
	}

	if cfg.Discord.BotToken != "" {
		dc, err := discord.New(cfg.Discord.BotToken, agt)
		if err != nil {
			log.Printf("Failed to initialize Discord bot: %v", err)
		} else {
			go func() {
				if err := dc.Start(ctx); err != nil {
					log.Printf("Discord bot error: %v", err)
				}
			}()
			log.Println("Discord bot starting")
		}
	}

	if cfg.Matter.URL != "" && cfg.Matter.BotToken != "" {
		mm, err := mattermost.New(cfg.Matter.URL, cfg.Matter.BotToken, agt)
		if err != nil {
			log.Printf("Failed to initialize Mattermost bot: %v", err)
		} else {
			go mm.Start(ctx)
			log.Println("Mattermost enabled (send-only)")
		}
	}

	// Started after the messengers so their file senders are registered.
	if cfg.Downloads.AutoInterval > 0 {
		go agt.RunSubscriptionPoller(ctx, cfg.Downloads.AutoInterval)
		log.Printf("Auto-download polls subscriptions every %s", cfg.Downloads.AutoInterval)
	}
	if cfg.Downloads.MaxGB > 0 || cfg.Downloads.MaxAgeDays > 0 {
		go agt.RunCleanup(ctx, 24*time.Hour)
	}

	apiServer := api.NewServer(net.JoinHostPort(cfg.Server.Host, cfg.Server.Port), database, dl, agt, cfg.WebDir, cfg.Server.APIToken)
	go func() {
		if err := apiServer.Start(); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	log.Println("Shutting down...")
	cancel()

	if err := apiServer.Stop(ctx); err != nil {
		log.Printf("API server shutdown error: %v", err)
	}

	log.Println("Server stopped")
}
