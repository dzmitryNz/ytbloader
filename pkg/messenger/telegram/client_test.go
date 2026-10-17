package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
)

type apiCall struct {
	method string
	fields map[string]string
	file   string // "<filename>=<content>" of the "document" part, if any
}

// newAPIClient returns an apiClient talking to a fake Telegram server that
// records every request and answers ok.
func newAPIClient(t *testing.T) (apiClient, *[]apiCall) {
	t.Helper()
	var calls []apiCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		call := apiCall{method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], fields: map[string]string{}}
		for k, v := range r.MultipartForm.Value {
			call.fields[k] = v[0]
		}
		if f, hdr, err := r.FormFile("document"); err == nil {
			data, _ := io.ReadAll(f)
			call.file = hdr.Filename + "=" + string(data)
			f.Close()
		}
		calls = append(calls, call)

		w.Header().Set("Content-Type", "application/json")
		if call.method == "answerCallbackQuery" {
			io.WriteString(w, `{"ok":true,"result":true}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`)
	}))
	t.Cleanup(srv.Close)

	b, err := bot.New("123:abc", bot.WithSkipGetMe(), bot.WithServerURL(srv.URL), bot.WithHTTPClient(0, srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return apiClient{b}, &calls
}

func TestAPIClientSendMessageWithKeyboard(t *testing.T) {
	c, calls := newAPIClient(t)
	kb := keyboard{{{Text: "Next »", Data: "p:subs:2"}}}

	if err := c.SendMessage(context.Background(), 42, "hi", kb); err != nil {
		t.Fatal(err)
	}

	call := (*calls)[0]
	if call.method != "sendMessage" || call.fields["chat_id"] != "42" || call.fields["text"] != "hi" {
		t.Errorf("call = %+v", call)
	}
	var markup struct {
		InlineKeyboard [][]struct {
			Text         string `json:"text"`
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(call.fields["reply_markup"]), &markup); err != nil {
		t.Fatalf("reply_markup %q: %v", call.fields["reply_markup"], err)
	}
	if rows := markup.InlineKeyboard; len(rows) != 1 || len(rows[0]) != 1 || rows[0][0].CallbackData != "p:subs:2" || rows[0][0].Text != "Next »" {
		t.Errorf("keyboard = %+v", rows)
	}
}

func TestAPIClientSendMessageWithoutKeyboardOmitsMarkup(t *testing.T) {
	c, calls := newAPIClient(t)

	if err := c.SendMessage(context.Background(), 42, "hi", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*calls)[0].fields["reply_markup"]; ok {
		t.Errorf("call = %+v, want no reply_markup", (*calls)[0])
	}
}

func TestAPIClientEditMessageAndAnswerCallback(t *testing.T) {
	c, calls := newAPIClient(t)

	if err := c.EditMessage(context.Background(), 42, 99, "page 2", keyboard{{{Text: "« Prev", Data: "p:subs:1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AnswerCallback(context.Background(), "cb1"); err != nil {
		t.Fatal(err)
	}

	edit := (*calls)[0]
	if edit.method != "editMessageText" || edit.fields["chat_id"] != "42" || edit.fields["message_id"] != "99" || edit.fields["text"] != "page 2" {
		t.Errorf("edit call = %+v", edit)
	}
	if !strings.Contains(edit.fields["reply_markup"], "p:subs:1") {
		t.Errorf("edit reply_markup = %q", edit.fields["reply_markup"])
	}
	answer := (*calls)[1]
	if answer.method != "answerCallbackQuery" || answer.fields["callback_query_id"] != "cb1" {
		t.Errorf("answer call = %+v", answer)
	}
}

func TestAPIClientSendDocumentUploadsFile(t *testing.T) {
	c, calls := newAPIClient(t)
	path := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(path, []byte("mp3-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.SendDocument(context.Background(), 42, path); err != nil {
		t.Fatal(err)
	}

	call := (*calls)[0]
	if call.method != "sendDocument" || call.fields["chat_id"] != "42" || call.file != "song.mp3=mp3-bytes" {
		t.Errorf("call = %+v", call)
	}
}

func TestAPIClientSendDocumentMissingFile(t *testing.T) {
	c, calls := newAPIClient(t)

	if err := c.SendDocument(context.Background(), 42, "/no/such/file.mp3"); err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if len(*calls) != 0 {
		t.Errorf("calls = %+v, want none", *calls)
	}
}
