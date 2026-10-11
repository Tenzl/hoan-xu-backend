package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCustomLinkResponseRequiresSuccessfulShortShopeeURL(t *testing.T) {
	valid := `{"data":{"batchCustomLink":[{"shortLink":"https://s.shopee.vn/3B7ybQjO2E","longLink":"https://shopee.vn/an_redir","failCode":0}]}}`
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", valid, true},
		{"rejected link", strings.Replace(valid, `"failCode":0`, `"failCode":2`, 1), false},
		{"rejected subid", strings.Replace(valid, `"failCode":0`, `"failCode":6`, 1), false},
		{"external URL", strings.Replace(valid, "s.shopee.vn", "evil.invalid", 1), false},
		{"long URL", strings.Replace(valid, "3B7ybQjO2E", "an_redir?sub_id=x", 1), false},
		{"credentials", strings.Replace(valid, "https://", "https://user@", 1), false},
		{"fragment", strings.Replace(valid, "3B7ybQjO2E", "3B7ybQjO2E#x", 1), false},
		{"graphql errors", strings.Replace(valid, `{"data":`, `{"errors":[{"message":"private upstream error"}],"data":`, 1), false},
		{"empty", `{"data":{"batchCustomLink":[]}}`, false},
		{"invalid", `not-json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, err := parseCustomLinkResponse([]byte(tc.body), 1)
			link := ""
			if err == nil {
				link, err = results[0].URL, results[0].Err
			}
			if tc.valid {
				if err != nil || link != "https://s.shopee.vn/3B7ybQjO2E" {
					t.Fatal(link, err)
				}
			} else if err == nil || link != "" || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe response accepted", link, err)
			}
		})
	}
}

func TestSingleCustomLinkUsesLoggedInBrowserAndCustomerSubID(t *testing.T) {
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/offer/product":
			fmt.Fprint(w, `{"code":0,"data":{"item_id":"6939920023"}}`)
		case "/api/v3/gql":
			// The real Affiliate app supplies request headers that a bare fetch lacks.
			if r.Header.Get("X-Fixture-Native") != "yes" {
				fmt.Fprint(w, `{"error":90309999}`)
				return
			}
			if r.Method != "POST" || r.URL.Query().Get("q") != "batchCustomLink" || r.Header.Get("csrf-token") != "fixture-csrf" {
				t.Error("invalid authenticated GraphQL request")
			}
			cookie, err := r.Cookie("fixture-session")
			if err != nil || cookie.Value != "logged-in" {
				t.Error("browser session missing")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["variables"].(map[string]any)["linkParams"].([]any)[0].(map[string]any)["advancedLinkParams"].(map[string]any)["subId3"] != "" {
				requests <- body
			}
			fmt.Fprint(w, `{"data":{"batchCustomLink":[{"shortLink":"https://s.shopee.vn/3B7ybQjO2E","longLink":"https://shopee.vn/an_redir","failCode":0}]}}`)
		default:
			http.SetCookie(w, &http.Cookie{Name: "fixture-session", Value: "logged-in", Path: "/", HttpOnly: true})
			fmt.Fprint(w, remoteSPAHTML+nativeCustomLinkHTML)
		}
	}))
	defer server.Close()
	m := workerTestManager(t, "local")
	u, _ := url.Parse(server.URL)
	m.responseHost, m.probeURL, m.offerBaseURL = u.Host, server.URL+"/dashboard", server.URL+"/offer/"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.probe(ctx)
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	for _, tc := range []struct{ customer, link string }{{"customer-one", "link-one"}, {"customer-one", "link-two"}, {"customer-two", "link-three"}} {
		request, stop := context.WithTimeout(ctx, 10*time.Second)
		links, err := m.CreateCustomLinks(request, []CustomLinkParam{{Shop: "83496725", Item: "6939920023", SubIDs: [5]string{tc.customer, "hoanxu", tc.link, "0p63", "abcdef1234"}}})
		stop()
		if err != nil || len(links) != 1 || links[0].Err != nil || links[0].URL != "https://s.shopee.vn/3B7ybQjO2E" {
			t.Fatal(links, err)
		}
		body := <-requests
		if body["operationName"] != "batchGetCustomLink" {
			t.Fatal(body)
		}
		variables := body["variables"].(map[string]any)
		params := variables["linkParams"].([]any)[0].(map[string]any)
		advanced := params["advancedLinkParams"].(map[string]any)
		if variables["sourceCaller"] != "CUSTOM_LINK_CALLER" || params["originalLink"] != "https://shopee.vn/product/83496725/6939920023" || advanced["subId1"] != tc.customer || advanced["subId2"] != "hoanxu" || advanced["subId3"] != tc.link || advanced["subId4"] != "0p63" || advanced["subId5"] != "abcdef1234" {
			t.Fatal("wrong product/tracking", body)
		}
	}
}

func TestSingleCustomLinkRequiresAuthenticatedSession(t *testing.T) {
	m := New("", t.TempDir())
	if _, err := m.CreateCustomLinks(context.Background(), []CustomLinkParam{{Shop: "1", Item: "2", SubIDs: [5]string{"customer", "hoanxu", "tracking", "0p63", "abcdef1234"}}}); err == nil || err.Error() != "SHOPEE_LOGIN_REQUIRED" {
		t.Fatal(err)
	}
}

func TestCustomLinkRejectsSubIDFailureWithoutReturningStandardURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/offer/product" {
			fmt.Fprint(w, `{"code":0,"data":{"item_id":"6939920023"}}`)
			return
		}
		if r.URL.Path == "/api/v3/gql" {
			fmt.Fprint(w, `{"data":{"batchCustomLink":[{"shortLink":"https://s.shopee.vn/Standard","failCode":6}]}}`)
			return
		}
		fmt.Fprint(w, remoteSPAHTML+nativeCustomLinkHTML)
	}))
	defer server.Close()
	m := workerTestManager(t, "local")
	u, _ := url.Parse(server.URL)
	m.responseHost, m.probeURL, m.offerBaseURL = u.Host, server.URL+"/dashboard", server.URL+"/offer/"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.probe(ctx)
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	request, stop := context.WithTimeout(ctx, 8*time.Second)
	defer stop()
	links, e := m.CreateCustomLinks(request, []CustomLinkParam{{Shop: "83496725", Item: "6939920023", SubIDs: [5]string{"customer", "hoanxu", "tracking", "0.63", "signature"}}})
	if e != nil || len(links) != 1 || links[0].Err == nil || links[0].Err.Error() != "SHOPEE_SUBID_REJECTED" || links[0].URL != "" {
		t.Fatal("invalid advanced form returned a link", links, e)
	}
}
