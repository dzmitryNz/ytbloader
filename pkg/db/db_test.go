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
