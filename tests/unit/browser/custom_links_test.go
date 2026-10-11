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

func TestCustomLinkUsesNativeGraphQLForOneProduct(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/offer/product":
			fmt.Fprint(w, `{"code":0,"data":{"item_id":"2"}}`)
		case "/api/v3/gql":
			if r.Method != "POST" || r.URL.Query().Get("q") != "batchCustomLink" || r.Header.Get("X-Fixture-Native") != "yes" {
				t.Error("missing native authenticated batch request")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			requests <- body
			fmt.Fprint(w, `{"data":{"batchCustomLink":[{"shortLink":"https://s.shopee.vn/first","longLink":"https://shopee.vn/an_redir","failCode":0}]}}`)
		default:
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
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	params := make([]CustomLinkParam, 1)
	for i := range params {
		params[i] = CustomLinkParam{Shop: "1", Item: fmt.Sprint(i + 2), SubIDs: [5]string{"customer", "hoanxu", fmt.Sprint("link", i), "0p63", "signature"}}
	}
	request, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	links, err := m.CreateCustomLinks(request, params)
	if err != nil || len(links) != 1 {
		t.Fatal(links, err)
	}
	if links[0].URL != "https://s.shopee.vn/first" || links[0].Err != nil {
		t.Fatal(links)
	}
	body := <-requests
	if body["operationName"] != "batchGetCustomLink" {
		t.Fatal(body)
	}
	variables := body["variables"].(map[string]any)
	if variables["sourceCaller"] != "CUSTOM_LINK_CALLER" {
		t.Fatal(variables)
	}
	rows := variables["linkParams"].([]any)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	for i, row := range rows {
		p := row.(map[string]any)
		if p["originalLink"] != fmt.Sprintf("https://shopee.vn/product/1/%d", i+2) || p["advancedLinkParams"].(map[string]any)["subId3"] != fmt.Sprint("link", i) {
			t.Fatal(p)
		}
	}
}

func TestCustomLinkBatchValidatesSizeBeforeBrowserAccess(t *testing.T) {
	m := New("", t.TempDir())
	for _, count := range []int{0, 2, 5, 6} {
		if _, err := m.CreateCustomLinks(context.Background(), make([]CustomLinkParam, count)); err == nil || err.Error() != "SHOPEE_RESPONSE_INVALID" {
			t.Fatal(count, err)
		}
	}
}

func TestCustomLinkRequestMustMatchEveryOrderedSignedProduct(t *testing.T) {
	expected := offerLinkRequest{Params: []CustomLinkParam{
		{Shop: "1", Item: "2", SubIDs: [5]string{"customer", "hoanxu", "first", "0p63", "signature1"}},
	}}
	valid := `{"operationName":"batchGetCustomLink","variables":{"sourceCaller":"CUSTOM_LINK_CALLER","linkParams":[{"originalLink":"https://shopee.vn/product/1/2","advancedLinkParams":{"subId1":"customer","subId2":"hoanxu","subId3":"first","subId4":"0p63","subId5":"signature1"}}]}}`
	if !matchesOfferLinkRequest(valid, expected) {
		t.Fatal("valid batch rejected")
	}
	for _, raw := range []string{
		strings.Replace(valid, "customer", "another", 1), strings.Replace(valid, "signature1", "signature2", 1),
		strings.Replace(valid, "/1/2", "/1/3", 1), strings.Replace(valid, "CUSTOM_LINK_CALLER", "WEB_SITE_CALLER", 1),
		strings.Replace(valid, "batchGetCustomLink", "batchGetProductOfferLink", 1),
	} {
		if matchesOfferLinkRequest(raw, expected) {
			t.Fatal("mismatched tracking accepted")
		}
	}
	if _, err := parseCustomLinkResponse([]byte(`{"data":{"batchCustomLink":[{"shortLink":"https://s.shopee.vn/first","failCode":0}]}}`), 2); err == nil {
		t.Fatal("missing batch row accepted")
	}
}

// Models the public app's webpack module, including its native request headers.
const nativeCustomLinkHTML = `<script>
const nativeModules = {61288: (e,t,n) => {
 t.ym = function(e) {
   return fetch('/api/v3/gql?q=batchCustomLink', {method:'POST',headers:{'Content-Type':'application/json','csrf-token':'fixture-csrf','X-Fixture-Native':'yes'},body:JSON.stringify({operationName:'batchGetCustomLink',query:'query batchGetCustomLink($linkParams: [CustomLinkParam!], $sourceCaller: SourceCaller){batchCustomLink(linkParams: $linkParams, sourceCaller: $sourceCaller){shortLink longLink failCode}}',variables:{linkParams:e.links.map(t=>({originalLink:t,advancedLinkParams:e.advancedLinkParams})),sourceCaller:'CUSTOM_LINK_CALLER'}})}).then(r=>r.json());
 };
}};
const nativeRequire = id => { const exports={};nativeModules[id](null,exports,nativeRequire);return exports; };
nativeRequire.m = nativeModules;
self.webpackChunkaffiliate_network = [];
self.webpackChunkaffiliate_network.push = chunk => chunk[2](nativeRequire);
</script>`
