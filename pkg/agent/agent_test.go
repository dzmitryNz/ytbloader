package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

	videos     map[string][]downloader.VideoInfo
	videosErr  map[string]error
	videoGate  chan struct{}
	videoCalls map[string]int
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
	f.mu.Lock()
	if f.videoCalls == nil {
		f.videoCalls = make(map[string]int)
	}
	f.videoCalls[channelURL]++
	gate := f.videoGate
	f.mu.Unlock()

	if gate != nil {
		<-gate
	}
	if err := f.videosErr[channelURL]; err != nil {
		return nil, err
	}
	return f.videos[channelURL], nil
}

func (f *fakeDownloader) channelCalls(url string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.videoCalls[url]
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

func addChannel(t *testing.T, database *db.DB, url, name string) *db.Channel {
	t.Helper()
	c := &db.Channel{URL: url, Name: name, MessengerID: "web", UserID: 1}
	if err := database.CreateChannel(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func videoURLs(t *testing.T, database *db.DB, channelID int64) []string {
	t.Helper()
	videos, err := database.GetVideosByChannel(channelID, 50)
	if err != nil {
		t.Fatal(err)
	}
	urls := make([]string, 0, len(videos))
	for _, v := range videos {
		urls = append(urls, v.URL)
	}
	return urls
}

func TestRefreshAllUpsertsEveryChannel(t *testing.T) {
	fake := &fakeDownloader{videos: map[string][]downloader.VideoInfo{
		"chA": {{URL: "a1", Title: "A1"}, {URL: "a2", Title: "A2"}},
		"chB": {{URL: "b1", Title: "B1"}},
	}}
	a, database := newTestAgent(t, fake)
	chA := addChannel(t, database, "chA", "A")
	chB := addChannel(t, database, "chB", "B")

	res := a.RefreshAll(context.Background(), 5*time.Second)

	if res.Channels != 2 || len(res.Failed) != 0 || res.Running {
		t.Errorf("result = %+v, want 2 channels, none failed, not running", res)
	}
	if got := videoURLs(t, database, chA.ID); len(got) != 2 {
		t.Errorf("channel A videos = %v, want 2", got)
	}
	if got := videoURLs(t, database, chB.ID); len(got) != 1 {
		t.Errorf("channel B videos = %v, want 1", got)
	}
}

func TestRefreshAllReportsFailedChannelAndKeepsGoing(t *testing.T) {
	fake := &fakeDownloader{
		videos:    map[string][]downloader.VideoInfo{"chB": {{URL: "b1", Title: "B1"}}},
		videosErr: map[string]error{"chA": errors.New("yt-dlp exploded")},
	}
	a, database := newTestAgent(t, fake)
	addChannel(t, database, "chA", "A")
	chB := addChannel(t, database, "chB", "B")

	res := a.RefreshAll(context.Background(), 5*time.Second)

	if len(res.Failed) != 1 || res.Failed[0] != "A" {
		t.Errorf("Failed = %v, want [A]", res.Failed)
	}
	if got := videoURLs(t, database, chB.ID); len(got) != 1 {
		t.Errorf("channel B videos = %v, want 1", got)
	}
}

func TestRefreshAllKeepsRunningAfterCallerStopsWaiting(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeDownloader{
		videos:    map[string][]downloader.VideoInfo{"chA": {{URL: "a1", Title: "A1"}}},
		videoGate: gate,
	}
	a, database := newTestAgent(t, fake)
	chA := addChannel(t, database, "chA", "A")

	res := a.RefreshAll(context.Background(), 10*time.Millisecond)
	if !res.Running {
		t.Fatalf("result = %+v, want Running", res)
	}
	close(gate)

	deadline := time.Now().Add(5 * time.Second)
	for len(videoURLs(t, database, chA.ID)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the run was cancelled: no videos stored after the gate opened")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRefreshAllCallerContextDoesNotCancelTheRun(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeDownloader{
		videos:    map[string][]downloader.VideoInfo{"chA": {{URL: "a1", Title: "A1"}}},
		videoGate: gate,
	}
	a, database := newTestAgent(t, fake)
	chA := addChannel(t, database, "chA", "A")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res := a.RefreshAll(ctx, time.Minute)
	if !res.Running {
		t.Fatalf("result = %+v, want Running when the caller context ends", res)
	}
	close(gate)

	deadline := time.Now().Add(5 * time.Second)
	for len(videoURLs(t, database, chA.ID)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the run stopped with the caller context")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRefreshAllConcurrentCallsShareOneRun(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeDownloader{
		videos: map[string][]downloader.VideoInfo{
			"chA": {{URL: "a1", Title: "A1"}},
			"chB": {{URL: "b1", Title: "B1"}},
		},
		videoGate: gate,
	}
	a, database := newTestAgent(t, fake)
	addChannel(t, database, "chA", "A")
	addChannel(t, database, "chB", "B")

	results := make(chan RefreshResult, 2)
	go func() { results <- a.RefreshAll(context.Background(), 5*time.Second) }()

	deadline := time.Now().Add(5 * time.Second)
	for fake.channelCalls("chA")+fake.channelCalls("chB") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first run never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	go func() { results <- a.RefreshAll(context.Background(), 5*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	close(gate)

	for i := 0; i < 2; i++ {
		select {
		case res := <-results:
			if res.Running || res.Channels != 2 {
				t.Errorf("result = %+v, want finished run over 2 channels", res)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a RefreshAll call did not return")
		}
	}
	for _, url := range []string{"chA", "chB"} {
		if n := fake.channelCalls(url); n != 1 {
			t.Errorf("GetChannelVideos(%s) calls = %d, want 1", url, n)
		}
	}
}

func TestRefreshAllStartsNewRunAfterTheLastEnded(t *testing.T) {
	fake := &fakeDownloader{videos: map[string][]downloader.VideoInfo{"chA": {{URL: "a1", Title: "A1"}}}}
	a, database := newTestAgent(t, fake)
	addChannel(t, database, "chA", "A")

	a.RefreshAll(context.Background(), 5*time.Second)
	a.RefreshAll(context.Background(), 5*time.Second)

	if n := fake.channelCalls("chA"); n != 2 {
		t.Errorf("GetChannelVideos calls = %d, want 2", n)
	}
}

func TestHandleNewVideosWithoutResultsKeepsItsText(t *testing.T) {
	fake := &fakeDownloader{videosErr: map[string]error{"chA": errors.New("boom")}}
	a, database := newTestAgent(t, fake)
	addChannel(t, database, "chA", "A")

	resp, err := a.ProcessMessage(context.Background(), "web", 1, 1, "/new")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "No new videos found" {
		t.Errorf("text = %q", resp.Text)
	}
}

func TestSubsPaginatesTenPerPage(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	for i := 0; i < 12; i++ {
		addChannel(t, database, fmt.Sprintf("ch%02d", i), fmt.Sprintf("Channel%02d", i))
	}

	resp, err := a.ProcessMessage(context.Background(), "web", 1, 1, "/subs")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(resp.Text, "Channel"); n != 10 {
		t.Errorf("page 1 lists %d channels, want 10", n)
	}
	if resp.Pager == nil || *resp.Pager != (Pager{Kind: "subs", Page: 1, TotalPages: 2}) {
		t.Errorf("pager = %+v, want subs page 1/2", resp.Pager)
	}

	resp, err = a.ProcessMessage(context.Background(), "web", 1, 1, "/subs 2")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(resp.Text, "Channel"); n != 2 {
		t.Errorf("page 2 lists %d channels, want 2", n)
	}
	if resp.Pager == nil || resp.Pager.Page != 2 || resp.Pager.TotalPages != 2 {
		t.Errorf("pager = %+v, want page 2/2", resp.Pager)
	}
}

func TestSubsWithOnePageHasNoPager(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	addChannel(t, database, "chA", "A")

	resp, err := a.ProcessMessage(context.Background(), "web", 1, 1, "/subs")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Pager != nil {
		t.Errorf("pager = %+v, want nil for a single page", resp.Pager)
	}
}

func TestSubsPageOutOfRangeIsClamped(t *testing.T) {
	a, database := newTestAgent(t, &fakeDownloader{})
	for i := 0; i < 12; i++ {
		addChannel(t, database, fmt.Sprintf("ch%02d", i), fmt.Sprintf("Channel%02d", i))
	}

	resp, err := a.ProcessMessage(context.Background(), "web", 1, 1, "/subs 99")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Pager == nil || resp.Pager.Page != 2 {
		t.Errorf("pager = %+v, want the last page (2)", resp.Pager)
	}
}

func TestVideosReturnsPagerForChannel(t *testing.T) {
	var infos []downloader.VideoInfo
	for i := 0; i < 12; i++ {
		infos = append(infos, downloader.VideoInfo{URL: fmt.Sprintf("v%02d", i), Title: fmt.Sprintf("Video%02d", i)})
	}
	fake := &fakeDownloader{videos: map[string][]downloader.VideoInfo{"chA": infos}}
	a, database := newTestAgent(t, fake)
	ch := addChannel(t, database, "chA", "A")

	resp, err := a.ProcessMessage(context.Background(), "web", 1, 1, fmt.Sprintf("/videos %d", ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	want := Pager{Kind: "videos", ChannelID: ch.ID, Page: 1, TotalPages: 2}
	if resp.Pager == nil || *resp.Pager != want {
		t.Errorf("pager = %+v, want %+v", resp.Pager, want)
	}
}
