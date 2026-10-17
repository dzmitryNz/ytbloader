package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := New(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func newChannel(t *testing.T, d *DB, url string) *Channel {
	t.Helper()
	c := &Channel{URL: url, Name: "chan", MessengerID: "telegram", UserID: 1, ChatID: 10}
	if err := d.CreateChannel(c); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	return c
}

func TestDownloadsCRUD(t *testing.T) {
	d := newTestDB(t)

	dl := &Download{URL: "https://y/1", Status: "pending", MessengerID: "telegram", UserID: 1, ChatID: 10, SendFile: true}
	if err := d.CreateDownload(dl); err != nil {
		t.Fatalf("CreateDownload: %v", err)
	}
	if dl.ID == 0 {
		t.Fatal("CreateDownload did not set ID")
	}

	got, err := d.GetDownload(dl.ID)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if got.URL != "https://y/1" || got.Status != "pending" || !got.SendFile || got.ChatID != 10 {
		t.Errorf("unexpected download: %+v", got)
	}

	if err := d.UpdateDownloadProgress(dl.ID, 42); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateDownloadStatus(dl.ID, "downloading"); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetDownload(dl.ID)
	if got.Progress != 42 || got.Status != "downloading" {
		t.Errorf("progress/status = %d/%s", got.Progress, got.Status)
	}

	if err := d.UpdateDownloadError(dl.ID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetDownload(dl.ID)
	if got.Status != "failed" || got.ErrorMsg != "boom" {
		t.Errorf("status/error = %s/%s", got.Status, got.ErrorMsg)
	}

	if err := d.UpdateDownloadResult(dl.ID, "Song", "/tmp/song.mp3", "completed", 1234); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetDownload(dl.ID)
	if got.Title != "Song" || got.OutputPath != "/tmp/song.mp3" || got.Size != 1234 || got.Status != "completed" || got.ErrorMsg != "" {
		t.Errorf("result not stored, or error not cleared: %+v", got)
	}

	list, err := d.ListDownloads()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListDownloads = %v, %v", list, err)
	}

	if err := d.DeleteDownload(dl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetDownload(dl.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetDownload after delete: err = %v, want sql.ErrNoRows", err)
	}
}

func TestListDownloadsByStatus(t *testing.T) {
	d := newTestDB(t)
	for i, status := range []string{"pending", "downloading", "completed", "failed"} {
		if err := d.CreateDownload(&Download{URL: "https://y/" + string(rune('a'+i)), Status: status}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := d.ListDownloadsByStatus("pending", "downloading")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d downloads, want 2", len(got))
	}
	for _, dl := range got {
		if dl.Status != "pending" && dl.Status != "downloading" {
			t.Errorf("unexpected status %q", dl.Status)
		}
	}

	none, err := d.ListDownloadsByStatus()
	if err != nil || len(none) != 0 {
		t.Errorf("no statuses: got %v, %v; want empty", none, err)
	}
}

func TestCreateChannelIsIdempotentPerUser(t *testing.T) {
	d := newTestDB(t)
	first := newChannel(t, d, "https://y/chan")
	second := newChannel(t, d, "https://y/chan")
	if first.ID != second.ID {
		t.Errorf("same channel got ids %d and %d", first.ID, second.ID)
	}

	got, err := d.GetChannelByURL("https://y/chan", "telegram", 1)
	if err != nil || got.ID != first.ID {
		t.Errorf("GetChannelByURL = %+v, %v", got, err)
	}
}

func TestDeleteChannelRemovesItsVideos(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")
	other := newChannel(t, d, "https://y/other")
	for _, url := range []string{"https://y/v1", "https://y/v2"} {
		if err := d.UpsertVideo(&Video{ChannelID: c.ID, URL: url, Title: url}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.UpsertVideo(&Video{ChannelID: other.ID, URL: "https://y/v1", Title: "kept"}); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteChannel(c.ID); err != nil {
		t.Fatalf("DeleteChannel with videos: %v", err)
	}

	if n, _ := d.CountVideos(c.ID); n != 0 {
		t.Errorf("videos left for deleted channel: %d", n)
	}
	if _, err := d.GetChannel(c.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetChannel after delete: err = %v, want sql.ErrNoRows", err)
	}
	if n, _ := d.CountVideos(other.ID); n != 1 {
		t.Errorf("other channel lost its video: %d", n)
	}
}

func TestUpsertVideoIsIdempotent(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")

	v1 := &Video{ChannelID: c.ID, URL: "https://y/v1", Title: "old", Duration: 10, Position: 1}
	if err := d.UpsertVideo(v1); err != nil {
		t.Fatal(err)
	}
	v2 := &Video{ChannelID: c.ID, URL: "https://y/v1", Title: "new", Duration: 20, Position: 3}
	if err := d.UpsertVideo(v2); err != nil {
		t.Fatal(err)
	}

	if v1.ID != v2.ID {
		t.Errorf("ids differ: %d vs %d", v1.ID, v2.ID)
	}
	if n, _ := d.CountVideos(c.ID); n != 1 {
		t.Errorf("CountVideos = %d, want 1", n)
	}
	videos, err := d.ListAllVideos(c.ID, 10, 0)
	if err != nil || len(videos) != 1 {
		t.Fatalf("ListAllVideos = %v, %v", videos, err)
	}
	if videos[0].Title != "new" || videos[0].Duration != 20 || videos[0].Position != 3 {
		t.Errorf("video not updated: %+v", videos[0])
	}
}

func TestListNewVideosAndCountSince(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")
	for i, url := range []string{"https://y/old", "https://y/new"} {
		if err := d.UpsertVideo(&Video{ChannelID: c.ID, URL: url, Title: url, Position: i + 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.conn.Exec(`UPDATE videos SET created_at = '2020-01-01 00:00:00' WHERE url = 'https://y/old'`); err != nil {
		t.Fatal(err)
	}

	since := time.Now().UTC().Add(-time.Hour)
	got, err := d.ListNewVideos(c.ID, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "https://y/new" {
		t.Errorf("ListNewVideos = %+v, want only https://y/new", got)
	}
	if n, err := d.CountNewVideosSince(c.ID, since); err != nil || n != 1 {
		t.Errorf("CountNewVideosSince = %d, %v; want 1", n, err)
	}
	if n, _ := d.CountNewVideosSince(c.ID, time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)); n != 2 {
		t.Errorf("CountNewVideosSince(2019) = %d, want 2", n)
	}
}

func TestDownloadedVideoURLs(t *testing.T) {
	d := newTestDB(t)

	if err := d.CreateDownload(&Download{URL: "https://y/completed", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateDownload(&Download{URL: "https://y/pending", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkVideoDownloaded("https://y/marked"); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkVideoDownloaded("https://y/marked"); err != nil {
		t.Fatalf("marking twice must be a no-op: %v", err)
	}

	urls, err := d.GetDownloadedVideoURLs()
	if err != nil {
		t.Fatal(err)
	}
	if !urls["https://y/completed"] || !urls["https://y/marked"] || urls["https://y/pending"] || len(urls) != 2 {
		t.Errorf("urls = %v", urls)
	}

	if err := d.UnmarkVideoDownloaded("https://y/marked"); err != nil {
		t.Fatal(err)
	}
	urls, _ = d.GetDownloadedVideoURLs()
	if urls["https://y/marked"] {
		t.Error("video still marked after UnmarkVideoDownloaded")
	}
}

func TestChannelNewVideosCount(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")

	if err := d.UpdateChannelNewVideosCount(c.ID, 5); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetChannel(c.ID)
	if got.NewVideosCount != 5 {
		t.Errorf("NewVideosCount = %d, want 5", got.NewVideosCount)
	}
	if err := d.ResetChannelNewVideosCount(c.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetChannel(c.ID)
	if got.NewVideosCount != 0 {
		t.Errorf("NewVideosCount after reset = %d, want 0", got.NewVideosCount)
	}
}

func addVideoAt(t *testing.T, d *DB, c *Channel, url string, position int, createdAt string) {
	t.Helper()
	if err := d.UpsertVideo(&Video{ChannelID: c.ID, URL: url, Title: url, Position: position}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.conn.Exec(`UPDATE videos SET created_at = ? WHERE channel_id = ? AND url = ?`, createdAt, c.ID, url); err != nil {
		t.Fatal(err)
	}
}

func ageChannel(t *testing.T, d *DB, c *Channel, createdAt string) {
	t.Helper()
	if _, err := d.conn.Exec(`UPDATE channels SET created_at = ? WHERE id = ?`, createdAt, c.ID); err != nil {
		t.Fatal(err)
	}
}

func recentURLs(t *testing.T, d *DB, since time.Time, limit int) ([]string, int) {
	t.Helper()
	got, total, err := d.ListRecentNewVideos(since, limit)
	if err != nil {
		t.Fatal(err)
	}
	urls := make([]string, 0, len(got))
	for _, v := range got {
		urls = append(urls, v.URL)
	}
	return urls, total
}

func TestListRecentNewVideosSkipsFirstBatchOfFreshSubscription(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")
	if err := d.UpsertVideo(&Video{ChannelID: c.ID, URL: "https://y/first", Title: "first", Position: 1}); err != nil {
		t.Fatal(err)
	}

	urls, total := recentURLs(t, d, time.Now().UTC().Add(-72*time.Hour), 5)
	if len(urls) != 0 || total != 0 {
		t.Errorf("fresh subscription batch listed: %v (total %d)", urls, total)
	}

	ageChannel(t, d, c, time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05"))
	urls, total = recentURLs(t, d, time.Now().UTC().Add(-72*time.Hour), 5)
	if len(urls) != 1 || urls[0] != "https://y/first" || total != 1 {
		t.Errorf("video added after the first batch window = %v (total %d), want it listed", urls, total)
	}
}

func TestListRecentNewVideosRespectsSinceAndDownloads(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")
	ageChannel(t, d, c, "2020-01-01 00:00:00")
	now := time.Now().UTC()
	recent := now.Add(-time.Hour).Format("2006-01-02 15:04:05")
	addVideoAt(t, d, c, "https://y/fresh", 1, recent)
	addVideoAt(t, d, c, "https://y/old", 2, now.Add(-5*24*time.Hour).Format("2006-01-02 15:04:05"))
	addVideoAt(t, d, c, "https://y/completed", 3, recent)
	addVideoAt(t, d, c, "https://y/marked", 4, recent)
	if err := d.CreateDownload(&Download{URL: "https://y/completed", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkVideoDownloaded("https://y/marked"); err != nil {
		t.Fatal(err)
	}

	urls, total := recentURLs(t, d, now.Add(-72*time.Hour), 5)
	if len(urls) != 1 || urls[0] != "https://y/fresh" || total != 1 {
		t.Errorf("urls = %v (total %d), want only https://y/fresh", urls, total)
	}
}

func TestListRecentNewVideosOrderAndLimit(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")
	other := newChannel(t, d, "https://y/other")
	ageChannel(t, d, c, "2020-01-01 00:00:00")
	ageChannel(t, d, other, "2020-01-01 00:00:00")
	now := time.Now().UTC()
	at := func(ago time.Duration) string { return now.Add(-ago).Format("2006-01-02 15:04:05") }
	addVideoAt(t, d, c, "https://y/oldest", 1, at(10*time.Hour))
	addVideoAt(t, d, c, "https://y/newest-b", 2, at(time.Hour))
	addVideoAt(t, d, c, "https://y/newest-a", 1, at(time.Hour))
	addVideoAt(t, d, other, "https://y/middle", 1, at(5*time.Hour))

	urls, total := recentURLs(t, d, now.Add(-72*time.Hour), 2)
	want := []string{"https://y/newest-a", "https://y/newest-b"}
	if len(urls) != 2 || urls[0] != want[0] || urls[1] != want[1] {
		t.Errorf("urls = %v, want %v", urls, want)
	}
	if total != 4 {
		t.Errorf("total = %d, want 4", total)
	}

	got, _, err := d.ListRecentNewVideos(now.Add(-72*time.Hour), 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListRecentNewVideos: %v, %d rows", err, len(got))
	}
	if got[0].ChannelName != "chan" {
		t.Errorf("ChannelName = %q, want chan", got[0].ChannelName)
	}
}

func autoURLs(t *testing.T, d *DB, channelID int64) []string {
	t.Helper()
	videos, err := d.ListAutoCandidates(channelID)
	if err != nil {
		t.Fatalf("ListAutoCandidates: %v", err)
	}
	var urls []string
	for _, v := range videos {
		urls = append(urls, v.URL)
	}
	return urls
}

func TestSetChannelAutoRoundTrip(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")

	got, _ := d.GetChannel(c.ID)
	if got.AutoDownload || got.AutoChatID != 0 || got.AutoMessenger != "" {
		t.Fatalf("new channel has auto settings: %+v", got)
	}

	if err := d.SetChannelAuto(c.ID, true, "discord", 77); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetChannel(c.ID)
	if !got.AutoDownload || got.AutoMessenger != "discord" || got.AutoChatID != 77 {
		t.Errorf("after on: %+v", got)
	}
	all, _ := d.ListAllChannels()
	if len(all) != 1 || !all[0].AutoDownload || all[0].AutoChatID != 77 {
		t.Errorf("ListAllChannels = %+v", all)
	}
	byURL, _ := d.GetChannelByURL("https://y/chan", "telegram", 1)
	if byURL == nil || !byURL.AutoDownload {
		t.Errorf("GetChannelByURL = %+v", byURL)
	}
	listed, _ := d.ListChannels("telegram", 1)
	if len(listed) != 1 || !listed[0].AutoDownload {
		t.Errorf("ListChannels = %+v", listed)
	}

	if err := d.SetChannelAuto(c.ID, false, "", 0); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetChannel(c.ID)
	if got.AutoDownload {
		t.Errorf("after off: %+v", got)
	}
}

func TestSetChannelAutoOnBaselinesExistingVideos(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")
	for i, u := range []string{"https://y/old1", "https://y/old2"} {
		if err := d.UpsertVideo(&Video{ChannelID: c.ID, URL: u, Position: i + 1}); err != nil {
			t.Fatal(err)
		}
	}

	if err := d.SetChannelAuto(c.ID, true, "telegram", 10); err != nil {
		t.Fatal(err)
	}
	if got := autoURLs(t, d, c.ID); len(got) != 0 {
		t.Fatalf("back catalogue is a candidate after enabling: %v", got)
	}

	fresh := &Video{ChannelID: c.ID, URL: "https://y/fresh", Position: 1}
	if err := d.UpsertVideo(fresh); err != nil {
		t.Fatal(err)
	}
	if got := autoURLs(t, d, c.ID); len(got) != 1 || got[0] != "https://y/fresh" {
		t.Fatalf("candidates = %v, want the fresh video", got)
	}

	if err := d.MarkVideoAutoSeen(fresh.ID); err != nil {
		t.Fatal(err)
	}
	if got := autoURLs(t, d, c.ID); len(got) != 0 {
		t.Errorf("candidates after marking seen = %v, want none", got)
	}
}

func TestListAutoCandidatesSkipsDownloadedAndInFlight(t *testing.T) {
	d := newTestDB(t)
	c := newChannel(t, d, "https://y/chan")
	if err := d.SetChannelAuto(c.ID, true, "telegram", 10); err != nil {
		t.Fatal(err)
	}
	for i, u := range []string{"https://y/completed", "https://y/marked", "https://y/pending", "https://y/downloading", "https://y/failed", "https://y/plain"} {
		if err := d.UpsertVideo(&Video{ChannelID: c.ID, URL: u, Position: i + 1}); err != nil {
			t.Fatal(err)
		}
	}
	for url, status := range map[string]string{
		"https://y/completed":   "completed",
		"https://y/pending":     "pending",
		"https://y/downloading": "downloading",
		"https://y/failed":      "failed",
	} {
		if err := d.CreateDownload(&Download{URL: url, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.MarkVideoDownloaded("https://y/marked"); err != nil {
		t.Fatal(err)
	}

	got := autoURLs(t, d, c.ID)
	want := map[string]bool{"https://y/failed": true, "https://y/plain": true}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for _, u := range got {
		if !want[u] {
			t.Errorf("unexpected candidate %q", u)
		}
	}
}

func TestClearDownloadOutputPathKeepsStatus(t *testing.T) {
	d := newTestDB(t)
	dl := &Download{URL: "https://y/1", Status: "pending"}
	if err := d.CreateDownload(dl); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateDownloadResult(dl.ID, "t", "/data/a.mp3", "completed", 5); err != nil {
		t.Fatal(err)
	}

	if err := d.ClearDownloadOutputPath(dl.ID); err != nil {
		t.Fatal(err)
	}

	got, _ := d.GetDownload(dl.ID)
	if got.OutputPath != "" || got.Status != "completed" || got.Size != 5 {
		t.Errorf("download = %+v, want empty path, completed, size kept", got)
	}
}
