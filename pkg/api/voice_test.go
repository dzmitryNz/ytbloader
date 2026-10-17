package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mitry/ytbloader/pkg/agent"
	"github.com/mitry/ytbloader/pkg/db"
	"github.com/mitry/ytbloader/pkg/downloader"
)

type fakeYT struct {
	release chan struct{}

	mu            sync.Mutex
	channelCalls  int
	downloadCalls int
}

func (f *fakeYT) Download(ctx context.Context, req *downloader.DownloadRequest) (*downloader.DownloadResult, error) {
	f.mu.Lock()
	f.downloadCalls++
	f.mu.Unlock()
	if req.OnStart != nil {
		req.OnStart()
	}
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &downloader.DownloadResult{OutputPath: "/tmp/x.mp3", Title: "x", Size: 1}, nil
}

func (f *fakeYT) GetInfo(ctx context.Context, url string) (string, string, error) {
	return "info title", "info channel", nil
}

func (f *fakeYT) GetChannelInfo(ctx context.Context, url string) (*downloader.ChannelInfo, error) {
	return &downloader.ChannelInfo{Name: "c", URL: url}, nil
}

func (f *fakeYT) GetChannelVideos(ctx context.Context, channelURL string, limit int) ([]downloader.VideoInfo, error) {
	f.mu.Lock()
	f.channelCalls++
	f.mu.Unlock()
	return nil, nil
}

func (f *fakeYT) GetVideoPublishDate(ctx context.Context, videoURL string) time.Time {
	return time.Time{}
}

func (f *fakeYT) downloads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloadCalls
}

func newVoiceServer(t *testing.T, token string) (*Server, *db.DB, *fakeYT, http.Handler) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "voice.db")
	database, err := db.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite3", dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	rawConns.Store(database, raw)
	fake := &fakeYT{release: make(chan struct{})}
	s := &Server{db: database, downloader: fake, agent: agent.New(database, fake), apiToken: token}
	t.Cleanup(func() {
		close(fake.release)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			active, _ := database.ListDownloadsByStatus("pending", "downloading")
			if len(active) == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		database.Close()
	})
	return s, database, fake, s.routes("")
}

func do(h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body struct {
		Success bool                   `json:"success"`
		Data    map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
	}
	return body.Data
}

func seedVideo(t *testing.T, database *db.DB, channelID int64, url string, position int) *db.Video {
	t.Helper()
	v := &db.Video{ChannelID: channelID, URL: url, Title: "title " + url, Duration: 125, Position: position}
	if err := database.UpsertVideo(v); err != nil {
		t.Fatal(err)
	}
	return v
}

func seedOldChannel(t *testing.T, database *db.DB, url, name string) *db.Channel {
	t.Helper()
	c := &db.Channel{URL: url, Name: name, MessengerID: "web", UserID: 1}
	if err := database.CreateChannel(c); err != nil {
		t.Fatal(err)
	}
	if err := setCreatedAt(database, "channels", c.ID, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRefreshAllRequiresTokenWhenSet(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "secret")

	if rec := do(h, http.MethodPost, "/api/subscriptions/refresh", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("without token: status %d, want 401", rec.Code)
	}
	rec := do(h, http.MethodPost, "/api/subscriptions/refresh?wait=1", map[string]string{"X-API-Token": "secret"})
	if rec.Code != http.StatusOK {
		t.Errorf("with token: status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRefreshAllReturnsResult(t *testing.T) {
	_, database, fake, h := newVoiceServer(t, "")
	seedOldChannel(t, database, "chA", "A")
	seedOldChannel(t, database, "chB", "B")

	rec := do(h, http.MethodPost, "/api/subscriptions/refresh?wait=5", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	data := decodeData(t, rec)
	if data["Channels"] != float64(2) || data["Running"] != false {
		t.Errorf("data = %v, want Channels 2, Running false", data)
	}
	if fake.channelCalls != 2 {
		t.Errorf("GetChannelVideos calls = %d, want 2", fake.channelCalls)
	}
}

func TestRefreshAllRejectsOtherMethods(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "")

	if rec := do(h, http.MethodGet, "/api/subscriptions/refresh", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}
}

func TestSubscriptionRefreshByIDStillRouted(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "")

	rec := do(h, http.MethodPost, "/api/subscriptions/5/refresh", nil)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 from refreshSubscription (channel 5 missing)", rec.Code)
	}
}

func TestNewVideosListsRows(t *testing.T) {
	_, database, _, h := newVoiceServer(t, "")
	c := seedOldChannel(t, database, "chA", "Channel A")
	v := seedVideo(t, database, c.ID, "https://y/a", 1)

	rec := do(h, http.MethodGet, "/api/videos/new", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	data := decodeData(t, rec)
	videos, _ := data["videos"].([]interface{})
	if len(videos) != 1 || data["total"] != float64(1) {
		t.Fatalf("data = %v, want one video, total 1", data)
	}
	got := videos[0].(map[string]interface{})
	if got["id"] != float64(v.ID) || got["title"] != "title https://y/a" || got["channel"] != "Channel A" ||
		got["duration"] != float64(125) || got["url"] != "https://y/a" {
		t.Errorf("video = %v", got)
	}
}

func TestNewVideosLimitAndClamp(t *testing.T) {
	_, database, _, h := newVoiceServer(t, "")
	c := seedOldChannel(t, database, "chA", "A")
	for i := 1; i <= 25; i++ {
		seedVideo(t, database, c.ID, "https://y/"+strconv.Itoa(i), i)
	}

	count := func(path string) (int, float64) {
		data := decodeData(t, do(h, http.MethodGet, path, nil))
		videos, _ := data["videos"].([]interface{})
		return len(videos), data["total"].(float64)
	}
	if n, total := count("/api/videos/new"); n != 5 || total != 25 {
		t.Errorf("default: %d videos, total %v, want 5 / 25", n, total)
	}
	if n, _ := count("/api/videos/new?limit=3"); n != 3 {
		t.Errorf("limit=3: %d videos", n)
	}
	if n, _ := count("/api/videos/new?limit=500"); n != 20 {
		t.Errorf("limit=500: %d videos, want clamp to 20", n)
	}
	if n, _ := count("/api/videos/new?limit=0"); n != 1 {
		t.Errorf("limit=0: %d videos, want clamp to 1", n)
	}
}

func TestNewVideosEmptyIsAnEmptyList(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "")

	data := decodeData(t, do(h, http.MethodGet, "/api/videos/new", nil))

	videos, ok := data["videos"].([]interface{})
	if !ok || len(videos) != 0 || data["total"] != float64(0) {
		t.Errorf("data = %v, want empty videos list and total 0", data)
	}
}

func TestNewVideosDaysWindow(t *testing.T) {
	_, database, _, h := newVoiceServer(t, "")
	c := seedOldChannel(t, database, "chA", "A")
	v := seedVideo(t, database, c.ID, "https://y/a", 1)
	if err := setCreatedAt(database, "channels", c.ID, time.Now().Add(-20*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := setCreatedAt(database, "videos", v.ID, time.Now().Add(-5*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if data := decodeData(t, do(h, http.MethodGet, "/api/videos/new", nil)); data["total"] != float64(0) {
		t.Errorf("default 3 days: total %v, want 0", data["total"])
	}
	if data := decodeData(t, do(h, http.MethodGet, "/api/videos/new?days=7", nil)); data["total"] != float64(1) {
		t.Errorf("days=7: total %v, want 1", data["total"])
	}
}

func TestVideoDownloadUnknownAndBadID(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "")

	if rec := do(h, http.MethodPost, "/api/videos/7/download", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing id: status %d, want 404", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/api/videos/abc/download", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("abc: status %d, want 400", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/api/videos/7/download", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}
}

func TestVideoDownloadStartsThenReportsQueued(t *testing.T) {
	_, database, fake, h := newVoiceServer(t, "")
	c := seedOldChannel(t, database, "chA", "A")
	v := seedVideo(t, database, c.ID, "https://y/a", 1)
	path := "/api/videos/" + strconv.FormatInt(v.ID, 10) + "/download"

	rec := do(h, http.MethodPost, path, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first call: status %d: %s", rec.Code, rec.Body.String())
	}
	data := decodeData(t, rec)
	if data["status"] != "started" || data["title"] != "title https://y/a" || data["download_id"] == nil {
		t.Errorf("first call data = %v", data)
	}

	rec = do(h, http.MethodPost, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second call: status %d: %s", rec.Code, rec.Body.String())
	}
	if data := decodeData(t, rec); data["status"] != "queued" || data["download_id"] == nil {
		t.Errorf("second call data = %v, want queued", data)
	}

	all, _ := database.ListDownloads()
	if len(all) != 1 {
		t.Errorf("download rows = %d, want 1", len(all))
	}
	deadline := time.Now().Add(2 * time.Second)
	for fake.downloads() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := fake.downloads(); n != 1 {
		t.Errorf("Download calls = %d, want 1", n)
	}
}

func TestVideoDownloadAlreadyDownloaded(t *testing.T) {
	_, database, fake, h := newVoiceServer(t, "")
	c := seedOldChannel(t, database, "chA", "A")
	v := seedVideo(t, database, c.ID, "https://y/a", 1)
	if err := database.MarkVideoDownloaded("https://y/a"); err != nil {
		t.Fatal(err)
	}

	rec := do(h, http.MethodPost, "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/download", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if data := decodeData(t, rec); data["status"] != "downloaded" {
		t.Errorf("data = %v, want downloaded", data)
	}
	if all, _ := database.ListDownloads(); len(all) != 0 {
		t.Errorf("download rows = %d, want 0", len(all))
	}
	if fake.downloads() != 0 {
		t.Error("Download was called for an already downloaded video")
	}
}

func TestVideoDownloadConcurrentCallsCreateOneDownload(t *testing.T) {
	_, database, _, h := newVoiceServer(t, "")
	c := seedOldChannel(t, database, "chA", "A")
	v := seedVideo(t, database, c.ID, "https://y/a", 1)
	path := "/api/videos/" + strconv.FormatInt(v.ID, 10) + "/download"

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			do(h, http.MethodPost, path, nil)
		}()
	}
	wg.Wait()

	if all, _ := database.ListDownloads(); len(all) != 1 {
		t.Errorf("download rows = %d, want 1", len(all))
	}
}

var rawConns sync.Map

// setCreatedAt reaches around the db package: created_at has no setter and
// the window logic needs rows of a given age.
func setCreatedAt(database *db.DB, table string, id int64, at time.Time) error {
	raw, _ := rawConns.Load(database)
	_, err := raw.(*sql.DB).Exec(`UPDATE `+table+` SET created_at = ? WHERE id = ?`, at.UTC().Format("2006-01-02 15:04:05"), id)
	return err
}
