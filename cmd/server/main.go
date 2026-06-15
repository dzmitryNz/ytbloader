package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

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
	cfg := config.Load()

	database, err := db.New(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()

	dl := downloader.New(cfg.YTDL.BinaryPath, cfg.YTDL.OutputDir)

	agt := agent.New(database, dl)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
			go dc.Start(ctx)
			log.Println("Discord bot started")
		}
	}

	if cfg.Matter.URL != "" && cfg.Matter.BotToken != "" {
		mm, err := mattermost.New(cfg.Matter.URL, cfg.Matter.BotToken, agt)
		if err != nil {
			log.Printf("Failed to initialize Mattermost bot: %v", err)
		} else {
			go mm.Start(ctx)
			log.Println("Mattermost bot started")
		}
	}

	apiServer := api.NewServer(":"+cfg.Server.Port, database, dl)
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