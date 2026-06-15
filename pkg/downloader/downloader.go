package downloader

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type Downloader struct {
	binaryPath string
	outputDir  string
	mu         sync.Mutex
}

type DownloadRequest struct {
	URL      string
	OutputDir string
}

type DownloadResult struct {
	OutputPath string
	Title      string
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

	outputTemplate := filepath.Join(outputDir, "%(title)s.%(ext)s")

	args := []string{
		"-x", "--audio-format", "mp3",
		"--audio-quality", "0",
		"-o", outputTemplate,
		"--print", "filename",
		req.URL,
	}

	cmd := exec.CommandContext(ctx, d.binaryPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to download: %w, output: %s", err, string(output))
	}

	outputPath := string(output)
	if len(outputPath) > 0 && outputPath[len(outputPath)-1] == '\n' {
		outputPath = outputPath[:len(outputPath)-1]
	}

	title := filepath.Base(outputPath)
	ext := filepath.Ext(title)
	if ext != "" {
		title = title[:len(title)-len(ext)]
	}

	return &DownloadResult{
		OutputPath: outputPath,
		Title:      title,
	}, nil
}

func (d *Downloader) GetInfo(ctx context.Context, url string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	args := []string{
		"--print", "title",
		"--skip-download",
		url,
	}

	cmd := exec.CommandContext(ctx, d.binaryPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get info: %w, output: %s", err, string(output))
	}

	title := string(output)
	if len(title) > 0 && title[len(title)-1] == '\n' {
		title = title[:len(title)-1]
	}

	return title, nil
}

func mkdirAll(path string) error {
	return exec.Command("mkdir", "-p", path).Run()
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
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get channel info: %w, output: %s", err, string(output))
	}

	result := string(output)
	if len(result) > 0 && result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}

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
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get channel videos: %w, output: %s", err, string(output))
	}

	var videos []VideoInfo
	lines := strings.Split(string(output), "\n")
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