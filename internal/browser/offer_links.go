package browser

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

type offerLinkRequest struct {
	Shop, Item string
	SubIDs     [5]string
}

var offerID = regexp.MustCompile(`^[0-9]+$`)
var shortOfferPath = regexp.MustCompile(`^/[A-Za-z0-9]+$`)

// subId1 is permanent per customer; subId2 labels HoanXu; subId3 identifies the link.
func (m *Manager) CreateOfferLink(ctx context.Context, shop, item string, ids [5]string) (string, error) {
	if !offerID.MatchString(shop) || !offerID.MatchString(item) {
		return "", failure("SHOPEE_RESPONSE_INVALID", "offer_link_input", false)
	}
	for _, id := range ids {
		if id == "" || len(id) > 50 {
			return "", failure("SHOPEE_RESPONSE_INVALID", "offer_link_input", false)
		}
	}
	body, err := m.submit(ctx, item, &offerLinkRequest{Shop: shop, Item: item, SubIDs: ids})
	if err != nil {
		return "", err
	}
	return parseOfferLinkResponse(body, shop, item)
}

func matchesOfferLinkRequest(raw string, link offerLinkRequest) bool {
	var request struct {
		Operation string `json:"operationName"`
		Variables struct {
			Source   string `json:"sourceCaller"`
			Products []struct {
				Item string      `json:"itemId"`
				Shop json.Number `json:"shopId"`
			} `json:"productOfferLinkParams"`
			SubIDs struct {
				Customer string `json:"subId1"`
				Campaign string `json:"subId2"`
				Link     string `json:"subId3"`
				Four     string `json:"subId4"`
				Five     string `json:"subId5"`
			} `json:"advancedLinkParams"`
		} `json:"variables"`
	}
	if len(raw) > maxProductBody || json.Unmarshal([]byte(raw), &request) != nil || request.Operation != "batchGetProductOfferLink" || request.Variables.Source != "WEB_SITE_CALLER" || len(request.Variables.Products) != 1 {
		return false
	}
	p, ids := request.Variables.Products[0], request.Variables.SubIDs
	return p.Item == link.Item && p.Shop.String() == link.Shop && [5]string{ids.Customer, ids.Campaign, ids.Link, ids.Four, ids.Five} == link.SubIDs
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
			if err == nil && u.Host == m.responseHost && u.Path == "/api/v3/gql" && u.Query().Get("q") == "productOfferLinks" && e.Request.Method == "POST" && e.DocumentURL == m.offerBaseURL+link.Item {
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
	values, _ := json.Marshal(link.SubIDs)
	fillSubIDs := chromedp.Tasks{
		chromedp.WaitReady("#getLinkModal_sub_id1", chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
  const values = `+string(values)+`;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
  values.forEach((value, index) => {
    const input = document.querySelector('#getLinkModal_sub_id'+(index+1));
    setter.call(input, value);
    input.dispatchEvent(new Event('input', {bubbles:true}));
    input.dispatchEvent(new Event('change', {bubbles:true}));
  });
})()`, nil),
	}
	for _, step := range []struct {
		phase  string
		action chromedp.Action
	}{
		{"offer_link_ready", chromedp.WaitEnabled(".get-link-btn", chromedp.ByQuery)},
		{"offer_link_open", chromedp.Evaluate(`document.querySelector('.get-link-btn').click()`, nil)},
		{"offer_link_advanced", chromedp.Tasks{chromedp.WaitReady(`.ant-modal-body input[type="radio"][value="1"]`, chromedp.ByQuery), chromedp.Evaluate(`document.querySelector('.ant-modal-body input[type="radio"][value="1"]').click()`, nil)}},
		{"offer_link_subids", fillSubIDs},
		{"offer_link_submit", chromedp.Evaluate(`document.querySelector('.ant-modal-body .add-subid-btn button').click()`, nil)},
		{"offer_link_validation", chromedp.ActionFunc(func(c context.Context) error {
			var rejected bool
			err := chromedp.Run(c, chromedp.Evaluate(`document.querySelector('.ant-modal-body .ant-form-item-has-error input[id^="getLinkModal_sub_id"]') !== null`, &rejected))
			if err != nil {
				return err
			}
			if rejected {
				return failure("SHOPEE_SUBID_REJECTED", "offer_link_validation", false)
			}
			return nil
		})},
	} {
		started := time.Now()
		if err = boundedCommand(scope, 5*time.Second, step.action); err != nil {
			var f *Failure
			if errors.As(err, &f) {
				return nil, f
			}
			return nil, failure("SHOPEE_TIMEOUT", step.phase, false)
		}
		slog.Debug("shopee_offer_link_step", "phase", step.phase, "duration_ms", time.Since(started).Milliseconds())
	}
	for {
		select {
		case <-scope.Done():
			return nil, failure("SHOPEE_TIMEOUT", "offer_link", false)
		case f := <-failures:
			return nil, f
		case id := <-completed:
			var post string
			err = boundedCommand(scope, 3*time.Second, chromedp.ActionFunc(func(c context.Context) error { var e error; post, e = network.GetRequestPostData(id).Do(c); return e }))
			if err != nil {
				return nil, failure("SHOPEE_RESPONSE_INVALID", "offer_link_request", false)
			}
			// Ignore the modal's automatic standard link and any response carrying
			// different product/customer/link IDs; never save its unattributed URL.
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
				if _, err = parseOfferLinkResponse(body, link.Shop, link.Item); err != nil {
					return nil, err
				}
				return body, nil
			default:
				return nil, failure("SHOPEE_UPSTREAM_FAILED", "offer_link", false)
			}
		}
	}
}

func parseOfferLinkResponse(body []byte, shop, item string) (string, error) {
	var response struct {
		Code   int               `json:"code"`
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Links []struct {
				Item string `json:"itemId"`
				Shop string `json:"shopId"`
				URL  string `json:"productOfferLink"`
			} `json:"productOfferLinks"`
		} `json:"data"`
	}
	bad := failure("SHOPEE_RESPONSE_INVALID", "offer_link_schema", false)
	if len(body) > maxProductBody || json.Unmarshal(body, &response) != nil || response.Code != 0 || len(response.Errors) != 0 || len(response.Data.Links) != 1 {
		return "", bad
	}
	l := response.Data.Links[0]
	u, err := url.Parse(l.URL)
	if err != nil || l.Item != item || l.Shop != shop || u.Scheme != "https" || u.Host != "s.shopee.vn" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !shortOfferPath.MatchString(u.Path) || u.Path == "/an_redir" || len(l.URL) > 2048 {
		return "", bad
	}
	return l.URL, nil
}
