package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mitry/ytbloader/pkg/db"
	"github.com/mitry/ytbloader/pkg/downloader"
)

type fakeDownloader struct {
	mu      sync.Mutex
	calls   int
	results []error // one entry per call; nil means success
	onCall  func(call int)
}

func (f *fakeDownloader) Download(ctx context.Context, req *downloader.DownloadRequest) (*downloader.DownloadResult, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()

	if f.onCall != nil {
		f.onCall(call)
	}
	if call > len(f.results) {
		return nil, errors.New("unexpected extra call")
	}
	if err := f.results[call-1]; err != nil {
		return nil, err
	}
	return &downloader.DownloadResult{OutputPath: "/tmp/song.mp3", Title: "song", Size: 7}, nil
}

func (f *fakeDownloader) GetInfo(ctx context.Context, url string) (string, string, error) {
	return "", "", nil
}

func (f *fakeDownloader) GetChannelInfo(ctx context.Context, url string) (*downloader.ChannelInfo, error) {
	return nil, nil
}

func (f *fakeDownloader) GetChannelVideos(ctx context.Context, channelURL string, limit int) ([]downloader.VideoInfo, error) {
	return nil, nil
}

func newTestAgent(t *testing.T, fake *fakeDownloader) (*Agent, *db.DB) {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	a := New(database, fake)
	a.retryDelays = []time.Duration{0, 0, 0}
	return a, database
}

func newDownload(t *testing.T, database *db.DB) *db.Download {
	t.Helper()
	d := &db.Download{URL: "https://y/v", Status: "pending"}
	if err := database.CreateDownload(d); err != nil {
		t.Fatal(err)
	}
	return d
}

func statusOf(t *testing.T, database *db.DB, id int64) *db.Download {
	t.Helper()
	d, err := database.GetDownload(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestProcessDownloadRetriesTransientError(t *testing.T) {
	fake := &fakeDownloader{results: []error{
		errors.New("failed to download: exit status 1\nERROR: HTTP Error 503: Service Unavailable"),
		nil,
	}}
	a, database := newTestAgent(t, fake)
	d := newDownload(t, database)

	a.processDownload(context.Background(), d)

	if fake.calls != 2 {
		t.Errorf("Download calls = %d, want 2", fake.calls)
	}
	got := statusOf(t, database, d.ID)
	if got.Status != "completed" {
		t.Errorf("status = %q (error %q), want completed", got.Status, got.ErrorMsg)
	}
}

func TestProcessDownloadDoesNotRetryPermanentError(t *testing.T) {
	for _, msg := range []string{
		"failed to download: exit status 1\nERROR: [youtube] abc: Video unavailable",
		"failed to download: exit status 1\nERROR: unable to download video data: HTTP Error 403: Forbidden",
	} {
		fake := &fakeDownloader{results: []error{errors.New(msg), nil}}
		a, database := newTestAgent(t, fake)
		d := newDownload(t, database)

		a.processDownload(context.Background(), d)

		if fake.calls != 1 {
			t.Errorf("%q: Download calls = %d, want 1", msg, fake.calls)
		}
		got := statusOf(t, database, d.ID)
		if got.Status != "failed" || got.ErrorMsg != msg {
			t.Errorf("%q: status/error = %q/%q, want failed with the message", msg, got.Status, got.ErrorMsg)
		}
	}
}

func TestProcessDownloadGivesUpAfterAllRetries(t *testing.T) {
	transient := errors.New("failed to download: exit status 1\nERROR: Connection reset by peer")
	fake := &fakeDownloader{results: []error{transient, transient, transient, transient, nil}}
	a, database := newTestAgent(t, fake)
	d := newDownload(t, database)

	a.processDownload(context.Background(), d)

	if want := 1 + len(a.retryDelays); fake.calls != want {
		t.Errorf("Download calls = %d, want %d", fake.calls, want)
	}
	if got := statusOf(t, database, d.ID); got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
}

func TestProcessDownloadCancelDuringBackoffStopsRetrying(t *testing.T) {
	transient := errors.New("failed to download: exit status 1\nERROR: HTTP Error 502: Bad Gateway")
	fake := &fakeDownloader{results: []error{transient, nil}}
	a, database := newTestAgent(t, fake)
	a.retryDelays = []time.Duration{time.Hour}
	d := newDownload(t, database)

	ctx, cancel := context.WithCancel(context.Background())
	fake.onCall = func(int) {
		time.AfterFunc(50*time.Millisecond, cancel)
	}

	done := make(chan struct{})
	go func() {
		a.processDownload(ctx, d)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processDownload kept waiting after the context was cancelled")
	}

	if fake.calls != 1 {
		t.Errorf("Download calls = %d, want 1", fake.calls)
	}
	if got := statusOf(t, database, d.ID); got.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
}

func TestIsTransient(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"ERROR: HTTP Error 500: Internal Server Error", true},
		{"ERROR: HTTP Error 503: Service Unavailable", true},
		{"ERROR: Connection reset by peer", true},
		{"ERROR: The read operation timed out", true},
		{"ERROR: Temporary failure in name resolution", true},
		{"ERROR: Video unavailable", false},
		{"ERROR: HTTP Error 403: Forbidden", false},
		{"ERROR: HTTP Error 404: Not Found", false},
		{"ERROR: Private video. Sign in if you've been granted access", false},
		{"ERROR: unable to download video data: HTTP Error 403: Forbidden", false},
		{"exit status 1", false},
	}
	for _, tt := range tests {
		if got := isTransient(errors.New(tt.msg)); got != tt.want {
			t.Errorf("isTransient(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
	if isTransient(nil) {
		t.Error("isTransient(nil) = true")
	}
}
