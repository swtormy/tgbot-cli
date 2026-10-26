package mtp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"time"
)

func isWindows() bool { return runtime.GOOS == "windows" }

const (
	AccessRead  = "read"
	AccessWrite = "write"
)

// Config is stored next to the session. It holds no Telegram login secrets,
// but api_hash should still be kept private.
type Config struct {
	APIID     int       `json:"api_id"`
	APIHash   string    `json:"api_hash"`
	Access    string    `json:"access"`
	UserID    int64     `json:"user_id,omitempty"`
	Username  string    `json:"username,omitempty"`
	Name      string    `json:"name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Profile is one logged-in account: a directory with the session, config and peer cache.
type Profile struct {
	Name string
	Dir  string
	Cfg  Config
}

var profileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// Home returns the tgbot config directory: $TGBOT_HOME or the OS config dir.
func Home(getenv func(string) string) (string, error) {
	if h := getenv("TGBOT_HOME"); h != "" {
		return h, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot find a config directory (set TGBOT_HOME): %w", err)
	}
	return filepath.Join(d, "tgbot"), nil
}

// NewProfile points at a profile directory without requiring it to exist.
func NewProfile(home, name string) (*Profile, error) {
	if name == "" {
		name = "default"
	}
	if !profileName.MatchString(name) {
		return nil, usagef("invalid profile name %q (letters, digits, - and _)", name)
	}
	return &Profile{Name: name, Dir: filepath.Join(home, "user", name)}, nil
}

func (p *Profile) SessionPath() string { return filepath.Join(p.Dir, "session.json") }
func (p *Profile) ConfigPath() string  { return filepath.Join(p.Dir, "config.json") }
func (p *Profile) PeersPath() string   { return filepath.Join(p.Dir, "peers.json") }
func (p *Profile) LockPath() string    { return filepath.Join(p.Dir, "lock") }

func (p *Profile) Exists() bool {
	_, err := os.Stat(p.SessionPath())
	return err == nil
}

// Load reads the config. It fails with KindAuth when the profile has never logged in.
func (p *Profile) Load(getenv func(string) string) error {
	b, err := os.ReadFile(p.ConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return &Error{Kind: KindAuth, Message: fmt.Sprintf("not logged in (profile %q): run `tgbot user login` in a terminal", p.Name)}
	}
	if err != nil {
		return &Error{Kind: KindRuntime, Message: err.Error(), Err: err}
	}
	if err := json.Unmarshal(b, &p.Cfg); err != nil {
		return &Error{Kind: KindRuntime, Message: "corrupt " + p.ConfigPath() + ": " + err.Error()}
	}
	if v := getenv("TGBOT_API_ID"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			p.Cfg.APIID = id
		}
	}
	if v := getenv("TGBOT_API_HASH"); v != "" {
		p.Cfg.APIHash = v
	}
	if p.Cfg.Access == "" {
		p.Cfg.Access = AccessRead
	}
	return nil
}

func (p *Profile) Save() error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p.Cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(p.ConfigPath(), b)
}

// CanWrite reports whether the profile may change anything on the account.
func (p *Profile) CanWrite() bool { return p.Cfg.Access == AccessWrite }

// RequireWrite is the single gate every state-changing command passes through.
func (p *Profile) RequireWrite(what string) error {
	if p.CanWrite() {
		return nil
	}
	return &Error{
		Kind: KindBlocked,
		Message: fmt.Sprintf("%s changes the account and this profile is read-only. "+
			"Ask the user to run `tgbot user access write` in their own terminal.", what),
	}
}
