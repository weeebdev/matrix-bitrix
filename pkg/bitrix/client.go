// Package bitrix is a client for the internal web API of self-hosted ("box") Bitrix24 portals,
// reverse-engineered from the browser messenger. It authenticates with the regular login form
// and then talks to /bitrix/services/main/ajax.php (im.v2 controllers) and /rest/*.json using
// the session cookie plus the bitrix_sessid CSRF token, exactly like the web client does.
package bitrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotLoggedIn   = errors.New("not logged in to bitrix")
	ErrInvalidLogin  = errors.New("invalid login or password")
	ErrSessionExpiry = errors.New("bitrix session expired")
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"

type Client struct {
	BaseURL *url.URL
	HTTP    *http.Client

	Login    string
	Password string

	lock   sync.RWMutex
	SessID string
	UserID int64
	SiteID string

	// OnSessionChange is called after a successful (re)login so the caller can persist cookies.
	OnSessionChange func()
}

func NewClient(baseURL string) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if !strings.Contains(baseURL, "://") {
		baseURL = "https://" + baseURL
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid portal URL: %w", err)
	} else if u.Host == "" {
		return nil, fmt.Errorf("invalid portal URL %q", baseURL)
	}
	u.Path = ""
	jar, _ := cookiejar.New(nil)
	return &Client{
		BaseURL: u,
		SiteID:  "s1",
		HTTP: &http.Client{
			Jar:     jar,
			Timeout: 60 * time.Second,
		},
	}, nil
}

func (c *Client) url(path string) string {
	return c.BaseURL.String() + path
}

// Cookies returns the session cookies so they can be persisted.
func (c *Client) Cookies() map[string]string {
	out := make(map[string]string)
	for _, ck := range c.HTTP.Jar.Cookies(c.BaseURL) {
		out[ck.Name] = ck.Value
	}
	return out
}

func (c *Client) SetCookies(cookies map[string]string) {
	list := make([]*http.Cookie, 0, len(cookies))
	for k, v := range cookies {
		list = append(list, &http.Cookie{Name: k, Value: v, Path: "/"})
	}
	c.HTTP.Jar.SetCookies(c.BaseURL, list)
}

func (c *Client) GetSessID() string {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.SessID
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "ru,en;q=0.9")
	return req, nil
}

var (
	reSessID = regexp.MustCompile(`["']bitrix_sessid["']\s*:\s*["']([0-9a-f]+)["']`)
	reUserID = regexp.MustCompile(`["']USER_ID["']\s*:\s*["']?(\d+)`)
	reSiteID = regexp.MustCompile(`["']SITE_ID["']\s*:\s*["']([a-z0-9]{2})["']`)
)

// RefreshSession loads a portal page to obtain the CSRF token and current user ID.
// Returns ErrNotLoggedIn if the cookies aren't (or are no longer) authenticated.
func (c *Client) RefreshSession(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/stream/", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("failed to load portal page: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("failed to read portal page: %w", err)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("portal page returned HTTP %d", resp.StatusCode)
	}
	sessMatch := reSessID.FindSubmatch(data)
	userMatch := reUserID.FindSubmatch(data)
	if sessMatch == nil || userMatch == nil || string(userMatch[1]) == "0" || strings.Contains(resp.Request.URL.Path, "/auth") {
		return ErrNotLoggedIn
	}
	userID, _ := strconv.ParseInt(string(userMatch[1]), 10, 64)
	if userID == 0 {
		return ErrNotLoggedIn
	}
	c.lock.Lock()
	c.SessID = string(sessMatch[1])
	c.UserID = userID
	if siteMatch := reSiteID.FindSubmatch(data); siteMatch != nil {
		c.SiteID = string(siteMatch[1])
	}
	c.lock.Unlock()
	return nil
}

var (
	reLoginError = regexp.MustCompile(`(?is)class="[^"]*(?:errortext|error-text|login-error|auth-error|alert-danger)[^"]*"[^>]*>(.{0,600}?)</(?:div|p|span|font)>`)
	reHTMLTag    = regexp.MustCompile(`<[^>]*>`)
	reSpaces     = regexp.MustCompile(`\s+`)
)

// loginFailureReason describes why the portal didn't accept a login, from the page it answered
// the login form with. It never includes the submitted credentials.
func loginFailureReason(resp *http.Response, page []byte) string {
	if strings.Contains(string(page), "captcha_sid") {
		return "portal asks for a captcha"
	}
	if m := reLoginError.FindSubmatch(page); m != nil {
		text := strings.TrimSpace(reSpaces.ReplaceAllString(reHTMLTag.ReplaceAllString(string(m[1]), " "), " "))
		if text != "" {
			if r := []rune(text); len(r) > 200 {
				text = string(r[:200])
			}
			return "portal says: " + text
		}
	}
	return fmt.Sprintf("no session after submitting the login form (HTTP %d on %s)", resp.StatusCode, resp.Request.URL.Path)
}

// PasswordLogin submits the standard Bitrix login form.
func (c *Client) PasswordLogin(ctx context.Context, login, password string) error {
	// Prime PHPSESSID
	req, err := c.newRequest(ctx, http.MethodGet, "/auth/", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("failed to open login page: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	form := url.Values{
		"AUTH_FORM":     {"Y"},
		"TYPE":          {"AUTH"},
		"backurl":       {"/auth/"},
		"USER_LOGIN":    {login},
		"USER_PASSWORD": {password},
		"USER_REMEMBER": {"Y"},
	}
	req, err = c.newRequest(ctx, http.MethodPost, "/auth/?login=yes", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("failed to submit login form: %w", err)
	}
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()

	err = c.RefreshSession(ctx)
	if errors.Is(err, ErrNotLoggedIn) {
		return fmt.Errorf("%w: %s", ErrInvalidLogin, loginFailureReason(resp, page))
	} else if err != nil {
		return err
	}
	c.Login, c.Password = login, password
	return nil
}

// EnsureSession re-fetches the CSRF token, logging in again with the stored password if needed.
func (c *Client) EnsureSession(ctx context.Context) error {
	err := c.RefreshSession(ctx)
	if errors.Is(err, ErrNotLoggedIn) && c.Login != "" && c.Password != "" {
		err = c.PasswordLogin(ctx, c.Login, c.Password)
	}
	if err == nil && c.OnSessionChange != nil {
		c.OnSessionChange()
	}
	return err
}

type ajaxResponse struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Errors []APIError      `json:"errors"`
}

type APIError struct {
	Message string `json:"message"`
	Code    any    `json:"code"`
}

type Error struct {
	Method string
	Code   string
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("bitrix %s: %s (%s)", e.Method, e.Msg, e.Code)
}

func isAuthError(code string) bool {
	switch strings.ToLower(code) {
	case "invalid_csrf", "no_auth_found", "unauthorized", "authorization_error", "expired_token", "invalid_token", "access_denied":
		return true
	}
	return false
}

// Action calls an ajax.php controller action (e.g. "im.v2.Chat.Message.send").
func (c *Client) Action(ctx context.Context, action string, params map[string]any, out any) error {
	return c.withRetry(ctx, func() error { return c.action(ctx, action, params, out) })
}

// REST calls /rest/<method>.json using session authentication.
func (c *Client) REST(ctx context.Context, method string, params map[string]any, out any) error {
	return c.withRetry(ctx, func() error { return c.rest(ctx, method, params, out) })
}

func (c *Client) withRetry(ctx context.Context, fn func() error) error {
	err := fn()
	var bErr *Error
	if errors.As(err, &bErr) && (isAuthError(bErr.Code) || bErr.Code == "HTTP 401") {
		if refreshErr := c.EnsureSession(ctx); refreshErr != nil {
			return fmt.Errorf("%w: %w", ErrSessionExpiry, refreshErr)
		}
		err = fn()
	}
	return err
}

// ActionJSON calls an ajax.php controller action with a JSON body (used by ui.entityselector).
func (c *Client) ActionJSON(ctx context.Context, action string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.withRetry(ctx, func() error { return c.doAction(ctx, action, string(data), "application/json", out) })
}

func (c *Client) action(ctx context.Context, action string, params map[string]any, out any) error {
	return c.doAction(ctx, action, EncodeForm(params).Encode(), "application/x-www-form-urlencoded", out)
}

func (c *Client) doAction(ctx context.Context, action, body, contentType string, out any) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/bitrix/services/main/ajax.php?action="+url.QueryEscape(action), strings.NewReader(body))
	if err != nil {
		return err
	}
	c.lock.RLock()
	req.Header.Set("X-Bitrix-Csrf-Token", c.SessID)
	req.Header.Set("X-Bitrix-Site-Id", c.SiteID)
	c.lock.RUnlock()
	req.Header.Set("Bx-Ajax", "true")
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s: failed to read response: %w", action, err)
	}
	var ar ajaxResponse
	if err = json.Unmarshal(data, &ar); err != nil {
		if resp.StatusCode == http.StatusUnauthorized {
			return &Error{Method: action, Code: "HTTP 401", Msg: "unauthorized"}
		}
		return fmt.Errorf("%s: HTTP %d, invalid JSON: %.200s", action, resp.StatusCode, data)
	}
	if ar.Status != "success" {
		e := &Error{Method: action, Code: "unknown", Msg: string(data)}
		if len(ar.Errors) > 0 {
			e.Code = fmt.Sprint(ar.Errors[0].Code)
			e.Msg = ar.Errors[0].Message
		}
		return e
	}
	if out != nil {
		if err = json.Unmarshal(ar.Data, out); err != nil {
			return fmt.Errorf("%s: failed to parse data: %w", action, err)
		}
	}
	return nil
}

type restResponse struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Desc   string          `json:"error_description"`
}

func (c *Client) rest(ctx context.Context, method string, params map[string]any, out any) error {
	form := EncodeForm(params)
	form.Set("sessid", c.GetSessID())
	req, err := c.newRequest(ctx, http.MethodPost, "/rest/"+method+".json", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s: failed to read response: %w", method, err)
	}
	var rr restResponse
	if err = json.Unmarshal(data, &rr); err != nil {
		if resp.StatusCode == http.StatusUnauthorized {
			return &Error{Method: method, Code: "HTTP 401", Msg: "unauthorized"}
		}
		return fmt.Errorf("%s: HTTP %d, invalid JSON: %.200s", method, resp.StatusCode, data)
	}
	if rr.Error != "" {
		return &Error{Method: method, Code: rr.Error, Msg: rr.Desc}
	}
	if out != nil {
		if err = json.Unmarshal(rr.Result, out); err != nil {
			return fmt.Errorf("%s: failed to parse result: %w", method, err)
		}
	}
	return nil
}

// Download fetches an authenticated portal URL (file downloads, avatars).
func (c *Client) Download(ctx context.Context, rawURL string) ([]byte, string, error) {
	if strings.HasPrefix(rawURL, "/") {
		rawURL = c.url(rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	return data, resp.Header.Get("Content-Type"), err
}

// EncodeForm flattens nested params into PHP-style form keys (a[b]=1, ids[0]=2).
func EncodeForm(params map[string]any) url.Values {
	out := url.Values{}
	for k, v := range params {
		encodeValue(out, k, v)
	}
	return out
}

func encodeValue(out url.Values, key string, v any) {
	switch val := v.(type) {
	case nil:
	case map[string]any:
		for k, sub := range val {
			encodeValue(out, key+"["+k+"]", sub)
		}
	case map[string]string:
		for k, sub := range val {
			out.Add(key+"["+k+"]", sub)
		}
	case []any:
		for i, sub := range val {
			encodeValue(out, key+"["+strconv.Itoa(i)+"]", sub)
		}
	case []string:
		for i, sub := range val {
			out.Add(key+"["+strconv.Itoa(i)+"]", sub)
		}
	case []int64:
		for i, sub := range val {
			out.Add(key+"["+strconv.Itoa(i)+"]", strconv.FormatInt(sub, 10))
		}
	case bool:
		if val {
			out.Add(key, "Y")
		} else {
			out.Add(key, "N")
		}
	default:
		out.Add(key, fmt.Sprint(val))
	}
}
