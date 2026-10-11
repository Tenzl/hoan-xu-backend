package browser

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// CustomLinkParam carries all five encrypted tracking parts for one product.
type CustomLinkParam struct {
	Shop, Item string
	SubIDs     [5]string
}
type CustomLinkResult struct {
	URL string
	Err error
}
type offerLinkRequest struct{ Params []CustomLinkParam }

var offerID = regexp.MustCompile(`^[0-9]+$`)
var shortOfferPath = regexp.MustCompile(`^/[A-Za-z0-9]+$`)

func (m *Manager) CreateCustomLinks(ctx context.Context, params []CustomLinkParam) ([]CustomLinkResult, error) {
	if len(params) != 1 {
		return nil, failure("SHOPEE_RESPONSE_INVALID", "offer_link_input", false)
	}
	for _, p := range params {
		if !offerID.MatchString(p.Shop) || !offerID.MatchString(p.Item) {
			return nil, failure("SHOPEE_RESPONSE_INVALID", "offer_link_input", false)
		}
		for _, id := range p.SubIDs {
			if id == "" || len(id) > 50 {
				return nil, failure("SHOPEE_RESPONSE_INVALID", "offer_link_input", false)
			}
		}
	}
	body, err := m.submit(ctx, params[0].Item, &offerLinkRequest{Params: params})
	if err != nil {
		return nil, err
	}
	return parseCustomLinkResponse(body, len(params))
}

type customLinkPayload struct {
	OriginalLink string            `json:"originalLink"`
	Advanced     map[string]string `json:"advancedLinkParams"`
}

func customLinkPayloads(link offerLinkRequest) []customLinkPayload {
	params := make([]customLinkPayload, len(link.Params))
	for i, p := range link.Params {
		ids := make(map[string]string, 5)
		for j, id := range p.SubIDs {
			ids["subId"+strconv.Itoa(j+1)] = id
		}
		params[i] = customLinkPayload{OriginalLink: "https://shopee.vn/product/" + p.Shop + "/" + p.Item, Advanced: ids}
	}
	return params
}
func matchesOfferLinkRequest(raw string, link offerLinkRequest) bool {
	var request struct {
		Operation string `json:"operationName"`
		Variables struct {
			Source string              `json:"sourceCaller"`
			Links  []customLinkPayload `json:"linkParams"`
		} `json:"variables"`
	}
	if len(link.Params) != 1 || len(raw) > maxProductBody || json.Unmarshal([]byte(raw), &request) != nil || request.Operation != "batchGetCustomLink" || request.Variables.Source != "CUSTOM_LINK_CALLER" || len(request.Variables.Links) != len(link.Params) {
		return false
	}
	for i, expected := range customLinkPayloads(link) {
		actual := request.Variables.Links[i]
		if actual.OriginalLink != expected.OriginalLink || len(actual.Advanced) != 5 {
			return false
		}
		for key, value := range expected.Advanced {
			if actual.Advanced[key] != value {
				return false
			}
		}
	}
	return true
}

// Let the Affiliate app build and authenticate its own GraphQL request. A bare
// fetch/XHR omits app-provided headers even when the browser is logged in.
func (m *Manager) fetchOfferLink(ctx context.Context, link offerLinkRequest) ([]byte, error) {
	scope, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	type response struct {
		status int64
		bytes  int64
	}
	requests := map[network.RequestID]response{}
	completed := make(chan network.RequestID, 16)
	failures := make(chan *Failure, 1)
	fail := func(code string) {
		select {
		case failures <- failure(code, "offer_link", false):
		default:
		}
	}
	chromedp.ListenTarget(scope, func(event any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := event.(type) {
		case *network.EventRequestWillBeSent:
			u, err := url.Parse(e.Request.URL)
			if err == nil && u.Host == m.responseHost && u.Path == "/api/v3/gql" && u.Query().Get("q") == "batchCustomLink" && e.Request.Method == "POST" && e.DocumentURL == m.offerBaseURL+link.Params[0].Item {
				requests[e.RequestID] = response{}
			}
		case *network.EventResponseReceived:
			if r, ok := requests[e.RequestID]; ok {
				r.status = int64(e.Response.Status)
				requests[e.RequestID] = r
			}
		case *network.EventDataReceived:
			if r, ok := requests[e.RequestID]; ok {
				r.bytes += e.DataLength
				requests[e.RequestID] = r
				if r.bytes > maxProductBody {
					fail("SHOPEE_RESPONSE_INVALID")
				}
			}
		case *network.EventLoadingFailed:
			if _, ok := requests[e.RequestID]; ok {
				fail("SHOPEE_UPSTREAM_FAILED")
			}
		case *network.EventLoadingFinished:
			if _, ok := requests[e.RequestID]; ok {
				if e.EncodedDataLength > maxProductBody {
					fail("SHOPEE_RESPONSE_INVALID")
					return
				}
				select {
				case completed <- e.RequestID:
				default:
					fail("SHOPEE_RESPONSE_INVALID")
				}
			}
		}
	})
	var err error
	values, _ := json.Marshal(customLinkPayloads(link))
	// Use the current app's exported GraphQL function so its client supplies fresh
	// CSRF/SAP headers. Discover by operation text instead of versioned module IDs.
	// The native function reads advancedLinkParams once per mapped URL; a getter
	// supplies that URL's signed IDs. Verify the actual outbound payload below.
	script := `(() => {
  const chunks = self.webpackChunkaffiliate_network;
  if (!chunks) return false;
  let require;
  chunks.push([["hoanxu_custom_link_"+crypto.randomUUID()], {}, r => { require = r; }]);
  if (!require || !require.m) return false;
  for (const [id, factory] of Object.entries(require.m)) {
   if (!factory.toString().includes('batchGetCustomLink')) continue;
   const exports = require(id);
   const send = Object.values(exports).find(fn => typeof fn === 'function' && fn.toString().includes('batchGetCustomLink'));
   if (!send) continue;
   const params = ` + string(values) + `;
   let index = 0;
   window.__hoanxuCustomLinkFailed = false;
   try {
    Promise.resolve(send({links:params.map(p => p.originalLink), get advancedLinkParams() { return params[index++].advancedLinkParams; }})).catch(() => { window.__hoanxuCustomLinkFailed = true; });
   } catch { window.__hoanxuCustomLinkFailed = true; }
   return true;
  }
  return false;
 })()`
	var sent bool
	if err = boundedCommand(scope, 5*time.Second, chromedp.Evaluate(script, &sent)); err != nil || !sent {
		return nil, failure("SHOPEE_RESPONSE_INVALID", "custom_link_client", false)
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-scope.Done():
			return nil, failure("SHOPEE_TIMEOUT", "offer_link", false)
		case f := <-failures:
			return nil, f
		case <-ticker.C:
			var failed bool
			if err := boundedCommand(scope, time.Second, chromedp.Evaluate(`window.__hoanxuCustomLinkFailed === true`, &failed)); err != nil || failed {
				return nil, failure("SHOPEE_UPSTREAM_FAILED", "custom_link_client", false)
			}
		case id := <-completed:
			var post string
			err = boundedCommand(scope, 3*time.Second, chromedp.ActionFunc(func(c context.Context) error { var e error; post, e = network.GetRequestPostData(id).Do(c); return e }))
			if err != nil {
				return nil, failure("SHOPEE_RESPONSE_INVALID", "offer_link_request", false)
			}
			// Only accept the response for this product and its exact tracking.
			if !matchesOfferLinkRequest(post, link) {
				continue
			}
			mu.Lock()
			r := requests[id]
			mu.Unlock()
			switch r.status {
			case 401:
				return nil, failure("SHOPEE_LOGIN_REQUIRED", "offer_link", false)
			case 429:
				return nil, failure("SHOPEE_RATE_LIMITED", "offer_link", false)
			case 200:
				var body []byte
				err = boundedCommand(scope, 3*time.Second, chromedp.ActionFunc(func(c context.Context) error { var e error; body, e = network.GetResponseBody(id).Do(c); return e }))
				if err != nil {
					return nil, failure("SHOPEE_RESPONSE_INVALID", "offer_link_body", false)
				}
				if _, err = parseCustomLinkResponse(body, len(link.Params)); err != nil {
					return nil, err
				}
				return body, nil
			default:
				return nil, failure("SHOPEE_UPSTREAM_FAILED", "offer_link", false)
			}
		}
	}
}

func parseCustomLinkResponse(body []byte, count int) ([]CustomLinkResult, error) {
	var response struct {
		Code   int               `json:"code"`
		Error  int               `json:"error"`
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Links []struct {
				Short string `json:"shortLink"`
				Long  string `json:"longLink"`
				Fail  *int   `json:"failCode"`
			} `json:"batchCustomLink"`
		} `json:"data"`
	}
	bad := failure("SHOPEE_RESPONSE_INVALID", "offer_link_schema", false)
	if count < 1 || count > 5 || len(body) > maxProductBody || json.Unmarshal(body, &response) != nil || response.Code != 0 || response.Error != 0 || len(response.Errors) != 0 || len(response.Data.Links) != count {
		return nil, bad
	}
	results := make([]CustomLinkResult, count)
	for i, link := range response.Data.Links {
		if link.Fail != nil && *link.Fail != 0 {
			code := "SHOPEE_UPSTREAM_FAILED"
			if *link.Fail >= 3 && *link.Fail <= 7 {
				code = "SHOPEE_SUBID_REJECTED"
			}
			results[i].Err = failure(code, "custom_link_rejected", false)
			continue
		}
		u, err := url.Parse(link.Short)
		if err != nil || u.Scheme != "https" || u.Host != "s.shopee.vn" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !shortOfferPath.MatchString(u.Path) || u.Path == "/an_redir" || len(link.Short) > 2048 {
			results[i].Err = bad
			continue
		}
		results[i].URL = link.Short
	}
	return results, nil
}
