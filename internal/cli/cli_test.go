package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swtormy/tgbot-cli/internal/spec"
)

const testToken = "123456:SECRET-token"

type result struct {
	code        int
	stdout, err string
}

func run(t *testing.T, env map[string]string, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	getenv := func(k string) string { return env[k] }
	code := Execute(args, strings.NewReader(stdin), &out, &errb, getenv)
	return result{code, strings.TrimSpace(out.String()), strings.TrimSpace(errb.String())}
}

func server(t *testing.T, h http.HandlerFunc) (map[string]string, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return map[string]string{"TELEGRAM_BOT_TOKEN": testToken, "TELEGRAM_API_URL": srv.URL}, srv
}

func okJSON(w http.ResponseWriter, result string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"ok":true,"result":`+result+`}`)
}

func TestDryRunNeedsNoTokenAndHidesIt(t *testing.T) {
	r := run(t, nil, "", "send", "@chan", "<b>hi</b>", "--parse_mode", "HTML", "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got["url"] != "https://api.telegram.org/bot<TOKEN>/sendMessage" {
		t.Errorf("url = %v", got["url"])
	}
	body := got["body"].(map[string]any)
	if body["chat_id"] != "@chan" || body["text"] != "<b>hi</b>" || body["parse_mode"] != "HTML" {
		t.Errorf("body = %v", body)
	}
	if !strings.Contains(r.stdout, "<b>hi</b>") {
		t.Errorf("HTML was escaped: %s", r.stdout)
	}
}

func TestSendSendsNumericChatIDAndJQ(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	env, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		okJSON(w, `{"message_id":77,"text":"x"}`)
	})
	r := run(t, env, "", "send", "123", "hello", "--jq", ".message_id")
	if r.code != 0 || r.stdout != "77" {
		t.Fatalf("exit %d out %q err %q", r.code, r.stdout, r.err)
	}
	if gotPath != "/bot"+testToken+"/sendMessage" {
		t.Errorf("path = %s", gotPath)
	}
	if gotBody["chat_id"] != float64(123) || gotBody["text"] != "hello" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestSendReadsTextFromStdin(t *testing.T) {
	var gotBody map[string]any
	env, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		okJSON(w, `{}`)
	})
	r := run(t, env, "line1\nline2\n", "send", "1", "-")
	if r.code != 0 || gotBody["text"] != "line1\nline2" {
		t.Fatalf("exit %d text %q err %s", r.code, gotBody["text"], r.err)
	}
}

func TestAPIErrorAndAuthExitCodes(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   int
		kind   string
	}{
		{400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, 1, "api"},
		{401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, 3, "auth"},
		{404, `{"ok":false,"error_code":404,"description":"Not Found"}`, 3, "auth"},
		{429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":5}}`, 1, "api"},
	}
	for _, c := range cases {
		env, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		})
		r := run(t, env, "", "getMe")
		if r.code != c.code {
			t.Errorf("status %d: exit %d, want %d", c.status, r.code, c.code)
		}
		var e struct {
			OK    bool `json:"ok"`
			Error struct {
				Type       string
				Code       int
				Message    string
				Parameters map[string]any
			}
		}
		if err := json.Unmarshal([]byte(r.err), &e); err != nil {
			t.Fatalf("stderr is not JSON: %q", r.err)
		}
		if e.OK || e.Error.Type != c.kind || e.Error.Code != c.status {
			t.Errorf("status %d: error = %+v", c.status, e.Error)
		}
		if c.status == 429 && e.Error.Parameters["retry_after"] != float64(5) {
			t.Errorf("retry_after missing: %s", r.err)
		}
	}
}

func TestMissingTokenIsAuthError(t *testing.T) {
	r := run(t, nil, "", "getMe")
	if r.code != 3 || !strings.Contains(r.err, "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("exit %d err %s", r.code, r.err)
	}
}

func TestTokenNeverLeaksOnTransportError(t *testing.T) {
	env, srv := server(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close()
	r := run(t, env, "", "getMe")
	if r.code != 1 {
		t.Fatalf("exit %d", r.code)
	}
	if strings.Contains(r.err, testToken) || strings.Contains(r.stdout, testToken) {
		t.Fatalf("token leaked: %s", r.err)
	}
}

func TestUsageErrors(t *testing.T) {
	env := map[string]string{"TELEGRAM_BOT_TOKEN": testToken}
	cases := map[string][]string{
		"missing required":   {"sendMessage", "--text", "hi"},
		"bad json":           {"sendMessage", "--chat_id", "1", "--text", "x", "--reply_markup", "{oops", "--dry-run"},
		"bad integer":        {"sendPoll", "--chat_id", "1", "--question", "q", "--options", "[]", "--open_period", "abc", "--dry-run"},
		"unknown body key":   {"sendMessage", "--body", `{"chat_id":1,"text":"x","txt":"y"}`, "--dry-run"},
		"unknown flag":       {"sendMessage", "--nope", "1"},
		"unknown command":    {"sendMesage"},
		"missing file":       {"sendPhoto", "--chat_id", "1", "--photo", "@/nonexistent.jpg", "--dry-run"},
		"file needs @":       {"setChatPhoto", "--chat_id", "1", "--photo", "plain", "--dry-run"},
		"bad jq":             {"getMe", "--jq", "{{", "--dry-run"},
		"invalid agent":      {"skill", "install", "--agent", "nope"},
		"send missing args":  {"send", "1"},
		"body not an object": {"getMe", "--body", "[1]"},
	}
	for name, args := range cases {
		r := run(t, env, "", args...)
		if r.code != 2 {
			t.Errorf("%s: exit %d (stderr %s), want 2", name, r.code, r.err)
		}
		if !json.Valid([]byte(r.err)) {
			t.Errorf("%s: stderr not JSON: %q", name, r.err)
		}
	}
}

func TestCoercion(t *testing.T) {
	r := run(t, nil, "", "sendMessage",
		"--chat_id", "-1001234567890", "--text", "42",
		"--disable_notification", "true",
		"--reply_markup", `{"inline_keyboard":[[{"text":"a","url":"https://x.y"}]]}`,
		"--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	var got struct{ Body map[string]any }
	json.Unmarshal([]byte(r.stdout), &got)
	b := got.Body
	if b["chat_id"] != float64(-1001234567890) {
		t.Errorf("chat_id = %v (%T)", b["chat_id"], b["chat_id"])
	}
	if b["text"] != "42" {
		t.Errorf("text must stay a string, got %v (%T)", b["text"], b["text"])
	}
	if b["disable_notification"] != true {
		t.Errorf("bool = %v", b["disable_notification"])
	}
	if _, ok := b["reply_markup"].(map[string]any); !ok {
		t.Errorf("reply_markup should be an object, got %T", b["reply_markup"])
	}
}

func TestBodyMergedWithFlagsOverride(t *testing.T) {
	r := run(t, nil, "", "sendMessage", "--body", `{"chat_id":1,"text":"from body"}`, "--text", "from flag", "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	if !strings.Contains(r.stdout, `"text":"from flag"`) || !strings.Contains(r.stdout, `"chat_id":1`) {
		t.Errorf("out = %s", r.stdout)
	}
}

func TestMultipartUpload(t *testing.T) {
	dir := t.TempDir()
	pic := filepath.Join(dir, "cat.jpg")
	os.WriteFile(pic, []byte("JPEGDATA"), 0o600)
	var fields = map[string]string{}
	var fileName, fileBody string
	env, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("not multipart: %v", err)
		}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		f, h, err := r.FormFile("photo")
		if err != nil {
			t.Errorf("no photo: %v", err)
			return
		}
		b, _ := io.ReadAll(f)
		fileName, fileBody = h.Filename, string(b)
		okJSON(w, `{"message_id":1}`)
	})
	r := run(t, env, "", "sendPhoto", "--chat_id", "5", "--photo", "@"+pic, "--caption", "meow",
		"--reply_markup", `{"inline_keyboard":[]}`)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	if fileName != "cat.jpg" || fileBody != "JPEGDATA" {
		t.Errorf("file = %q %q", fileName, fileBody)
	}
	if fields["chat_id"] != "5" || fields["caption"] != "meow" || fields["reply_markup"] != `{"inline_keyboard":[]}` {
		t.Errorf("fields = %v", fields)
	}
}

func TestPhotoByFileIDStaysJSON(t *testing.T) {
	var ct string
	env, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		okJSON(w, `{}`)
	})
	r := run(t, env, "", "sendPhoto", "--chat_id", "5", "--photo", "AgACfileid")
	if r.code != 0 || ct != "application/json" {
		t.Fatalf("exit %d content-type %s err %s", r.code, ct, r.err)
	}
}

func TestUpdatesNextOffsetAndStateFile(t *testing.T) {
	state := filepath.Join(t.TempDir(), "offset")
	var offsets []any
	env, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		offsets = append(offsets, b["offset"])
		if len(offsets) == 1 {
			okJSON(w, `[{"update_id":5,"message":{"text":"a"}},{"update_id":6,"message":{"text":"b"}}]`)
			return
		}
		okJSON(w, `[]`)
	})
	r := run(t, env, "", "updates", "--state-file", state, "--jq", ".next_offset")
	if r.code != 0 || r.stdout != "7" {
		t.Fatalf("exit %d out %q err %s", r.code, r.stdout, r.err)
	}
	if b, _ := os.ReadFile(state); strings.TrimSpace(string(b)) != "7" {
		t.Errorf("state = %q", b)
	}
	r = run(t, env, "", "updates", "--state-file", state)
	if r.code != 0 || r.stdout != `{"next_offset":7,"updates":[]}` {
		t.Fatalf("second call: exit %d out %q err %s", r.code, r.stdout, r.err)
	}
	if offsets[0] != nil || offsets[1] != float64(7) {
		t.Errorf("offsets sent = %v", offsets)
	}
}

func TestUpdatesEmptyWithoutOffset(t *testing.T) {
	env, _ := server(t, func(w http.ResponseWriter, r *http.Request) { okJSON(w, `null`) })
	r := run(t, env, "", "updates")
	if r.code != 0 || r.stdout != `{"updates":[]}` {
		t.Fatalf("exit %d out %q err %s", r.code, r.stdout, r.err)
	}
}

func TestDownload(t *testing.T) {
	env, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			okJSON(w, `{"file_id":"X","file_path":"photos/file_1.jpg"}`)
		case r.URL.Path == "/file/bot"+testToken+"/photos/file_1.jpg":
			io.WriteString(w, "BYTES")
		default:
			http.NotFound(w, r)
		}
	})
	dir := t.TempDir()
	r := run(t, env, "", "download", "X", "--out", dir)
	want := filepath.Join(dir, "file_1.jpg")
	if r.code != 0 || r.stdout != want {
		t.Fatalf("exit %d out %q err %s", r.code, r.stdout, r.err)
	}
	if b, _ := os.ReadFile(want); string(b) != "BYTES" {
		t.Errorf("content = %q", b)
	}
}

func TestSchemaAndUsageNeedNoToken(t *testing.T) {
	r := run(t, nil, "", "sendMessage", "--schema")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	var s struct {
		Schema     string         `json:"$schema"`
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Schema, "2020-12") || s.Properties["chat_id"] == nil || len(s.Required) < 2 {
		t.Errorf("schema = %s", r.stdout)
	}

	r = run(t, nil, "", "--usage")
	if r.code != 0 || !json.Valid([]byte(r.stdout)) || !strings.Contains(r.stdout, `"chat_id!:Integer|String"`) {
		t.Fatalf("usage: exit %d, valid=%v", r.code, json.Valid([]byte(r.stdout)))
	}
}

func TestEveryMethodBuildsAndHasHelp(t *testing.T) {
	sp, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.Methods) < 100 {
		t.Fatalf("only %d methods in spec", len(sp.Methods))
	}
	reserved := map[string]bool{"body": true, "schema": true, "help": true, "dry-run": true, "jq": true, "pretty": true, "api-url": true, "http-timeout": true}
	for _, n := range sp.Names() {
		for _, f := range sp.Methods[n].Fields {
			if reserved[f.Name] {
				t.Errorf("%s: parameter %q collides with a tgbot flag", n, f.Name)
			}
		}
		r := run(t, nil, "", n, "--help")
		if r.code != 0 || !strings.Contains(r.stdout, "tgbot "+n) {
			t.Errorf("%s --help: exit %d", n, r.code)
		}
	}
}

func TestMethodsListAndRootHelpAreShort(t *testing.T) {
	r := run(t, nil, "", "methods")
	if r.code != 0 || !strings.Contains(r.stdout, "sendMessage\t") {
		t.Fatalf("methods: exit %d", r.code)
	}
	r = run(t, nil, "", "--help")
	if r.code != 0 || strings.Count(r.stdout, "\n") > 60 || strings.Contains(r.stdout, "addStickerToSet") {
		t.Errorf("root help too long or lists generated commands (%d lines)", strings.Count(r.stdout, "\n"))
	}
}

func TestSkill(t *testing.T) {
	r := run(t, nil, "", "skill", "print")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "---\nname: tgbot\ndescription:") {
		t.Fatalf("print: exit %d", r.code)
	}
	dir := filepath.Join(t.TempDir(), "x", "tgbot")
	r = run(t, nil, "", "skill", "install", "--dir", dir)
	if r.code != 0 {
		t.Fatalf("install: exit %d %s", r.code, r.err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil || string(b) != skillMD {
		t.Fatalf("installed skill differs: %v", err)
	}
	wd, _ := os.Getwd()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.Chdir(tmp)
	defer os.Chdir(wd)
	for agent, sub := range skillDirs {
		r = run(t, nil, "", "skill", "install", "--agent", agent)
		want := filepath.Join(tmp, sub, "tgbot", "SKILL.md")
		if r.code != 0 || r.stdout != want {
			t.Errorf("%s: exit %d out %q want %q", agent, r.code, r.stdout, want)
		}
	}
}
