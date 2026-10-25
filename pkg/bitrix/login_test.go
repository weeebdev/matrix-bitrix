package bitrix

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestLoginFailureReason(t *testing.T) {
	resp := &http.Response{StatusCode: 200, Request: &http.Request{URL: &url.URL{Path: "/auth/"}}}
	cases := map[string]string{
		`<input type="hidden" name="captcha_sid" value="x">`:                     "portal asks for a captcha",
		`<div class="login-error"><span>Неверный логин или пароль.</span></div>`: "portal says: Неверный логин или пароль.",
		`<font class="errortext">Account locked<br>try later</font>`:             "portal says: Account locked try later",
		`<html><body><form>login form</form></body></html>`:                      "no session after submitting the login form (HTTP 200 on /auth/)",
	}
	for page, want := range cases {
		if got := loginFailureReason(resp, []byte(page)); got != want {
			t.Errorf("%q: got %q, want %q", page, got, want)
		}
	}
	long := `<div class="errortext">` + strings.Repeat("ж", 300) + `</div>`
	if got := []rune(loginFailureReason(resp, []byte(long))); len(got) != len([]rune("portal says: "))+200 {
		t.Errorf("long message not cut to 200 runes: %d", len(got))
	}
}
