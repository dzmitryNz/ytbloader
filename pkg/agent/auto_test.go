package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mitry/ytbloader/pkg/db"
	"github.com/mitry/ytbloader/pkg/downloader"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func completedCount(t *testing.T, database *db.DB) int {
	t.Helper()
	list, err := database.ListDownloadsByStatus("completed")
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func allDownloads(t *testing.T, database *db.DB) []db.Download {
	t.Helper()
	list, err := database.ListDownloads()
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func vi(url string) downloader.VideoInfo { return downloader.VideoInfo{URL: url, Title: url} }

// autoAgent returns an agent with an auto-download channel "chA" that already
// stores videos v1 and v2, enabled from chat 55 by user 1 over "web".
func autoAgent(t *testing.T, fake *fakeDownloader) (*Agent, *db.DB, *db.Channel) {
	t.Helper()
	fake.videos = map[string][]downloader.VideoInfo{"chA": {vi("v2"), vi("v1")}}
	a, database := newTestAgent(t, fake)
	ch := addChannel(t, database, "chA", "A")

	resp, err := a.ProcessMessage(context.Background(), "web", 1, 55, fmt.Sprintf("/auto %d on", ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := database.GetChannel(ch.ID); !got.AutoDownload {
		t.Fatalf("/auto on did not enable the channel: %q", resp.Text)
	}
	return a, database, ch
}

func TestAutoCommandEnablesChannelForTheChat(t *testing.T) {
	_, database, ch := autoAgent(t, &fakeDownloader{})

	got, err := database.GetChannel(ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoDownload || got.AutoMessenger != "web" || got.AutoChatID != 55 {
		t.Errorf("channel = %+v, want auto on for web chat 55", got)
	}
}

func TestAutoCommandOffDisablesChannel(t *testing.T) {
	a, database, ch := autoAgent(t, &fakeDownloader{})

	if _, err := a.ProcessMessage(context.Background(), "web", 1, 55, fmt.Sprintf("/auto %d off", ch.ID)); err != nil {
		t.Fatal(err)
	}

	if got, _ := database.GetChannel(ch.ID); got.AutoDownload {
		t.Errorf("channel still auto after off: %+v", got)
	}
}

func TestAutoCommandWithoutOnOffShowsStatus(t *testing.T) {
	a, _, ch := autoAgent(t, &fakeDownloader{})

	resp, err := a.ProcessMessage(context.Background(), "web", 1, 55, fmt.Sprintf("/auto %d", ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(resp.Text), "on") {
		t.Errorf("status text = %q, want it to say auto-download is on", resp.Text)
	}
}

func TestAutoCommandRejectsBadInput(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	ch := addChannel(t, database, "chA", "A")

	for _, msg := range []string{"/auto", "/auto x on", "/auto 999 on", fmt.Sprintf("/auto %d maybe", ch.ID)} {
		resp, err := a.ProcessMessage(context.Background(), "web", 1, 55, msg)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Text == "" {
			t.Errorf("%q: empty reply", msg)
		}
		if got, _ := database.GetChannel(ch.ID); got.AutoDownload {
			t.Errorf("%q enabled the channel", msg)
		}
	}
}

func TestAutoCommandIgnoresChannelsOfOtherUsers(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	ch := addChannel(t, database, "chA", "A") // owned by user 1 on "web"

	resp, err := a.ProcessMessage(context.Background(), "web", 2, 99, fmt.Sprintf("/auto %d on", ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Channel not found" {
		t.Errorf("text = %q, want %q", resp.Text, "Channel not found")
	}
	if got, _ := database.GetChannel(ch.ID); got.AutoDownload {
		t.Error("another user turned auto-download on")
	}
}

func TestAutoCommandOnFailsWhenVideosCannotBeFetched(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{videosErr: map[string]error{"chA": errors.New("boom")}})
	ch := addChannel(t, database, "chA", "A")

	if _, err := a.ProcessMessage(context.Background(), "web", 1, 55, fmt.Sprintf("/auto %d on", ch.ID)); err != nil {
		t.Fatal(err)
	}

	// Without the baseline fetch the whole back catalogue would be downloaded.
	if got, _ := database.GetChannel(ch.ID); got.AutoDownload {
		t.Error("auto-download enabled although the current videos could not be recorded")
	}
}

func TestPollEnqueuesOnlyNewNotDownloadedVideosOnce(t *testing.T) {
	fake := &fakeDownloader{results: []error{nil, nil, nil}}
	a, database, _ := autoAgent(t, fake)
	fake.videos["chA"] = []downloader.VideoInfo{vi("v4"), vi("v3"), vi("v2"), vi("v1")}
	if err := database.MarkVideoDownloaded("v4"); err != nil {
		t.Fatal(err)
	}

	if n := a.pollOnce(context.Background()); n != 1 {
		t.Fatalf("first poll enqueued %d, want 1", n)
	}
	waitFor(t, "the download to complete", func() bool { return completedCount(t, database) == 1 })

	downloads := allDownloads(t, database)
	if len(downloads) != 1 {
		t.Fatalf("downloads = %d, want 1", len(downloads))
	}
	d := downloads[0]
	if d.URL != "v3" || d.MessengerID != "web" || d.ChatID != 55 || d.UserID != 1 || !d.SendFile {
		t.Errorf("download = %+v, want v3 sent to web chat 55 for user 1", d)
	}

	if n := a.pollOnce(context.Background()); n != 0 {
		t.Errorf("second poll enqueued %d, want 0", n)
	}
	if got := len(allDownloads(t, database)); got != 1 {
		t.Errorf("downloads after second poll = %d, want 1", got)
	}
}

func TestPollDoesNotRetryFailedDownloadsEveryTick(t *testing.T) {
	fake := &fakeDownloader{results: []error{errors.New("video unavailable")}}
	a, database, _ := autoAgent(t, fake)
	fake.videos["chA"] = []downloader.VideoInfo{vi("v3"), vi("v2"), vi("v1")}

	if n := a.pollOnce(context.Background()); n != 1 {
		t.Fatalf("first poll enqueued %d, want 1", n)
	}
	waitFor(t, "the download to fail", func() bool {
		list, _ := database.ListDownloadsByStatus("failed")
		return len(list) == 1
	})

	if n := a.pollOnce(context.Background()); n != 0 {
		t.Errorf("second poll enqueued %d, want 0 (a failed video is not retried on every tick)", n)
	}
}

func TestPollLeavesChannelsWithoutAutoAlone(t *testing.T) {
	fake := &fakeDownloader{results: []error{nil}, videos: map[string][]downloader.VideoInfo{
		"chB": {vi("b1")},
	}}
	a, database := newTestAgent(t, fake)
	addChannel(t, database, "chB", "B")

	if n := a.pollOnce(context.Background()); n != 0 {
		t.Errorf("poll enqueued %d, want 0", n)
	}
	if got := fake.channelCalls("chB"); got != 0 {
		t.Errorf("poll fetched a channel without auto-download %d times", got)
	}
}

func TestPollSkipsTickWhileARefreshIsRunning(t *testing.T) {
	fake := &fakeDownloader{results: []error{nil}}
	a, _, _ := autoAgent(t, fake)
	before := fake.channelCalls("chA")

	run := &refreshRun{done: make(chan struct{})}
	a.refreshMu.Lock()
	a.refresh = run
	a.refreshMu.Unlock()

	if n := a.pollOnce(context.Background()); n != 0 {
		t.Errorf("poll enqueued %d during a refresh, want 0", n)
	}
	if got := fake.channelCalls("chA"); got != before {
		t.Errorf("poll fetched a channel during a refresh (%d calls, had %d)", got, before)
	}
}

func TestRefreshAllWaitsForARunningPollInsteadOfJoiningIt(t *testing.T) {
	fake := &fakeDownloader{}
	a, _, _ := autoAgent(t, fake)
	addChannel(t, a.db, "chB", "B")
	fake.videos["chB"] = []downloader.VideoInfo{vi("b1")}
	beforeB := fake.channelCalls("chB")

	poll := &refreshRun{done: make(chan struct{}), poll: true}
	a.refreshMu.Lock()
	a.refresh = poll
	a.refreshMu.Unlock()

	result := make(chan RefreshResult, 1)
	go func() { result <- a.RefreshAll(context.Background(), 5*time.Second) }()

	select {
	case res := <-result:
		t.Fatalf("RefreshAll returned %+v while a poll was running; a poll only covers auto channels", res)
	case <-time.After(100 * time.Millisecond):
	}
	if got := fake.channelCalls("chB"); got != beforeB {
		t.Fatalf("RefreshAll refreshed during the poll")
	}

	a.refreshMu.Lock()
	a.refresh = nil
	a.refreshMu.Unlock()
	close(poll.done)

	select {
	case res := <-result:
		if res.Channels != 2 || res.Running {
			t.Errorf("result = %+v, want a full run over 2 channels", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshAll did not run after the poll ended")
	}
	if got := fake.channelCalls("chB"); got != beforeB+1 {
		t.Errorf("chB fetched %d times, want %d", got, beforeB+1)
	}
}

func TestRunSubscriptionPollerPollsOnEveryTickUntilCancelled(t *testing.T) {
	fake := &fakeDownloader{}
	a, _, _ := autoAgent(t, fake)
	before := fake.channelCalls("chA")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.RunSubscriptionPoller(ctx, 10*time.Millisecond)
		close(done)
	}()

	waitFor(t, "two polls", func() bool { return fake.channelCalls("chA") >= before+2 })
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("poller did not stop after the context was cancelled")
	}
}

// --- disk cleanup ---

// addCompletedFile writes a file of size bytes aged by age and records a
// completed download that points at it.
func addCompletedFile(t *testing.T, database *db.DB, dir, name string, size int, age time.Duration) (*db.Download, string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	d := &db.Download{URL: "https://y/" + name, Status: "pending"}
	if err := database.CreateDownload(d); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateDownloadResult(d.ID, name, path, "completed", int64(size)); err != nil {
		t.Fatal(err)
	}
	return d, path
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestCleanupRemovesFilesOlderThanMaxAge(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	dir := t.TempDir()
	old, oldPath := addCompletedFile(t, database, dir, "old.mp3", 10, 10*24*time.Hour)
	fresh, freshPath := addCompletedFile(t, database, dir, "fresh.mp3", 10, time.Hour)
	a.SetRetention(Retention{Dir: dir, MaxAge: 7 * 24 * time.Hour})

	removed, err := a.CleanupDownloads()
	if err != nil {
		t.Fatal(err)
	}

	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if fileExists(oldPath) || !fileExists(freshPath) {
		t.Errorf("old exists = %v, fresh exists = %v, want false, true", fileExists(oldPath), fileExists(freshPath))
	}
	gotOld, _ := database.GetDownload(old.ID)
	if gotOld.OutputPath != "" || gotOld.Status != "completed" {
		t.Errorf("old row = %+v, want empty path and status completed", gotOld)
	}
	gotFresh, _ := database.GetDownload(fresh.ID)
	if gotFresh.OutputPath != freshPath {
		t.Errorf("fresh row path = %q, want it untouched", gotFresh.OutputPath)
	}
}

func TestCleanupRemovesOldestFilesUntilUnderMaxBytes(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	dir := t.TempDir()
	_, oldest := addCompletedFile(t, database, dir, "a.mp3", 100, 3*24*time.Hour)
	_, middle := addCompletedFile(t, database, dir, "b.mp3", 100, 2*24*time.Hour)
	_, newest := addCompletedFile(t, database, dir, "c.mp3", 100, 24*time.Hour)
	a.SetRetention(Retention{Dir: dir, MaxBytes: 150})

	removed, err := a.CleanupDownloads()
	if err != nil {
		t.Fatal(err)
	}

	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if fileExists(oldest) || fileExists(middle) || !fileExists(newest) {
		t.Errorf("exists: oldest %v, middle %v, newest %v; want false, false, true", fileExists(oldest), fileExists(middle), fileExists(newest))
	}
}

func TestCleanupWithoutLimitsKeepsEverything(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	dir := t.TempDir()
	_, path := addCompletedFile(t, database, dir, "a.mp3", 100, 400*24*time.Hour)
	a.SetRetention(Retention{Dir: dir})

	removed, err := a.CleanupDownloads()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 || !fileExists(path) {
		t.Errorf("removed = %d, exists = %v, want 0, true", removed, fileExists(path))
	}
}

func TestCleanupNeverTouchesFilesOutsideTheDownloadsDir(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	dir := t.TempDir()
	d, outside := addCompletedFile(t, database, t.TempDir(), "elsewhere.mp3", 100, 400*24*time.Hour)
	a.SetRetention(Retention{Dir: dir, MaxAge: time.Hour, MaxBytes: 1})

	removed, err := a.CleanupDownloads()
	if err != nil {
		t.Fatal(err)
	}

	if removed != 0 || !fileExists(outside) {
		t.Errorf("removed = %d, outside file exists = %v, want 0, true", removed, fileExists(outside))
	}
	if got, _ := database.GetDownload(d.ID); got.OutputPath != outside {
		t.Errorf("row path = %q, want it untouched", got.OutputPath)
	}
}

func TestCleanupClearsPathOfFilesThatAreAlreadyGone(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	dir := t.TempDir()
	d, path := addCompletedFile(t, database, dir, "gone.mp3", 10, time.Hour)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	a.SetRetention(Retention{Dir: dir, MaxAge: 24 * time.Hour})

	if _, err := a.CleanupDownloads(); err != nil {
		t.Fatal(err)
	}

	if got, _ := database.GetDownload(d.ID); got.OutputPath != "" {
		t.Errorf("row path = %q, want it cleared", got.OutputPath)
	}
}

func TestCleanupCountsAFileSharedByTwoRowsOnce(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	dir := t.TempDir()
	_, path := addCompletedFile(t, database, dir, "same.mp3", 100, 48*time.Hour)
	second := &db.Download{URL: "https://y/again", Status: "pending"}
	if err := database.CreateDownload(second); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateDownloadResult(second.ID, "again", path, "completed", 100); err != nil {
		t.Fatal(err)
	}
	_, other := addCompletedFile(t, database, dir, "other.mp3", 100, time.Hour)
	// 200 real bytes on disk; counting the shared file twice would make it 300
	// and wrongly remove the newer file as well.
	a.SetRetention(Retention{Dir: dir, MaxBytes: 250})

	if _, err := a.CleanupDownloads(); err != nil {
		t.Fatal(err)
	}

	if !fileExists(path) || !fileExists(other) {
		t.Errorf("shared exists = %v, other exists = %v, want both kept (200 <= 250 bytes)", fileExists(path), fileExists(other))
	}
}

func TestCleanupRunsAfterEachDownload(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh.mp3")
	if err := os.WriteFile(fresh, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDownloader{results: []error{nil}, outputPath: fresh}
	a, database := newTestAgent(t, fake)
	_, oldPath := addCompletedFile(t, database, dir, "old.mp3", 10, 10*24*time.Hour)
	a.SetRetention(Retention{Dir: dir, MaxAge: 24 * time.Hour})

	d := newDownload(t, database)
	a.processDownload(context.Background(), d)

	if fileExists(oldPath) {
		t.Error("old file survived a cleanup that should run after each download")
	}
	if !fileExists(fresh) {
		t.Error("the file that was just downloaded was removed")
	}
}
