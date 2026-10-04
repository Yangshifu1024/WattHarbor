package serviceclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPErrorRetainsStatusAndSentinelErrors(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 503} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(fmt.Sprintf("%s-%d", method, status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Service-Token") != "private-token" || r.Method != method {
						t.Error("invalid request")
					}
					w.WriteHeader(status)
					fmt.Fprint(w, "private response")
				}))
				defer server.Close()
				client := Client{HTTP: server.Client()}
				var err error
				if method == http.MethodGet {
					_, err = client.Get(context.Background(), server.URL, "private-token", "/resource")
				} else {
					err = client.Post(context.Background(), server.URL, "private-token", "/resource", struct{}{}, nil)
				}
				var rejection *HTTPError
				if !errors.As(err, &rejection) || rejection.StatusCode != status || rejection.Path != "/resource" {
					t.Fatalf("lost HTTP status: %v", err)
				}
				if errors.Is(err, ErrUnauthorized) != (status == 401 || status == 403) || errors.Is(err, ErrNotFound) != (status == 404) {
					t.Fatalf("lost sentinel mapping: %v", err)
				}
				if strings.Contains(err.Error(), "private") {
					t.Fatalf("leaked credentials or response: %v", err)
				}
			})
		}
	}
}

func TestConfiguredClientRejectsRedirectWithoutForwardingToken(t *testing.T) {
	forwarded := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := Client{HTTP: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		var err error
		if method == http.MethodGet {
			_, err = client.Get(context.Background(), server.URL, "secret", "/resource")
		} else {
			err = client.Post(context.Background(), server.URL, "secret", "/resource", struct{}{}, nil)
		}
		var rejection *HTTPError
		if !errors.As(err, &rejection) || rejection.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("redirect accepted: %v", err)
		}
	}
	if forwarded != 0 {
		t.Fatalf("followed %d redirects", forwarded)
	}
}
