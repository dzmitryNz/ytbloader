package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mitry/ytbloader/pkg/db"
	"github.com/mitry/ytbloader/pkg/downloader"
)

type FileSender func(messengerID string, chatID int64, filePath string) error

type Agent struct {
	db         *db.DB
	downloader *downloader.Downloader
	senders    map[string]FileSender
	mu         sync.RWMutex
	cancels    map[int64]context.CancelFunc
}

type Response struct {
	Text     string
	HasFile  bool
	FilePath string
}

const videosPerPage = 10

// progressWriteInterval caps how often download progress is persisted.
const progressWriteInterval = 3 * time.Second

func New(database *db.DB, dl *downloader.Downloader) *Agent {
	return &Agent{
		db:         database,
		downloader: dl,
		senders:    make(map[string]FileSender),
		cancels:    make(map[int64]context.CancelFunc),
	}
}

func (a *Agent) RegisterSender(messengerID string, sender FileSender) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.senders[messengerID] = sender
}

func (a *Agent) ProcessMessage(ctx context.Context, messengerID string, userID int64, chatID int64, message string) (*Response, error) {
	parts := strings.Fields(message)
	if len(parts) == 0 {
		return &Response{Text: "Empty message"}, nil
	}

	command := strings.ToLower(parts[0])

	switch command {
	case "/download", "/dl":
		return a.handleDownload(ctx, messengerID, userID, chatID, parts[1:])
	case "/tasks", "/t":
		return a.handleListTasks(messengerID, userID)
	case "/task":
		return a.handleCreateTask(messengerID, userID, parts[1:])
	case "/done":
		return a.handleDoneTask(parts[1:])
	case "/sub":
		return a.handleSubscribe(ctx, messengerID, userID, chatID, parts[1:])
	case "/subs":
		return a.handleListSubs(messengerID, userID)
	case "/unsub":
		return a.handleUnsub(parts[1:])
	case "/videos":
		return a.handleVideos(ctx, parts[1:])
	case "/new":
		return a.handleNewVideos(ctx, messengerID, userID, parts[1:])
	case "/help", "/h":
		return a.handleHelp(), nil
	default:
		return a.handleUnknown(command)
	}
}

func (a *Agent) handleDownload(ctx context.Context, messengerID string, userID int64, chatID int64, args []string) (*Response, error) {
	if len(args) == 0 {
		return &Response{Text: "Usage: /download <url> [--send]\n\nFlags:\n  --send  Send MP3 file back to chat"}, nil
	}

	url := args[0]
	sendFile := false

	for _, arg := range args[1:] {
		if arg == "--send" || arg == "-s" {
			sendFile = true
		}
	}

	title, channelName, err := a.downloader.GetInfo(ctx, url)
	if err != nil {
		return &Response{Text: fmt.Sprintf("Failed to get video info: %v", err)}, nil
	}

	d := &db.Download{
		URL:         url,
		Title:       title,
		ChannelName: channelName,
		Status:      "pending",
		MessengerID: messengerID,
		UserID:      userID,
		ChatID:      chatID,
		SendFile:    sendFile,
	}

	if err := a.db.CreateDownload(d); err != nil {
		return &Response{Text: fmt.Sprintf("Failed to save download: %v", err)}, nil
	}

	a.ProcessDownload(ctx, d)

	sendInfo := ""
	if sendFile {
		sendInfo = "\nFile will be sent to chat when ready"
	}

	return &Response{Text: fmt.Sprintf("Download started: %s\nID: %d%s", title, d.ID, sendInfo)}, nil
}

func (a *Agent) ProcessDownload(ctx context.Context, d *db.Download) {
	ctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancels[d.ID] = cancel
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.cancels, d.ID)
			a.mu.Unlock()
		}()
		a.processDownload(ctx, d)
	}()
}

// ResumeInterrupted re-queues downloads that a restart left in a non-terminal
// status, which would otherwise sit in the list forever. Returns how many were
// picked up.
func (a *Agent) ResumeInterrupted(ctx context.Context) (int, error) {
	downloads, err := a.db.ListDownloadsByStatus("pending", "downloading")
	if err != nil {
		return 0, err
	}

	for i := range downloads {
		a.ProcessDownload(ctx, &downloads[i])
	}

	return len(downloads), nil
}

func (a *Agent) CancelDownload(id int64) bool {
	a.mu.RLock()
	cancel, ok := a.cancels[id]
	a.mu.RUnlock()
	if ok {
		cancel()
		return true
	}
	return false
}

func (a *Agent) processDownload(ctx context.Context, d *db.Download) {
	_ = a.db.UpdateDownloadStatus(d.ID, "pending")

	// yt-dlp emits a progress line many times per second; OnProgress runs on a
	// single goroutine, so plain variables are enough to rate-limit the writes.
	var (
		lastWrite time.Time
		lastPct   int
	)

	result, err := a.downloader.Download(ctx, &downloader.DownloadRequest{
		URL: d.URL,
		OnStart: func() {
			_ = a.db.UpdateDownloadStatus(d.ID, "downloading")
		},
		OnProgress: func(pct int) {
			// The closing 100 is the one update worth writing unconditionally;
			// rate-limiting it away leaves finished rows stuck just short.
			if pct == lastPct || (pct < 100 && time.Since(lastWrite) < progressWriteInterval) {
				return
			}
			lastPct = pct
			lastWrite = time.Now()
			_ = a.db.UpdateDownloadProgress(d.ID, pct)
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			_ = a.db.UpdateDownloadError(d.ID, "cancelled", "")
			return
		}
		_ = a.db.UpdateDownloadError(d.ID, "failed", err.Error())
		return
	}

	_ = a.db.UpdateDownloadResult(d.ID, result.Title, result.OutputPath, "completed", result.Size)

	d.OutputPath = result.OutputPath
	d.Title = result.Title

	if d.SendFile && d.ChatID != 0 {
		a.mu.RLock()
		sender, ok := a.senders[d.MessengerID]
		a.mu.RUnlock()

		if ok {
			if err := sender(d.MessengerID, d.ChatID, result.OutputPath); err != nil {
				fmt.Printf("Failed to send file: %v\n", err)
			}
		}
	}
}

func (a *Agent) handleSubscribe(ctx context.Context, messengerID string, userID int64, chatID int64, args []string) (*Response, error) {
	if len(args) == 0 {
		return &Response{Text: "Usage: /sub <channel_url>\n\nExample: /sub https://www.youtube.com/@channel"}, nil
	}

	url := args[0]

	existing, _ := a.db.GetChannelByURL(url, messengerID, userID)
	if existing != nil {
		return &Response{Text: fmt.Sprintf("Already subscribed to: %s (ID: %d)", existing.Name, existing.ID)}, nil
	}

	info, err := a.downloader.GetChannelInfo(ctx, url)
	if err != nil {
		return &Response{Text: fmt.Sprintf("Failed to get channel info: %v", err)}, nil
	}

	channel := &db.Channel{
		URL:         url,
		Name:        info.Name,
		MessengerID: messengerID,
		UserID:      userID,
		ChatID:      chatID,
	}

	if err := a.db.CreateChannel(channel); err != nil {
		return &Response{Text: fmt.Sprintf("Failed to subscribe: %v", err)}, nil
	}

	return &Response{Text: fmt.Sprintf("Subscribed to: %s\nID: %d\nUse /videos %d to see videos", info.Name, channel.ID, channel.ID)}, nil
}

func (a *Agent) handleListSubs(messengerID string, userID int64) (*Response, error) {
	channels, err := a.db.ListChannels(messengerID, userID)
	if err != nil {
		return &Response{Text: fmt.Sprintf("Failed to list subscriptions: %v", err)}, nil
	}

	if len(channels) == 0 {
		return &Response{Text: "No subscriptions yet.\nUse /sub <channel_url> to subscribe"}, nil
	}

	var sb strings.Builder
	sb.WriteString("Your subscriptions:\n\n")
	for _, c := range channels {
		sb.WriteString(fmt.Sprintf("  %d. %s\n     %s\n", c.ID, c.Name, c.URL))
	}
	sb.WriteString("\nUse /videos <id> to see videos\nUse /new to see new videos")

	return &Response{Text: sb.String()}, nil
}

func (a *Agent) handleUnsub(args []string) (*Response, error) {
	if len(args) == 0 {
		return &Response{Text: "Usage: /unsub <channel_id>"}, nil
	}

	var id int64
	if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil {
		return &Response{Text: "Invalid channel ID"}, nil
	}

	channel, err := a.db.GetChannel(id)
	if err != nil {
		return &Response{Text: "Channel not found"}, nil
	}

	if err := a.db.DeleteChannel(id); err != nil {
		return &Response{Text: fmt.Sprintf("Failed to unsubscribe: %v", err)}, nil
	}

	return &Response{Text: fmt.Sprintf("Unsubscribed from: %s", channel.Name)}, nil
}

func (a *Agent) handleVideos(ctx context.Context, args []string) (*Response, error) {
	if len(args) == 0 {
		return &Response{Text: "Usage: /videos <channel_id> [page]"}, nil
	}

	var channelID int64
	if _, err := fmt.Sscanf(args[0], "%d", &channelID); err != nil {
		return &Response{Text: "Invalid channel ID"}, nil
	}

	page := 1
	if len(args) >= 2 {
		fmt.Sscanf(args[1], "%d", &page)
	}
	if page < 1 {
		page = 1
	}

	channel, err := a.db.GetChannel(channelID)
	if err != nil {
		return &Response{Text: "Channel not found"}, nil
	}

	videos, err := a.downloader.GetChannelVideos(ctx, channel.URL, 50)
	if err != nil {
		return &Response{Text: fmt.Sprintf("Failed to fetch videos: %v", err)}, nil
	}

	for _, v := range videos {
		a.db.UpsertVideo(&db.Video{
			ChannelID: channelID,
			URL:       v.URL,
			Title:     v.Title,
			Duration:  v.Duration,
		})
	}

	total, _ := a.db.CountVideos(channelID)
	totalPages := (total + videosPerPage - 1) / videosPerPage
	if totalPages < 1 {
		totalPages = 1
	}

	offset := (page - 1) * videosPerPage
	dbVideos, err := a.db.ListAllVideos(channelID, videosPerPage, offset)
	if err != nil {
		return &Response{Text: fmt.Sprintf("Failed to list videos: %v", err)}, nil
	}

	if len(dbVideos) == 0 {
		return &Response{Text: fmt.Sprintf("No videos found in: %s", channel.Name)}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Videos in %s (page %d/%d):\n\n", channel.Name, page, totalPages))

	for i, v := range dbVideos {
		num := offset + i + 1
		duration := formatDuration(v.Duration)
		sb.WriteString(fmt.Sprintf("  %d. %s [%s]\n     /dl %s --send\n", num, v.Title, duration, v.URL))
	}

	if page < totalPages {
		sb.WriteString(fmt.Sprintf("\n/page %d - next page", page+1))
	}

	return &Response{Text: sb.String()}, nil
}

func (a *Agent) handleNewVideos(ctx context.Context, messengerID string, userID int64, args []string) (*Response, error) {
	page := 1
	if len(args) >= 1 {
		fmt.Sscanf(args[0], "%d", &page)
	}
	if page < 1 {
		page = 1
	}

	channels, err := a.db.ListChannels(messengerID, userID)
	if err != nil {
		return &Response{Text: fmt.Sprintf("Failed to list subscriptions: %v", err)}, nil
	}

	if len(channels) == 0 {
		return &Response{Text: "No subscriptions yet.\nUse /sub <channel_url> to subscribe"}, nil
	}

	var allNewVideos []struct {
		ChannelName string
		Videos      []db.Video
	}

	for _, ch := range channels {
		videos, err := a.downloader.GetChannelVideos(ctx, ch.URL, 20)
		if err != nil {
			continue
		}

		// Everything stored after the previous check is what's actually new;
		// UpsertVideo leaves created_at untouched for rows we already had.
		lastCheck := ch.LastCheck

		for i, v := range videos {
			_ = a.db.UpsertVideo(&db.Video{
				ChannelID: ch.ID,
				URL:       v.URL,
				Title:     v.Title,
				Duration:  v.Duration,
				Position:  i + 1,
			})
		}

		newVideos, err := a.db.ListNewVideos(ch.ID, lastCheck)
		if err != nil {
			continue
		}
		_ = a.db.UpdateChannelLastCheck(ch.ID)

		if len(newVideos) > 0 {
			allNewVideos = append(allNewVideos, struct {
				ChannelName string
				Videos      []db.Video
			}{ChannelName: ch.Name, Videos: newVideos})
		}
	}

	if len(allNewVideos) == 0 {
		return &Response{Text: "No new videos found"}, nil
	}

	var sb strings.Builder
	sb.WriteString("New videos:\n\n")

	for _, entry := range allNewVideos {
		sb.WriteString(fmt.Sprintf("  %s:\n", entry.ChannelName))
		for _, v := range entry.Videos {
			sb.WriteString(fmt.Sprintf("    - %s\n      /dl %s --send\n", v.Title, v.URL))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Use /mark-read to mark all as read")

	return &Response{Text: sb.String()}, nil
}

func (a *Agent) handleDoneTask(args []string) (*Response, error) {
	if len(args) == 0 {
		return &Response{Text: "Usage: /done <task_id>"}, nil
	}

	var id int64
	if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil {
		return &Response{Text: "Invalid task ID"}, nil
	}

	if err := a.db.UpdateTaskStatus(id, "done"); err != nil {
		return &Response{Text: fmt.Sprintf("Failed to complete task: %v", err)}, nil
	}

	return &Response{Text: fmt.Sprintf("Task %d completed", id)}, nil
}

func (a *Agent) handleListTasks(messengerID string, userID int64) (*Response, error) {
	tasks, err := a.db.ListTasks(messengerID, userID)
	if err != nil {
		return &Response{Text: fmt.Sprintf("Failed to list tasks: %v", err)}, nil
	}

	if len(tasks) == 0 {
		return &Response{Text: "No tasks found"}, nil
	}

	var sb strings.Builder
	sb.WriteString("Your tasks:\n\n")
	for _, t := range tasks {
		status := "○"
		if t.Status == "done" {
			status = "●"
		}
		sb.WriteString(fmt.Sprintf("%s %d. %s\n", status, t.ID, t.Title))
	}

	return &Response{Text: sb.String()}, nil
}

func (a *Agent) handleCreateTask(messengerID string, userID int64, args []string) (*Response, error) {
	if len(args) == 0 {
		return &Response{Text: "Usage: /task <title>"}, nil
	}

	title := strings.Join(args, " ")
	t := &db.Task{
		Title:       title,
		Status:      "open",
		MessengerID: messengerID,
		UserID:      userID,
	}

	if err := a.db.CreateTask(t); err != nil {
		return &Response{Text: fmt.Sprintf("Failed to create task: %v", err)}, nil
	}

	return &Response{Text: fmt.Sprintf("Task created: %d. %s", t.ID, t.Title)}, nil
}

func (a *Agent) handleHelp() *Response {
	return &Response{
		Text: `Commands:

/download <url> [--send] - Download YouTube video as MP3
  --send, -s  Send MP3 file back to chat

/sub <channel_url> - Subscribe to a channel
/subs - List your subscriptions
/unsub <id> - Unsubscribe from a channel
/videos <id> [page] - List videos in a channel
/new [page] - List new videos from subscriptions

/tasks - List your tasks
/task <title> - Create a new task
/done <id> - Mark task as done
/help - Show this help`,
	}
}

func (a *Agent) handleUnknown(cmd string) (*Response, error) {
	return &Response{Text: fmt.Sprintf("Unknown command: %s\nType /help for available commands", cmd)}, nil
}

func formatDuration(seconds int) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
