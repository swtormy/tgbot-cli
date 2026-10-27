package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// userHome makes a profile that looks logged in. No test here talks to Telegram.
func userHome(t *testing.T, access string) map[string]string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "user", "default")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"api_id":1,"api_hash":"h","access":"` + access + `","name":"Test"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"TGBOT_HOME": home}
}

func TestUserNotLoggedInIsAuthError(t *testing.T) {
	env := map[string]string{"TGBOT_HOME": t.TempDir()}
	for _, args := range [][]string{{"user", "status"}, {"user", "dialogs"}, {"user", "history", "me"}} {
		r := run(t, env, "", args...)
		if r.code != 3 || !strings.Contains(r.err, `"type":"auth"`) || !strings.Contains(r.err, "tgbot user login") {
			t.Errorf("%v: exit %d err %s", args, r.code, r.err)
		}
	}
}

func TestUserWritesBlockedWhenReadOnly(t *testing.T) {
	env := userHome(t, "read")
	for _, args := range [][]string{
		{"user", "send", "@alice", "hi"},
		{"user", "edit", "@alice", "5", "x"},
		{"user", "delete", "@alice", "5"},
		{"user", "forward", "@alice", "@bob", "5"},
		{"user", "read", "@alice"},
		{"user", "join", "@chan"},
		{"user", "leave", "@chan"},
		{"user", "raw", "messages.sendMessage", `{"peer":{"@type":"inputPeerSelf"},"message":"x"}`},
	} {
		r := run(t, env, "", args...)
		if r.code != 4 || !strings.Contains(r.err, `"type":"blocked"`) || !strings.Contains(r.err, "access write") {
			t.Errorf("%v: exit %d err %s", args, r.code, r.err)
		}
	}
}

func TestUserRawNeverAllowsDangerousMethods(t *testing.T) {
	env := userHome(t, "write")
	for _, m := range []string{"auth.logOut", "account.deleteAccount", "account.updatePasswordSettings", "auth.exportLoginToken"} {
		for _, extra := range [][]string{nil, {"--dry-run"}} {
			r := run(t, env, "", append([]string{"user", "raw", m, "{}"}, extra...)...)
			if r.code != 4 {
				t.Errorf("%s %v: exit %d err %s", m, extra, r.code, r.err)
			}
		}
	}
}

func TestUserRawDryRunNeedsNoNetwork(t *testing.T) {
	env := userHome(t, "read")
	r := run(t, env, "", "user", "raw", "messages.getHistory", `{"peer":{"@peer":"@durov"},"limit":3}`, "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d %s", r.code, r.err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v["class"] != "read" || v["dry_run"] != true {
		t.Fatalf("out %s (%v)", r.stdout, err)
	}
}

func TestUserRawListAndDescribeWorkOffline(t *testing.T) {
	env := map[string]string{"TGBOT_HOME": t.TempDir()}
	r := run(t, env, "", "user", "raw", "list", "contacts.")
	if r.code != 0 || !strings.Contains(r.stdout, "contacts.search") {
		t.Fatalf("list: exit %d %s %s", r.code, r.stdout, r.err)
	}
	r = run(t, env, "", "user", "raw", "describe", "messages.getHistory")
	if r.code != 0 || !strings.Contains(r.stdout, "peer") {
		t.Fatalf("describe: exit %d %s %s", r.code, r.stdout, r.err)
	}
}

func TestUserSendDryRunReportsWriteAccess(t *testing.T) {
	for access, want := range map[string]bool{"read": false, "write": true} {
		r := run(t, userHome(t, access), "", "user", "send", "@alice", "hi", "--dry-run")
		if r.code != 0 {
			t.Fatalf("%s: exit %d %s", access, r.code, r.err)
		}
		var v map[string]any
		_ = json.Unmarshal([]byte(r.stdout), &v)
		if v["write_access"] != want {
			t.Errorf("%s: write_access = %v", access, v["write_access"])
		}
	}
}

func TestUserAccessWriteNeedsHumanAtTerminal(t *testing.T) {
	env := userHome(t, "read")
	r := run(t, env, "write\n", "user", "access", "write")
	if r.code != 4 {
		t.Fatalf("piped stdin granted write: exit %d %s", r.code, r.err)
	}
	if r := run(t, env, "", "user", "access"); !strings.Contains(r.stdout, `"access":"read"`) {
		t.Fatalf("access changed: %s", r.stdout)
	}

	old := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	defer func() { isTerminal = old }()

	if r := run(t, env, "no\n", "user", "access", "write"); r.code == 0 {
		t.Fatalf("wrong confirmation accepted: %s", r.stdout)
	}
	r = run(t, env, "write\n", "user", "access", "write")
	if r.code != 0 || !strings.Contains(r.stdout, `"access":"write"`) {
		t.Fatalf("confirmed grant failed: exit %d %s %s", r.code, r.stdout, r.err)
	}
	if r := run(t, env, "", "user", "access", "read"); !strings.Contains(r.stdout, `"access":"read"`) {
		t.Fatalf("switch back failed: %s", r.stdout)
	}
}

func TestUserLogoutNeedsHumanAtTerminal(t *testing.T) {
	env := userHome(t, "read")
	r := run(t, env, "yes\n", "user", "logout")
	if r.code == 0 {
		t.Fatalf("logout ran without a terminal: %s", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(env["TGBOT_HOME"], "user", "default", "session.json")); err != nil {
		t.Fatalf("session file was removed: %v", err)
	}
}

func TestProfileNameIsValidated(t *testing.T) {
	r := run(t, map[string]string{"TGBOT_HOME": t.TempDir()}, "", "user", "status", "--profile", "../evil")
	if r.code != 2 {
		t.Fatalf("exit %d %s", r.code, r.err)
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"24h":                  now.Add(-24 * time.Hour),
		"7d":                   now.Add(-7 * 24 * time.Hour),
		"2026-10-01":           time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		"2026-10-01T09:30:00Z": time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC),
	} {
		got, err := parseWhen(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseWhen(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseWhen("yesterdayish", now); err == nil {
		t.Error("garbage accepted")
	}
	if got, err := parseWhen("", now); err != nil || !got.IsZero() {
		t.Errorf("empty = %v, %v", got, err)
	}
}

func TestParseFuture(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	got, err := parseFuture("+30m", now)
	if err != nil || !got.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parseFuture("soon", now); err == nil {
		t.Error("garbage accepted")
	}
}

func TestUserSkill(t *testing.T) {
	r := run(t, nil, "", "skill", "print", "tgbot-user")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "---\nname: tgbot-user\ndescription:") {
		t.Fatalf("print: exit %d %.80s", r.code, r.stdout)
	}
	if r := run(t, nil, "", "skill", "print", "nope"); r.code != 2 {
		t.Fatalf("unknown skill: exit %d", r.code)
	}
	wd, _ := os.Getwd()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.Chdir(tmp)
	defer os.Chdir(wd)
	r = run(t, nil, "", "skill", "install", "tgbot-user", "--agent", "cursor")
	want := filepath.Join(tmp, skillDirs["cursor"], "tgbot-user", "SKILL.md")
	if r.code != 0 || r.stdout != want {
		t.Fatalf("install: exit %d out %q want %q (%s)", r.code, r.stdout, want, r.err)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != skillUserMD {
		t.Fatalf("installed skill differs: %v", err)
	}
}

func TestLoginResendNeedsAPendingCode(t *testing.T) {
	env := map[string]string{"TGBOT_HOME": t.TempDir()}
	r := run(t, env, "", "user", "login", "--api-id", "1", "--api-hash", "x", "--phone", "+15550001111", "--resend", "--connect-timeout", "2s")
	if r.code != 2 || !strings.Contains(r.err, "no code was requested") {
		t.Fatalf("exit %d err %s", r.code, r.err)
	}
}

func TestSessionExportNeedsTerminalAndImportRejectsJunk(t *testing.T) {
	env := map[string]string{"TGBOT_HOME": t.TempDir()}
	r := run(t, env, "", "user", "session", "export")
	if r.code != exitBlocked {
		t.Fatalf("export without terminal: exit %d %s", r.code, r.err)
	}
	r = run(t, env, "", "user", "login", "--api-id", "1", "--api-hash", "x", "--import", "junk")
	if r.code != 2 || !strings.Contains(r.err, "not a tgbot session string") {
		t.Fatalf("exit %d err %s", r.code, r.err)
	}
}
