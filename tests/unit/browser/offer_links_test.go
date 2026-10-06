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

func TestOfferLinkResponseRequiresMatchingProductAndShortShopeeURL(t *testing.T) {
	valid := `{"data":{"productOfferLinks":[{"itemId":"6939920023","shopId":"83496725","productOfferLink":"https://s.shopee.vn/3B7ybQjO2E"}]}}`
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", valid, true},
		{"wrong item", strings.Replace(valid, "6939920023", "99", 1), false},
		{"wrong shop", strings.Replace(valid, "83496725", "99", 1), false},
		{"external URL", strings.Replace(valid, "s.shopee.vn", "evil.invalid", 1), false},
		{"long URL", strings.Replace(valid, "3B7ybQjO2E", "an_redir?sub_id=x", 1), false},
		{"credentials", strings.Replace(valid, "https://", "https://user@", 1), false},
		{"fragment", strings.Replace(valid, "3B7ybQjO2E", "3B7ybQjO2E#x", 1), false},
		{"graphql errors", strings.Replace(valid, `{"data":`, `{"errors":[{"message":"private upstream error"}],"data":`, 1), false},
		{"empty", `{"data":{"productOfferLinks":[]}}`, false},
		{"invalid", `not-json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link, err := parseOfferLinkResponse([]byte(tc.body), "83496725", "6939920023")
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

func TestOfferLinkUsesLoggedInBrowserAndCustomerSubID(t *testing.T) {
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
			if r.Method != "POST" || r.URL.Query().Get("q") != "productOfferLinks" || r.Header.Get("csrf-token") != "fixture-csrf" {
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
			if body["variables"].(map[string]any)["advancedLinkParams"].(map[string]any)["subId3"] != "" {
				requests <- body
			}
			fmt.Fprint(w, `{"data":{"productOfferLinks":[{"itemId":"6939920023","shopId":"83496725","productOfferLink":"https://s.shopee.vn/3B7ybQjO2E"}]}}`)
		default:
			http.SetCookie(w, &http.Cookie{Name: "fixture-session", Value: "logged-in", Path: "/", HttpOnly: true})
			fmt.Fprint(w, remoteSPAHTML+nativeOfferLinkHTML)
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
		link, err := m.CreateOfferLink(request, "83496725", "6939920023", [5]string{tc.customer, "hoanxu", tc.link, "0p63", "abcdef1234"})
		stop()
		if err != nil || link != "https://s.shopee.vn/3B7ybQjO2E" {
			t.Fatal(link, err)
		}
		body := <-requests
		if body["operationName"] != "batchGetProductOfferLink" {
			t.Fatal(body)
		}
		variables := body["variables"].(map[string]any)
		params := variables["productOfferLinkParams"].([]any)[0].(map[string]any)
		advanced := variables["advancedLinkParams"].(map[string]any)
		if variables["sourceCaller"] != "WEB_SITE_CALLER" || params["itemId"] != "6939920023" || params["shopId"] != float64(83496725) || advanced["subId1"] != tc.customer || advanced["subId2"] != "hoanxu" || advanced["subId3"] != tc.link || advanced["subId4"] != "0p63" || advanced["subId5"] != "abcdef1234" {
			t.Fatal("wrong product/tracking", body)
		}
	}
}

const nativeOfferLinkHTML = `<button class="get-link-btn" disabled onclick="document.querySelector('.ant-modal-body').hidden=false;generate(false)">Get Link</button>
<div class="ant-modal-body" hidden>
  <input type="radio" value="1" onclick="document.querySelector('.sub-id-form').hidden=false">
  <form class="sub-id-form" hidden>
    <input id="getLinkModal_sub_id1"><input id="getLinkModal_sub_id2"><input id="getLinkModal_sub_id3">
    <input id="getLinkModal_sub_id4"><input id="getLinkModal_sub_id5">
    <div class="add-subid-btn"><button type="button" onclick="generate(true)">Add to Link</button></div>
  </form>
</div>
<script>
setTimeout(()=>document.querySelector('.get-link-btn').disabled=false,400);
const fieldState={};
for(let i=1;i<=5;i++) document.querySelector('#getLinkModal_sub_id'+i).addEventListener('input',e=>fieldState['subId'+i]=e.target.value);
function generate(advanced) {
  const params={};for(let i=1;i<=5;i++) params['subId'+i]=advanced?(fieldState['subId'+i]||''):'';
  fetch('/api/v3/gql?q=productOfferLinks', {method:'POST',headers:{'Content-Type':'application/json','csrf-token':'fixture-csrf','X-Fixture-Native':'yes'},body:JSON.stringify({operationName:'batchGetProductOfferLink',query:'fixture',variables:{sourceCaller:'WEB_SITE_CALLER',productOfferLinkParams:[{itemId:'6939920023',shopId:83496725,trace:''}],advancedLinkParams:params}})});
}
</script>`

func TestOfferLinkRequiresAuthenticatedSession(t *testing.T) {
	m := New("", t.TempDir())
	if _, err := m.CreateOfferLink(context.Background(), "1", "2", [5]string{"customer", "hoanxu", "tracking", "0p63", "abcdef1234"}); err == nil || err.Error() != "SHOPEE_LOGIN_REQUIRED" {
		t.Fatal(err)
	}
}

func TestOfferLinkRejectsNativeFormValidationWithoutReturningStandardURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/offer/product" {
			fmt.Fprint(w, `{"code":0,"data":{"item_id":"6939920023"}}`)
			return
		}
		if r.URL.Path == "/api/v3/gql" {
			fmt.Fprint(w, `{"data":{"productOfferLinks":[{"itemId":"6939920023","shopId":"83496725","productOfferLink":"https://s.shopee.vn/Standard"}]}}`)
			return
		}
		fmt.Fprint(w, remoteSPAHTML+nativeOfferLinkHTML+`<script>
 document.querySelector('.add-subid-btn button').onclick=()=>{document.querySelector('#getLinkModal_sub_id4').parentElement.classList.add('ant-form-item-has-error')};
 </script>`)
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
	link, e := m.CreateOfferLink(request, "83496725", "6939920023", [5]string{"customer", "hoanxu", "tracking", "0.63", "signature"})
	if e == nil || e.Error() != "SHOPEE_SUBID_REJECTED" || link != "" {
		t.Fatal("invalid advanced form returned a link", link, e)
	}
}
