// Package tg is a minimal Telegram Bot API HTTP client.
package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const DefaultBaseURL = "https://api.telegram.org"

type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// Request is a prepared Bot API call. Params holds JSON-compatible values,
// Files maps a field name to a local path that is uploaded as multipart.
type Request struct {
	Method string
	Params map[string]any
	Files  map[string]string
}

type APIError struct {
	Code        int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram error %d: %s", e.Code, e.Description)
}

// IsAuth reports whether the error means the bot token was rejected.
// Telegram answers 401 for a revoked token and 404 for a malformed one.
func (e *APIError) IsAuth() bool { return e.Code == 401 || e.Code == 404 }

func (c *Client) endpoint(method string) string {
	return strings.TrimRight(c.baseURL(), "/") + "/bot" + c.Token + "/" + method
}

func (c *Client) baseURL() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return c.BaseURL
}

func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

// Redact removes the bot token from any text that may reach logs or output.
func (c *Client) Redact(s string) string {
	if c.Token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.Token, "<TOKEN>")
}

// DryRun describes the request that Call would send, without credentials.
func DryRun(baseURL string, r Request) map[string]any {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	out := map[string]any{
		"method": r.Method,
		"url":    strings.TrimRight(baseURL, "/") + "/bot<TOKEN>/" + r.Method,
		"body":   r.Params,
	}
	if len(r.Files) > 0 {
		out["files"] = r.Files
		out["content_type"] = "multipart/form-data"
	} else {
		out["content_type"] = "application/json"
	}
	return out
}

func (c *Client) Call(ctx context.Context, r Request) (json.RawMessage, error) {
	body, contentType, err := encode(r)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(r.Method), body)
	if err != nil {
		return nil, errors.New(c.Redact(err.Error()))
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("request %s failed: %s", r.Method, c.Redact(err.Error()))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %s", c.Redact(err.Error()))
	}
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, &APIError{Code: resp.StatusCode, Description: "non-JSON response: HTTP " + resp.Status}
	}
	if !env.OK {
		code := env.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return nil, &APIError{Code: code, Description: env.Description, Parameters: env.Parameters}
	}
	return env.Result, nil
}

func encode(r Request) (io.Reader, string, error) {
	if len(r.Files) == 0 {
		b, err := json.Marshal(r.Params)
		if err != nil {
			return nil, "", err
		}
		return bytes.NewReader(b), "application/json", nil
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	names := make([]string, 0, len(r.Params))
	for k := range r.Params {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if err := w.WriteField(k, formValue(r.Params[k])); err != nil {
			return nil, "", err
		}
	}
	for field, path := range r.Files {
		f, err := os.Open(path)
		if err != nil {
			return nil, "", err
		}
		part, err := w.CreateFormFile(field, filepath.Base(path))
		if err == nil {
			_, err = io.Copy(part, f)
		}
		f.Close()
		if err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// formValue renders a param for a multipart field: plain text for scalars,
// JSON for objects and arrays, as the Bot API expects.
func formValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool, int64, float64, int:
		return fmt.Sprint(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// Download fetches a file previously resolved with getFile into dst.
func (c *Client) Download(ctx context.Context, filePath, dst string) error {
	u := strings.TrimRight(c.baseURL(), "/") + "/file/bot" + c.Token + "/" + filePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errors.New(c.Redact(err.Error()))
	}
	resp, err := c.http().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("download failed: %s", c.Redact(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Code: resp.StatusCode, Description: "download failed: HTTP " + resp.Status}
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
