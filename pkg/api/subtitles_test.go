package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestSubtitleLangs(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "")
	rec := do(h, http.MethodGet, "/api/subtitles/langs?url=https://youtu.be/abc", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"code":"ru"`) || !strings.Contains(rec.Body.String(), `"auto":true`) {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
}

func TestSubtitleLangsNeedsURL(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "")
	if rec := do(h, http.MethodGet, "/api/subtitles/langs", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("code %d, want 400", rec.Code)
	}
}

func TestSubtitleDownload(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "")
	rec := do(h, http.MethodGet, "/api/subtitles?url=https://youtu.be/abc&lang=ru", nil)
	if rec.Code != 200 {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `attachment; filename="abc.ru.srt"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !strings.Contains(rec.Body.String(), "-->") {
		t.Errorf("body = %q", rec.Body)
	}
}

func TestSubtitleDownloadRejectsBadLang(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "")
	for _, q := range []string{"lang=", "lang=--exec", "lang=ru,en"} {
		if rec := do(h, http.MethodGet, "/api/subtitles?url=https://youtu.be/abc&"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, want 400", q, rec.Code)
		}
	}
}

func TestSubtitlesRequireToken(t *testing.T) {
	_, _, _, h := newVoiceServer(t, "secret")
	if rec := do(h, http.MethodGet, "/api/subtitles?url=u&lang=ru", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("code %d, want 401", rec.Code)
	}
}
