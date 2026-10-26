package downloader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewBaseArgs(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{"empty options add no flags", Options{}, nil},
		{
			"all options set",
			Options{
				JSRuntime:        "deno:/usr/bin/deno",
				RemoteComponents: "ejs:github",
				POTScript:        "/opt/pot.js",
				PlayerClient:     "web_embedded",
			},
			[]string{
				"--js-runtimes", "deno:/usr/bin/deno",
				"--remote-components", "ejs:github",
				"--extractor-args", "youtubepot-bgutilscript:script_path=/opt/pot.js",
				"--extractor-args", "youtube:player_client=web_embedded",
			},
		},
		{
			"only player client",
			Options{PlayerClient: "android"},
			[]string{"--extractor-args", "youtube:player_client=android"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New("yt-dlp", t.TempDir(), 1, tt.opts)
			if !reflect.DeepEqual(d.baseArgs, tt.want) {
				t.Errorf("baseArgs = %v, want %v", d.baseArgs, tt.want)
			}
		})
	}
}

func TestArgsPrependsBaseArgs(t *testing.T) {
	d := New("yt-dlp", t.TempDir(), 1, Options{JSRuntime: "node"})
	got := d.args("--flat-playlist", "--", "https://example.com")
	want := []string{"--js-runtimes", "node", "--flat-playlist", "--", "https://example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestNewClampsConcurrency(t *testing.T) {
	for _, n := range []int{0, -1} {
		d := New("yt-dlp", t.TempDir(), n, Options{})
		if cap(d.sem) != 1 {
			t.Errorf("maxConcurrent %d: cap(sem) = %d, want 1", n, cap(d.sem))
		}
	}
	if d := New("yt-dlp", t.TempDir(), 3, Options{}); cap(d.sem) != 3 {
		t.Errorf("cap(sem) = %d, want 3", cap(d.sem))
	}
}

func TestScale(t *testing.T) {
	tests := []struct {
		pct        float64
		base, span int
		want       int
	}{
		{0, 0, 50, 0},
		{50, 0, 50, 25},
		{100, 0, 50, 50},
		{0, 50, 50, 50},
		{50, 50, 50, 75},
		{100, 50, 50, 100},
		{-10, 0, 50, 0},
		{150, 0, 50, 50},
	}
	for _, tt := range tests {
		if got := scale(tt.pct, tt.base, tt.span); got != tt.want {
			t.Errorf("scale(%v, %d, %d) = %d, want %d", tt.pct, tt.base, tt.span, got, tt.want)
		}
	}
}

// stubEnv points a Downloader at the fake yt-dlp and puts the fake ffmpeg first
// in PATH. It returns the downloader, the file that records the yt-dlp argv and
// the directory the fake download is written to.
func stubEnv(t *testing.T) (d *Downloader, argvFile, outDir string) {
	t.Helper()

	stub, err := filepath.Abs(filepath.Join("testdata", "fake-yt-dlp.sh"))
	if err != nil {
		t.Fatal(err)
	}
	fakeFFmpeg, err := filepath.Abs(filepath.Join("testdata", "fake-ffmpeg.sh"))
	if err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	if err := os.Symlink(fakeFFmpeg, filepath.Join(binDir, "ffmpeg")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tmp := t.TempDir()
	argvFile = filepath.Join(tmp, "argv.txt")
	outDir = filepath.Join(tmp, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_ARGV_FILE", argvFile)
	t.Setenv("FAKE_OUT_DIR", outDir)
	t.Setenv("FAKE_SLEEP", "")
	t.Setenv("FAKE_FAIL_MSG", "")
	t.Setenv("FAKE_FFMPEG_FAIL", "")

	return New(stub, outDir, 1, Options{PlayerClient: "web_embedded"}), argvFile, outDir
}

func readArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestDownloadParsesProgressAndPath(t *testing.T) {
	d, _, outDir := stubEnv(t)

	var progress []int
	res, err := d.Download(context.Background(), &DownloadRequest{
		URL:        "https://www.youtube.com/watch?v=abc",
		OnProgress: func(p int) { progress = append(progress, p) },
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	wantPath := filepath.Join(outDir, "song.mp3")
	if res.OutputPath != wantPath {
		t.Errorf("OutputPath = %q, want %q", res.OutputPath, wantPath)
	}
	if res.Title != "song" {
		t.Errorf("Title = %q, want song", res.Title)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("mp3 not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "song.webm")); !os.IsNotExist(err) {
		t.Errorf("source file should be removed after transcoding, stat err = %v", err)
	}

	want := []int{5, 25, 50, 100}
	if !reflect.DeepEqual(progress, want) {
		t.Errorf("progress = %v, want %v", progress, want)
	}
}

func TestDownloadKeepsSourceWhenTranscodeFails(t *testing.T) {
	d, _, outDir := stubEnv(t)
	t.Setenv("FAKE_FFMPEG_FAIL", "1")

	res, err := d.Download(context.Background(), &DownloadRequest{URL: "https://example.com/v"})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if want := filepath.Join(outDir, "song.webm"); res.OutputPath != want {
		t.Errorf("OutputPath = %q, want %q", res.OutputPath, want)
	}
}

func TestDownloadPassesURLAfterDoubleDash(t *testing.T) {
	d, argvFile, _ := stubEnv(t)

	const url = "--exec=touch /tmp/pwned"
	if _, err := d.Download(context.Background(), &DownloadRequest{URL: url}); err != nil {
		t.Fatalf("Download: %v", err)
	}

	argv := readArgv(t, argvFile)
	n := len(argv)
	if n < 2 || argv[n-2] != "--" || argv[n-1] != url {
		t.Errorf("argv must end with [-- <url>], got tail %v", argv[max(0, n-3):])
	}
	if argv[0] != "--extractor-args" || argv[1] != "youtube:player_client=web_embedded" {
		t.Errorf("base args must come first, got %v", argv[:2])
	}
}

func TestDownloadReportsStderrOnFailure(t *testing.T) {
	d, _, _ := stubEnv(t)
	t.Setenv("FAKE_FAIL_MSG", "ERROR: Video unavailable")

	_, err := d.Download(context.Background(), &DownloadRequest{URL: "https://example.com/v"})
	if err == nil || !strings.Contains(err.Error(), "Video unavailable") {
		t.Errorf("err = %v, want it to contain the stderr tail", err)
	}
}

func TestDownloadRespectsContextCancel(t *testing.T) {
	d, _, _ := stubEnv(t)
	t.Setenv("FAKE_SLEEP", "30")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := d.Download(ctx, &DownloadRequest{URL: "https://example.com/v"})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Download did not return after the context was cancelled")
	}
}

func TestDownloadMarksWatchedOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		d, argvFile, _ := stubEnv(t)
		d.markWatched = enabled

		if _, err := d.Download(context.Background(), &DownloadRequest{URL: "https://example.com/v"}); err != nil {
			t.Fatalf("Download: %v", err)
		}

		got := false
		for _, a := range readArgv(t, argvFile) {
			if a == "--mark-watched" {
				got = true
			}
		}
		if got != enabled {
			t.Errorf("markWatched=%v: --mark-watched in argv = %v", enabled, got)
		}
	}
}

func TestDownloadPassesCookiesOnlyWhenSet(t *testing.T) {
	for _, cookies := range []string{"", "/opt/cookies.txt"} {
		d, argvFile, _ := stubEnv(t)
		d.cookies = cookies

		if _, err := d.Download(context.Background(), &DownloadRequest{URL: "https://example.com/v"}); err != nil {
			t.Fatalf("Download: %v", err)
		}

		argv := readArgv(t, argvFile)
		got := ""
		for i, a := range argv {
			if a == "--cookies" && i+1 < len(argv) {
				got = argv[i+1]
			}
		}
		if got != cookies {
			t.Errorf("cookies=%q: --cookies value in argv = %q", cookies, got)
		}
	}
}
