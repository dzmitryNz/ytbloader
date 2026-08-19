package downloader

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Downloader struct {
	binaryPath string
	outputDir  string
	sem        chan struct{}
	// baseArgs precede every yt-dlp invocation; they carry the JS runtime and
	// PO token setup YouTube requires before it serves any media.
	baseArgs []string
}

// Options carry the yt-dlp extraction settings. Every field is optional: an
// empty one simply leaves the corresponding flag off the command line.
type Options struct {
	JSRuntime        string
	RemoteComponents string
	POTScript        string
	PlayerClient     string
}

type DownloadRequest struct {
	URL        string
	OutputDir  string
	OnStart    func()
	OnProgress func(int)
}

var progressRe = regexp.MustCompile(`(\d+\.?\d*)%`)

// progressMarker tags the lines produced by --progress-template so they stand
// out from everything else yt-dlp writes to stdout.
const progressMarker = "[ytbloader-progress]"

// downloadShare splits the reported progress between the two phases of a job.
// Both are slow enough to matter — fetching dominates on a thin connection,
// transcoding on a fast one — so neither gets to own the whole bar.
const downloadShare = 50

var (
	ffDurationRe = regexp.MustCompile(`Duration: (\d+):(\d+):(\d+(?:\.\d+)?)`)
	ffTimeRe     = regexp.MustCompile(`^out_time_us=(\d+)`)
)

// scale maps a phase-local percentage onto [base, base+span] of the overall bar.
func scale(pct float64, base, span int) int {
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	return base + int(pct*float64(span)/100)
}

type DownloadResult struct {
	OutputPath string
	Title      string
	Size       int64
}

type ChannelInfo struct {
	Name string
	URL  string
}

type VideoInfo struct {
	URL      string
	Title    string
	Duration int
}

func New(binaryPath, outputDir string, maxConcurrent int, opts Options) *Downloader {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	var baseArgs []string
	if opts.JSRuntime != "" {
		baseArgs = append(baseArgs, "--js-runtimes", opts.JSRuntime)
	}
	if opts.RemoteComponents != "" {
		baseArgs = append(baseArgs, "--remote-components", opts.RemoteComponents)
	}
	if opts.POTScript != "" {
		baseArgs = append(baseArgs, "--extractor-args", "youtubepot-bgutilscript:script_path="+opts.POTScript)
	}
	if opts.PlayerClient != "" {
		baseArgs = append(baseArgs, "--extractor-args", "youtube:player_client="+opts.PlayerClient)
	}

	return &Downloader{
		binaryPath: binaryPath,
		outputDir:  outputDir,
		sem:        make(chan struct{}, maxConcurrent),
		baseArgs:   baseArgs,
	}
}

// args prefixes the shared extraction flags to a per-call argument list.
func (d *Downloader) args(rest ...string) []string {
	out := make([]string, 0, len(d.baseArgs)+len(rest))
	out = append(out, d.baseArgs...)
	return append(out, rest...)
}

func (d *Downloader) Download(ctx context.Context, req *DownloadRequest) (*DownloadResult, error) {
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-d.sem }()

	if req.OnStart != nil {
		req.OnStart()
	}

	outputDir := req.OutputDir
	if outputDir == "" {
		outputDir = d.outputDir
	}

	if err := mkdirAll(outputDir); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	outputTemplate := filepath.Join(outputDir, "%(title).100s.%(ext)s")

	// --print would report the path on stdout, but it also implies --quiet,
	// which suppresses progress entirely. Routing the path to a file instead
	// keeps yt-dlp talkative and leaves stdout free for progress lines.
	pathFile, err := os.CreateTemp("", "ytbloader-path-*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	pathFileName := pathFile.Name()
	pathFile.Close()
	defer os.Remove(pathFileName)

	// The end product is an mp3, so pulling the video stream only wastes
	// bandwidth — and video formats are the ones YouTube guards hardest.
	dlArgs := d.args(
		"-f", "bestaudio/best",
		"-o", outputTemplate,
		"--print-to-file", "after_move:filename", pathFileName,
		"--newline",
		"--progress-template", "download:"+progressMarker+" %(progress._percent_str)s",
		"--", req.URL,
	)

	cmd := exec.CommandContext(ctx, d.binaryPath, dlArgs...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to download: %w", err)
	}

	var wg sync.WaitGroup

	// Progress lines carry our marker, so they cannot be confused with the
	// rest of yt-dlp's chatter on stdout.
	if stdout != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				line := scanner.Text()
				if req.OnProgress == nil || !strings.HasPrefix(line, progressMarker) {
					continue
				}
				if matches := progressRe.FindStringSubmatch(line); len(matches) > 1 {
					if pct, err := strconv.ParseFloat(matches[1], 64); err == nil {
						req.OnProgress(scale(pct, 0, downloadShare))
					}
				}
			}
		}()
	}

	// yt-dlp explains a failure on stderr, so keep the tail around:
	// "exit status 1" on its own says nothing.
	errLog := &errorTail{}
	if stderr != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				errLog.add(scanner.Text())
			}
		}()
	}

	waitErr := cmd.Wait()
	wg.Wait()
	if waitErr != nil {
		return nil, fmt.Errorf("failed to download: %w%s", waitErr, errLog.suffix())
	}

	dlPath := lastLine(pathFileName)
	if dlPath == "" {
		return nil, fmt.Errorf("download completed but output path not reported")
	}

	ext := filepath.Ext(dlPath)
	mp3Path := strings.TrimSuffix(dlPath, ext) + ".mp3"

	if err := d.transcode(ctx, dlPath, mp3Path, req.OnProgress); err != nil {
		return &DownloadResult{
			OutputPath: dlPath,
			Title:      filepath.Base(strings.TrimSuffix(dlPath, ext)),
			Size:       fileSize(dlPath),
		}, nil
	}

	os.Remove(dlPath)

	title := filepath.Base(mp3Path)
	titleExt := filepath.Ext(title)
	if titleExt != "" {
		title = title[:len(title)-len(titleExt)]
	}

	return &DownloadResult{
		OutputPath: mp3Path,
		Title:      title,
		Size:       fileSize(mp3Path),
	}, nil
}

// transcode converts the downloaded audio to mp3, reporting progress over the
// second half of the bar. ffmpeg announces the track length on stderr and the
// position reached on stdout, so the two together give a real percentage.
func (d *Downloader) transcode(ctx context.Context, srcPath, mp3Path string, onProgress func(int)) error {
	ffArgs := []string{
		"-i", srcPath,
		"-codec:a", "libmp3lame",
		"-q:a", "0",
		"-y",
		"-nostats",
		"-progress", "pipe:1",
		mp3Path,
	}

	ffCmd := exec.CommandContext(ctx, "ffmpeg", ffArgs...)
	stdout, _ := ffCmd.StdoutPipe()
	stderr, _ := ffCmd.StderrPipe()
	if err := ffCmd.Start(); err != nil {
		return err
	}

	// durationUS is written by the stderr reader and read by the stdout reader.
	var (
		mu         sync.Mutex
		durationUS float64
	)

	var wg sync.WaitGroup
	if stderr != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				m := ffDurationRe.FindStringSubmatch(scanner.Text())
				if m == nil {
					continue
				}
				h, _ := strconv.ParseFloat(m[1], 64)
				min, _ := strconv.ParseFloat(m[2], 64)
				sec, _ := strconv.ParseFloat(m[3], 64)
				mu.Lock()
				durationUS = (h*3600 + min*60 + sec) * 1e6
				mu.Unlock()
			}
		}()
	}

	if stdout != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				m := ffTimeRe.FindStringSubmatch(scanner.Text())
				if m == nil || onProgress == nil {
					continue
				}
				mu.Lock()
				total := durationUS
				mu.Unlock()
				if total <= 0 {
					continue
				}
				pos, err := strconv.ParseFloat(m[1], 64)
				if err != nil {
					continue
				}
				onProgress(scale(pos/total*100, downloadShare, 100-downloadShare))
			}
		}()
	}

	err := ffCmd.Wait()
	wg.Wait()
	if err != nil {
		return err
	}

	if onProgress != nil {
		onProgress(100)
	}
	return nil
}

func (d *Downloader) GetInfo(ctx context.Context, url string) (string, string, error) {
	args := d.args(
		"--print", "%(title)s|||%(channel)s",
		"--skip-download",
		"--", url,
	)

	cmd := exec.CommandContext(ctx, d.binaryPath, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("failed to get info: %w", err)
	}

	result := strings.TrimSpace(stdout.String())
	parts := strings.SplitN(result, "|||", 2)
	if len(parts) >= 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
	}
	return result, "", nil
}

// errorTail keeps the last few stderr lines so a failed run can explain itself.
type errorTail struct {
	mu     sync.Mutex
	lines  []string
	errors []string
}

const errorTailLines = 3

func (e *errorTail) add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lines = append(e.lines, line)
	if len(e.lines) > errorTailLines {
		e.lines = e.lines[len(e.lines)-errorTailLines:]
	}
	if strings.HasPrefix(line, "ERROR:") {
		e.errors = append(e.errors, line)
	}
}

// suffix prefers yt-dlp's own ERROR lines and falls back to the tail of
// stderr, which is all there is when yt-dlp dies without reporting a reason.
func (e *errorTail) suffix() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	lines := e.errors
	if len(lines) == 0 {
		lines = e.lines
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > errorTailLines {
		lines = lines[len(lines)-errorTailLines:]
	}
	return ": " + strings.Join(lines, " | ")
}

// lastLine returns the final non-empty line of a file, ignoring read errors:
// an unreadable or empty file is reported as a missing path by the caller.
func lastLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func mkdirAll(path string) error {
	return os.MkdirAll(path, 0755)
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (d *Downloader) GetChannelInfo(ctx context.Context, url string) (*ChannelInfo, error) {
	args := d.args(
		"--print", "%(channel)s|||%(channel_url)s",
		"--playlist-items", "0",
		"--skip-download",
		"--", url,
	)

	cmd := exec.CommandContext(ctx, d.binaryPath, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to get channel info: %w", err)
	}

	result := strings.TrimSpace(stdout.String())

	parts := strings.SplitN(result, "|||", 2)
	if len(parts) < 2 {
		return &ChannelInfo{Name: result, URL: url}, nil
	}

	return &ChannelInfo{Name: parts[0], URL: parts[1]}, nil
}

func (d *Downloader) GetChannelVideos(ctx context.Context, channelURL string, limit int) ([]VideoInfo, error) {
	args := d.args(
		"--flat-playlist",
		"--print", "%(url)s|||%(title)s|||%(duration)s",
		"--playlist-end", fmt.Sprintf("%d", limit),
		"--skip-download",
		"--", channelURL,
	)

	cmd := exec.CommandContext(ctx, d.binaryPath, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to get channel videos: %w", err)
	}

	// cmd.Wait closes the stderr pipe, so the drain goroutine has to finish first.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
		}
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("failed to get channel videos: %w", err)
	}

	var videos []VideoInfo

	lines := strings.Split(stdout.String(), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "|||", 3)
		if len(parts) < 2 {
			continue
		}

		duration := 0
		if len(parts) >= 3 {
			fmt.Sscanf(parts[2], "%d", &duration)
		}

		videos = append(videos, VideoInfo{
			URL:      parts[0],
			Title:    parts[1],
			Duration: duration,
		})
	}

	return videos, nil
}

func (d *Downloader) GetVideoPublishDate(ctx context.Context, videoURL string) time.Time {
	args := d.args(
		"--print", "%(upload_date)s",
		"--skip-download",
		"--", videoURL,
	)

	cmd := exec.CommandContext(ctx, d.binaryPath, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return time.Time{}
	}

	dateStr := strings.TrimSpace(stdout.String())
	if dateStr == "" || dateStr == "NA" {
		return time.Time{}
	}

	t, _ := time.Parse("20060102", dateStr)
	return t
}
