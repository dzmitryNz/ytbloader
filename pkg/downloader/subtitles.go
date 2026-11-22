package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SubLang is one subtitle language a video offers. Auto marks machine-made
// captions, as opposed to subtitles uploaded by the author.
type SubLang struct {
	Code string `json:"code"`
	Auto bool   `json:"auto"`
}

var subLangRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// validSubLang accepts plain language codes only: --sub-langs takes regexes
// and comma lists, which a web caller has no business passing.
func validSubLang(code string) bool { return subLangRe.MatchString(code) }

func parseSubLangs(data []byte) ([]SubLang, error) {
	var info struct {
		Subtitles map[string]json.RawMessage `json:"subtitles"`
		Auto      map[string]json.RawMessage `json:"automatic_captions"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parse video info: %w", err)
	}
	var out []SubLang
	for _, group := range []struct {
		m    map[string]json.RawMessage
		auto bool
	}{{info.Subtitles, false}, {info.Auto, true}} {
		start := len(out)
		for code := range group.m {
			out = append(out, SubLang{Code: code, Auto: group.auto})
		}
		part := out[start:]
		sort.Slice(part, func(i, j int) bool { return part[i].Code < part[j].Code })
	}
	return out, nil
}

// SubtitleLangs lists the subtitle languages available for a video.
func (d *Downloader) SubtitleLangs(ctx context.Context, url string) ([]SubLang, error) {
	cmd := exec.CommandContext(ctx, d.binaryPath, d.args("--skip-download", "--dump-single-json", "--", url)...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to get video info: %w", err)
	}
	return parseSubLangs(stdout.Bytes())
}

func (d *Downloader) subtitleArgs(url, lang string, auto bool, outTmpl string) []string {
	write := "--write-subs"
	if auto {
		write = "--write-auto-subs"
	}
	return d.args("--skip-download", write, "--sub-langs", lang,
		"--convert-subs", "srt", "-o", outTmpl, "--", url)
}

// Subtitles downloads one language as SRT and returns the file name and
// content. Nothing is kept on disk.
func (d *Downloader) Subtitles(ctx context.Context, url, lang string, auto bool) (string, []byte, error) {
	if !validSubLang(lang) {
		return "", nil, fmt.Errorf("invalid language code")
	}
	dir, err := os.MkdirTemp("", "ytbloader-subs-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(dir)

	cmd := exec.CommandContext(ctx, d.binaryPath, d.subtitleArgs(url, lang, auto, filepath.Join(dir, "%(id)s.%(ext)s"))...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("failed to download subtitles: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.srt"))
	if len(files) == 0 {
		return "", nil, fmt.Errorf("no subtitles for language %q", lang)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return "", nil, err
	}
	return filepath.Base(files[0]), data, nil
}
