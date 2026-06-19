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
	mu         sync.Mutex
}

type DownloadRequest struct {
	URL        string
	OutputDir  string
	OnProgress func(int)
}

var progressRe = regexp.MustCompile(`(\d+\.?\d*)%`)

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

func New(binaryPath, outputDir string) *Downloader {
	return &Downloader{
		binaryPath: binaryPath,
		outputDir:  outputDir,
	}
}

func (d *Downloader) Download(ctx context.Context, req *DownloadRequest) (*DownloadResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	outputDir := req.OutputDir
	if outputDir == "" {
		outputDir = d.outputDir
	}

	if err := mkdirAll(outputDir); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	outputTemplate := filepath.Join(outputDir, "%(title).100s.%(ext)s")

	dlArgs := []string{
		"-o", outputTemplate,
		"--print", "after_move:filename",
		"--newline",
		req.URL,
	}

	cmd := exec.CommandContext(ctx, d.binaryPath, dlArgs...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to download: %w", err)
	}

	if req.OnProgress != nil && stderr != nil {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				line := scanner.Text()
				if matches := progressRe.FindStringSubmatch(line); len(matches) > 1 {
					if pct, err := strconv.ParseFloat(matches[1], 64); err == nil {
						req.OnProgress(int(pct))
					}
				}
			}
		}()

		if err := cmd.Wait(); err != nil {
			wg.Wait()
			return nil, fmt.Errorf("failed to download: %w", err)
		}
		wg.Wait()
	} else {
		if err := cmd.Wait(); err != nil {
			return nil, fmt.Errorf("failed to download: %w", err)
		}
	}

	dlPath := strings.TrimSpace(stdout.String())
	if dlPath == "" {
		return nil, fmt.Errorf("download completed but output path not reported")
	}

	ext := filepath.Ext(dlPath)
	mp3Path := strings.TrimSuffix(dlPath, ext) + ".mp3"

	ffArgs := []string{"-i", dlPath, "-codec:a", "libmp3lame", "-q:a", "0", "-y", mp3Path}
	ffCmd := exec.CommandContext(ctx, "ffmpeg", ffArgs...)
	ffCmd.Stderr = os.Stderr
	if err := ffCmd.Run(); err != nil {
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

func (d *Downloader) GetInfo(ctx context.Context, url string) (string, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	args := []string{
		"--print", "%(title)s|||%(channel)s",
		"--skip-download",
		url,
	}

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

func dirFiles(dir string) map[string]bool {
	files := make(map[string]bool)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}
	for _, e := range entries {
		files[e.Name()] = true
	}
	return files
}

func findNewFile(before map[string]bool, dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestTime int64
	for _, e := range entries {
		if before[e.Name()] {
			continue
		}
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".mp3" && ext != ".webm" && ext != ".m4a" && ext != ".opus" && ext != ".ogg" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().UnixNano() > newestTime {
			newestTime = info.ModTime().UnixNano()
			newest = filepath.Join(dir, e.Name())
		}
	}
	return newest
}

func findNewestAudio(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestTime int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".mp3" && ext != ".webm" && ext != ".m4a" && ext != ".opus" && ext != ".ogg" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().UnixNano() > newestTime {
			newestTime = info.ModTime().UnixNano()
			newest = filepath.Join(dir, e.Name())
		}
	}
	return newest
}

func (d *Downloader) GetChannelInfo(ctx context.Context, url string) (*ChannelInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	args := []string{
		"--print", "%(channel)s|||%(channel_url)s",
		"--playlist-items", "0",
		"--skip-download",
		url,
	}

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
	d.mu.Lock()
	defer d.mu.Unlock()

	args := []string{
		"--flat-playlist",
		"--print", "%(url)s|||%(title)s|||%(duration)s",
		"--playlist-end", fmt.Sprintf("%d", limit),
		"--skip-download",
		channelURL,
	}

	cmd := exec.CommandContext(ctx, d.binaryPath, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to get channel videos: %w", err)
	}

	var videos []VideoInfo
	scanner := bufio.NewScanner(stderr)
	go func() {
		for scanner.Scan() {}
	}()

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("failed to get channel videos: %w", err)
	}

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
	d.mu.Lock()
	defer d.mu.Unlock()

	args := []string{
		"--print", "%(upload_date)s",
		"--skip-download",
		videoURL,
	}

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