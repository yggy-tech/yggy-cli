package devicelogin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type server struct {
	t         *testing.T
	polls     []func(w http.ResponseWriter)
	seen      int
	startBody map[string]string
	pollBody  map[string]string
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(codePath, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &s.startBody)
		_, _ = io.WriteString(w, `{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":"https://app.yggy.tech/device","verification_uri_complete":"https://app.yggy.tech/device?user_code=ABCD-EFGH","expires_in":900,"interval":5}`)
	})
	mux.HandleFunc(keyPath, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &s.pollBody)
		if s.seen >= len(s.polls) {
			s.t.Fatalf("polled %d times", s.seen+1)
		}
		s.polls[s.seen](w)
		s.seen++
	})
	return mux
}

func oauth(status int, name string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":"`+name+`","error_description":"x"}`)
	}
}

func key(w http.ResponseWriter) {
	_, _ = io.WriteString(w, `{"access_token":"ygg_secret","token_type":"Bearer","key_id":"k1"}`)
}

func client(t *testing.T, s *server) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	var waits []time.Duration
	return &Client{
		BaseURL: srv.URL,
		HTTP:    srv.Client(),
		Sleep:   func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil },
	}, &waits
}

func TestStartSendsTheClientIDAndPrefersTheCompleteURL(t *testing.T) {
	s := &server{t: t}
	c, _ := client(t, s)
	code, err := c.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.startBody["client_id"] != "yggy-cli" {
		t.Fatalf("start body = %v", s.startBody)
	}
	if code.UserCode != "ABCD-EFGH" || code.URL() != "https://app.yggy.tech/device?user_code=ABCD-EFGH" {
		t.Fatalf("code = %+v url %q", code, code.URL())
	}
}

func TestWaitPollsUntilApprovedAndReturnsTheKey(t *testing.T) {
	s := &server{t: t, polls: []func(http.ResponseWriter){
		oauth(400, "authorization_pending"), oauth(400, "authorization_pending"), key,
	}}
	c, waits := client(t, s)
	got, err := c.Wait(context.Background(), Code{DeviceCode: "dc", Interval: 5, ExpiresIn: 900})
	if err != nil || got != "ygg_secret" {
		t.Fatalf("Wait = %q, %v", got, err)
	}
	if len(*waits) != 3 || (*waits)[0] != 5*time.Second {
		t.Fatalf("waits = %v", *waits)
	}
	if s.pollBody["grant_type"] != grantType || s.pollBody["device_code"] != "dc" || s.pollBody["client_id"] != "yggy-cli" {
		t.Fatalf("poll body = %v", s.pollBody)
	}
}

func TestWaitSlowsDownWhenTheServerAsks(t *testing.T) {
	s := &server{t: t, polls: []func(http.ResponseWriter){oauth(400, "slow_down"), key}}
	c, waits := client(t, s)
	if _, err := c.Wait(context.Background(), Code{DeviceCode: "dc", Interval: 5, ExpiresIn: 900}); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 2 || (*waits)[1] != 10*time.Second {
		t.Fatalf("waits = %v, want the interval to grow by 5s", *waits)
	}
}

func TestWaitStopsOnDenialAndExpiry(t *testing.T) {
	s := &server{t: t, polls: []func(http.ResponseWriter){oauth(400, "access_denied")}}
	c, _ := client(t, s)
	if _, err := c.Wait(context.Background(), Code{DeviceCode: "dc", ExpiresIn: 900}); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied: err = %v", err)
	}
	s = &server{t: t, polls: []func(http.ResponseWriter){oauth(400, "expired_token")}}
	c, _ = client(t, s)
	if _, err := c.Wait(context.Background(), Code{DeviceCode: "dc", ExpiresIn: 900}); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: err = %v", err)
	}
}

func TestWaitGivesUpAtTheLocalDeadline(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1", Sleep: func(context.Context, time.Duration) error {
		time.Sleep(1100 * time.Millisecond)
		return nil
	}}
	if _, err := c.Wait(context.Background(), Code{DeviceCode: "dc", Interval: 1, ExpiresIn: 1}); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitStopsWhenTheContextIsCancelled(t *testing.T) {
	c := &Client{Sleep: func(ctx context.Context, _ time.Duration) error { return context.Canceled }}
	if _, err := c.Wait(context.Background(), Code{DeviceCode: "dc"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorsNameWhatWentWrong(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"rate limited": {429, `{"code":"RATE_LIMITED"}`, "too many login attempts"},
		"described":    {400, `{"error":"invalid_grant","error_description":"Invalid device code"}`, "Invalid device code"},
		"message only": {500, `{"message":"boom"}`, "boom"},
		"bare status":  {502, `bad gateway`, "status 502"},
	}
	for name, tc := range cases {
		if err := serverError(tc.status, []byte(tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
