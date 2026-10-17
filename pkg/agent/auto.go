package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mitry/ytbloader/pkg/db"
)

func (a *Agent) handleAuto(ctx context.Context, messengerID string, userID int64, chatID int64, args []string) (*Response, error) {
	const usage = "Usage: /auto <channel_id> [on|off]"
	if len(args) == 0 {
		return &Response{Text: usage}, nil
	}

	var id int64
	if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil {
		return &Response{Text: "Invalid channel ID"}, nil
	}

	// The channel decides where files go, so only its owner may change it.
	channel, err := a.db.GetChannel(id)
	if err != nil || channel.MessengerID != messengerID || channel.UserID != userID {
		return &Response{Text: "Channel not found"}, nil
	}

	if len(args) == 1 {
		state := "off"
		if channel.AutoDownload {
			state = "on"
		}
		return &Response{Text: fmt.Sprintf("Auto-download for %s is %s\n\n%s", channel.Name, state, usage)}, nil
	}

	switch strings.ToLower(args[1]) {
	case "on":
		// Record the videos that exist now first: they are the back catalogue,
		// and only videos that appear later are downloaded.
		if err := a.storeChannelVideos(ctx, *channel); err != nil {
			return &Response{Text: fmt.Sprintf("Failed to read the videos of %s: %v\nAuto-download was not enabled", channel.Name, err)}, nil
		}
		if err := a.db.SetChannelAuto(id, true, messengerID, chatID); err != nil {
			return &Response{Text: fmt.Sprintf("Failed to enable auto-download: %v", err)}, nil
		}
		return &Response{Text: fmt.Sprintf("Auto-download is on for %s\nNew videos will be downloaded and sent to this chat", channel.Name)}, nil
	case "off":
		if err := a.db.SetChannelAuto(id, false, "", 0); err != nil {
			return &Response{Text: fmt.Sprintf("Failed to disable auto-download: %v", err)}, nil
		}
		return &Response{Text: fmt.Sprintf("Auto-download is off for %s", channel.Name)}, nil
	}
	return &Response{Text: usage}, nil
}

// RunSubscriptionPoller checks the channels with auto-download every interval
// until ctx is cancelled. Downloads it starts run on ctx.
func (a *Agent) RunSubscriptionPoller(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := a.pollOnce(ctx); n > 0 {
				log.Printf("subscription poll: queued %d download(s)", n)
			}
		}
	}
}

// pollOnce stores the latest videos of every auto-download channel and queues
// downloads for the ones not handled yet. It returns how many it queued.
//
// It holds the shared refresh slot while it works, so it never overlaps a
// RefreshAll run; when a refresh is already running it skips the tick. It does
// not touch last_check, which /new relies on.
func (a *Agent) pollOnce(ctx context.Context) int {
	a.refreshMu.Lock()
	if a.refresh != nil {
		a.refreshMu.Unlock()
		log.Printf("subscription poll: a refresh is running, skipping this tick")
		return 0
	}
	run := &refreshRun{done: make(chan struct{}), poll: true}
	a.refresh = run
	a.refreshMu.Unlock()

	defer func() {
		a.refreshMu.Lock()
		a.refresh = nil
		a.refreshMu.Unlock()
		close(run.done)
	}()

	channels, err := a.db.ListAllChannels()
	if err != nil {
		log.Printf("subscription poll: list channels: %v", err)
		return 0
	}

	fetchCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()

	queued := 0
	for _, ch := range channels {
		if !ch.AutoDownload {
			continue
		}
		if err := a.storeChannelVideos(fetchCtx, ch); err != nil {
			// Videos stored by an earlier /new may still be waiting.
			log.Printf("subscription poll: %s: %v", ch.Name, err)
		}
		queued += a.queueAutoDownloads(ctx, ch)
	}
	return queued
}

// queueAutoDownloads starts a download, to be sent to the chat that enabled
// auto-download, for each video of the channel not handled yet.
func (a *Agent) queueAutoDownloads(ctx context.Context, ch db.Channel) int {
	videos, err := a.db.ListAutoCandidates(ch.ID)
	if err != nil {
		log.Printf("subscription poll: %s: list new videos: %v", ch.Name, err)
		return 0
	}

	queued := 0
	for _, v := range videos {
		d := &db.Download{
			URL:         v.URL,
			Title:       v.Title,
			ChannelName: ch.Name,
			Status:      "pending",
			MessengerID: ch.AutoMessenger,
			UserID:      ch.UserID,
			ChatID:      ch.AutoChatID,
			SendFile:    true,
		}
		if err := a.db.CreateDownload(d); err != nil {
			log.Printf("subscription poll: %s: save download: %v", ch.Name, err)
			continue
		}
		// Mark before the download runs: a video whose download fails is not
		// retried on every tick.
		if err := a.db.MarkVideoAutoSeen(v.ID); err != nil {
			log.Printf("subscription poll: %s: mark video: %v", ch.Name, err)
		}
		a.ProcessDownload(ctx, d)
		queued++
	}
	return queued
}

// Retention limits the downloaded files kept on disk. Zero MaxBytes or MaxAge
// means no limit of that kind; files outside Dir are never touched.
type Retention struct {
	Dir      string
	MaxBytes int64
	MaxAge   time.Duration
}

func (r Retention) enabled() bool {
	return r.Dir != "" && (r.MaxBytes > 0 || r.MaxAge > 0)
}

func (a *Agent) SetRetention(r Retention) {
	a.retentionMu.Lock()
	defer a.retentionMu.Unlock()
	a.retention = r
}

type diskFile struct {
	path    string
	size    int64
	modTime time.Time
	ids     []int64
}

// CleanupDownloads deletes completed downloads' files that are older than
// MaxAge, then the oldest ones until the rest fits in MaxBytes. The rows stay
// as history with an empty output_path. It returns how many files it deleted.
func (a *Agent) CleanupDownloads() (int, error) {
	a.retentionMu.Lock()
	defer a.retentionMu.Unlock()

	r := a.retention
	if !r.enabled() {
		return 0, nil
	}

	downloads, err := a.db.ListDownloadsByStatus("completed")
	if err != nil {
		return 0, err
	}

	byPath := make(map[string]*diskFile)
	var files []*diskFile
	for _, d := range downloads {
		if d.OutputPath == "" || !within(r.Dir, d.OutputPath) {
			continue
		}
		if f, ok := byPath[d.OutputPath]; ok {
			f.ids = append(f.ids, d.ID)
			continue
		}

		info, err := os.Stat(d.OutputPath)
		if err != nil {
			if os.IsNotExist(err) {
				// Already gone: forget the path so it is not looked up again.
				if err := a.db.ClearDownloadOutputPath(d.ID); err != nil {
					log.Printf("cleanup: clear path of download %d: %v", d.ID, err)
				}
			}
			continue
		}
		f := &diskFile{path: d.OutputPath, size: info.Size(), modTime: info.ModTime(), ids: []int64{d.ID}}
		byPath[d.OutputPath] = f
		files = append(files, f)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })

	var total int64
	for _, f := range files {
		total += f.size
	}

	removed := 0
	for _, f := range files {
		tooOld := r.MaxAge > 0 && time.Since(f.modTime) > r.MaxAge
		tooBig := r.MaxBytes > 0 && total > r.MaxBytes
		if !tooOld && !tooBig {
			continue
		}

		if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
			log.Printf("cleanup: remove %s: %v", f.path, err)
			continue
		}
		total -= f.size
		removed++
		for _, id := range f.ids {
			if err := a.db.ClearDownloadOutputPath(id); err != nil {
				log.Printf("cleanup: clear path of download %d: %v", id, err)
			}
		}
	}
	return removed, nil
}

// within reports whether path is inside dir.
func within(dir, path string) bool {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (a *Agent) cleanupAfterDownload() {
	if n, err := a.CleanupDownloads(); err != nil {
		log.Printf("cleanup: %v", err)
	} else if n > 0 {
		log.Printf("cleanup: removed %d old file(s)", n)
	}
}

// RunCleanup runs CleanupDownloads every interval until ctx is cancelled, for
// files that age out while no download is running.
func (a *Agent) RunCleanup(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.cleanupAfterDownload()
		}
	}
}
