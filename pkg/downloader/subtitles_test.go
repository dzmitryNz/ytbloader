package downloader

import (
	"reflect"
	"testing"
)

func TestParseSubLangs(t *testing.T) {
	js := `{"subtitles":{"ru":[{}],"en":[{}]},"automatic_captions":{"ru":[{}],"be":[{}]}}`
	got, err := parseSubLangs([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	want := []SubLang{
		{Code: "en"}, {Code: "ru"},
		{Code: "be", Auto: true}, {Code: "ru", Auto: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseSubLangsEmpty(t *testing.T) {
	got, err := parseSubLangs([]byte(`{"title":"x"}`))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want empty", got, err)
	}
}

func TestSubtitleArgs(t *testing.T) {
	d := New("yt-dlp", t.TempDir(), 1, Options{JSRuntime: "node"})
	manual := d.subtitleArgs("https://x/v", "ru", false, "/tmp/o/%(id)s.%(ext)s")
	want := []string{"--js-runtimes", "node", "--skip-download", "--write-subs",
		"--sub-langs", "ru", "--convert-subs", "srt", "-o", "/tmp/o/%(id)s.%(ext)s", "--", "https://x/v"}
	if !reflect.DeepEqual(manual, want) {
		t.Errorf("manual = %v", manual)
	}
	auto := d.subtitleArgs("https://x/v", "ru", true, "o")
	if auto[3] != "--write-auto-subs" {
		t.Errorf("auto flag = %v", auto)
	}
}

func TestValidSubLang(t *testing.T) {
	for _, ok := range []string{"ru", "en-US", "zh-Hans", "en.*"[:2]} {
		if !validSubLang(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "--exec", "a b", "ru,en", "en.*", "../x"} {
		if validSubLang(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
