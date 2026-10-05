# Shopee Affiliate Commission Checker
## Kế hoạch triển khai bằng Go + chromedp + Chromium + Render

> Mục tiêu: xây dựng backend nhận link Shopee, tự xác định `item_id`, dùng Chromium headless chạy Shopee Affiliate web, bắt response chứa hoa hồng và trả về dữ liệu chuẩn hóa cho frontend.

## 1. Mục tiêu cuối cùng

### Input

```json
{
  "url": "https://s.shopee.vn/4fwjJHTVQv"
}
```

### Output mong muốn

```json
{
  "success": true,
  "data": {
    "itemId": "50601389541",
    "shopId": "52797090",
    "productName": "Tên sản phẩm",
    "price": 579000,
    "commissionRate": 9.5,
    "commission": 55005,
    "sellerCommissionRate": 7,
    "sellerCommission": 40530,
    "shopeeCommissionRate": 2.5,
    "shopeeCommission": 14475,
    "commissionCap": 40000,
    "productLink": "https://shopee.vn/product/...",
    "affiliateLink": "https://shopee.vn/universal-link/..."
  }
}
```

### Flow tổng quát

```text
User paste link
      ↓
Go API
      ↓
Resolve shortlink
      ↓
Extract item_id
      ↓
Check cache
      ↓
chromedp worker
      ↓
Chromium mở Shopee Affiliate
      ↓
Shopee frontend tự gọi API product
      ↓
chromedp bắt response
      ↓
Normalize dữ liệu
      ↓
Cache
      ↓
Return JSON
```

## 2. Kiến trúc tổng thể

```text
                         Internet
                            │
                            ▼
                 ┌─────────────────────┐
                 │ Frontend / Next.js  │
                 │ Vercel              │
                 └──────────┬──────────┘
                            │
                     HTTPS REST API
                            │
                            ▼
        ┌────────────────────────────────────┐
        │ Render Web Service                 │
        │                                    │
        │ Go API                             │
        │ ├── HTTP Router                    │
        │ ├── Link Resolver                  │
        │ ├── Shopee Service                 │
        │ ├── Cache                          │
        │ ├── Worker Pool                    │
        │ └── Browser Manager                │
        │          │                         │
        │          ▼                         │
        │      chromedp                      │
        │          │                         │
        │          ▼                         │
        │ Chromium Headless                  │
        └──────────┬─────────────────────────┘
                   │
                   ▼
          affiliate.shopee.vn
```

## 3. Stack đề xuất

| Thành phần | Công nghệ |
|---|---|
| Backend | Go |
| HTTP Router | `chi` hoặc `gin` |
| Browser Automation | `chromedp` |
| Browser | Chromium |
| Cache MVP | In-memory |
| Cache Production | Redis |
| Database | PostgreSQL nếu cần history/user |
| Deploy Backend | Render Web Service |
| Deploy Frontend | Vercel |
| Container | Docker |
| Persistent Browser State | Render Persistent Disk |
| Logging | `slog` hoặc `zerolog` |

Đề xuất tối giản:

```text
Go + chi + chromedp + Chromium + slog
```

## 4. Cấu trúc project

```text
shopee-aff-api/
│
├── cmd/
│   └── api/
│       └── main.go
│
├── internal/
│   ├── api/
│   │   ├── router.go
│   │   ├── middleware.go
│   │   └── handler/
│   │       ├── product.go
│   │       └── health.go
│   │
│   ├── browser/
│   │   ├── manager.go
│   │   ├── worker.go
│   │   ├── session.go
│   │   └── interceptor.go
│   │
│   ├── shopee/
│   │   ├── service.go
│   │   ├── resolver.go
│   │   ├── parser.go
│   │   ├── normalizer.go
│   │   └── model.go
│   │
│   ├── cache/
│   │   ├── cache.go
│   │   └── memory.go
│   │
│   └── config/
│       └── config.go
│
├── testdata/
│   ├── normal-product.json
│   ├── zero-commission.json
│   ├── capped-commission.json
│   └── missing-product.json
│
├── Dockerfile
├── go.mod
├── go.sum
├── .env.example
└── README.md
```

## 5. Public API

### 5.1 Check product

```http
POST /api/v1/shopee/check
Content-Type: application/json
```

Body:

```json
{
  "url": "https://s.shopee.vn/4fwjJHTVQv"
}
```

Response:

```json
{
  "success": true,
  "data": {
    "itemId": "50601389541",
    "shopId": "52797090",
    "productName": "Tên sản phẩm",
    "price": 579000,
    "commissionRate": 9.5,
    "commission": 55005,
    "sellerCommissionRate": 7,
    "sellerCommission": 40530,
    "shopeeCommissionRate": 2.5,
    "shopeeCommission": 14475,
    "commissionCap": 40000,
    "affiliateLink": "https://shopee.vn/universal-link/..."
  }
}
```

### 5.2 Health

```http
GET /healthz
```

```json
{
  "status": "ok"
}
```

### 5.3 Readiness

```http
GET /readyz
```

```json
{
  "status": "ready",
  "browser": true,
  "shopeeAuthenticated": true,
  "queue": {
    "workers": 2,
    "pending": 0
  }
}
```

## 6. Validate URL và chống SSRF

Chỉ allow:

```text
shopee.vn
www.shopee.vn
s.shopee.vn
affiliate.shopee.vn
```

Reject:

```text
localhost
127.0.0.1
0.0.0.0
169.254.x.x
10.x.x.x
172.16.x.x - 172.31.x.x
192.168.x.x
domain khác
```

Giới hạn độ dài URL khoảng `2048-4096` ký tự.

## 7. Resolve Shopee Link

### Link đã có item ID

Ví dụ:

```text
https://shopee.vn/product/590427230/14916526784
```

Parse trực tiếp:

```text
shopId = 590427230
itemId = 14916526784
```

### Shortlink

Ví dụ:

```text
https://s.shopee.vn/4fwjJHTVQv
```

Dùng `http.Client`:

```go
client := &http.Client{
    Timeout: 8 * time.Second,
}
```

Follow redirect rồi parse URL cuối để lấy `shopId` và `itemId`.

### Không dùng Chromium để resolve shortlink

```text
shortlink
→ Go http.Client
→ itemId
```

Chromium chỉ dùng khi cần check commission.

## 8. Cache trước browser

Key:

```text
product:{itemId}
```

TTL ban đầu:

```text
5-10 phút
```

Flow:

```text
itemId
 ↓
cache hit?
 ├── YES → return
 └── NO
       ↓
    browser
```

## 9. Browser Manager

Browser Manager chịu trách nhiệm:

```text
start Chromium
monitor Chromium
restart Chromium nếu crash
create tab
close tab
check session
```

Không launch Chromium mỗi request. Chromium nên được launch một lần khi server start và reuse lâu dài.

## 10. Chromium Persistent Profile

Production:

```text
CHROME_USER_DATA_DIR=/var/data/chrome-profile
```

Profile giữ:

```text
cookies
Local Storage
IndexedDB
Shopee login state
browser state
```

Trên Render, mount Persistent Disk ở:

```text
/var/data
```

## 11. Shopee Login Strategy

Không hard-code username/password trong source.

Không tự bypass CAPTCHA, 2FA hoặc xác minh.

Flow hợp lý:

```text
Chromium headful local
→ login Shopee Affiliate thủ công
→ lưu profile
→ dùng profile đó cho automation
```

Production kiểm tra session định kỳ. Nếu session hết hạn thì đánh dấu `SHOPEE_LOGIN_REQUIRED` thay vì spam request.

## 12. Session Monitoring

Mỗi 5-15 phút, browser mở:

```text
https://affiliate.shopee.vn/dashboard
```

Nếu dashboard load được:

```text
authenticated=true
```

Nếu redirect login:

```text
authenticated=false
```

## 13. Worker Pool

MVP:

```text
MAX_WORKERS=2
QUEUE_SIZE=50
```

Flow:

```text
HTTP request
    ↓
queue
    ↓
Worker 1 ─→ Chromium tab
Worker 2 ─→ Chromium tab
```

Model:

```go
type Job struct {
    ItemID string
    Result chan Result
}
```

## 14. Browser Context per Job

Mỗi job:

```text
new chromedp context
 ↓
new target/tab
 ↓
navigate
 ↓
capture response
 ↓
close context
```

Browser process vẫn sống.

## 15. Capture Shopee Product Response

Trang:

```text
https://affiliate.shopee.vn/offer/product_offer/{itemId}
```

Frontend Shopee gọi:

```text
GET /api/v3/offer/product?item_id={itemId}
```

Register listener trước khi navigate:

```go
chromedp.ListenTarget(ctx, func(ev interface{}) {
    e, ok := ev.(*network.EventResponseReceived)
    if !ok {
        return
    }

    if strings.Contains(
        e.Response.URL,
        "/api/v3/offer/product?item_id="+itemID,
    ) {
        // save RequestID
    }
})
```

Sau đó:

```go
chromedp.Run(ctx,
    network.Enable(),
    chromedp.Navigate(
        "https://affiliate.shopee.vn/offer/product_offer/"+itemID,
    ),
)
```

Dùng `network.GetResponseBody(requestID)` để đọc JSON.

## 16. Không gọi trực tiếp internal API

Không làm:

```text
Go
→ /api/v3/offer/product
→ fake x-sap-sec
→ fake af-ac-*
```

Mà làm:

```text
Go
→ Chromium
→ Shopee frontend
→ Shopee API
```

Browser thật tự xử lý cookies và security context.

## 17. Model Response

Chỉ map field cần thiết:

```go
type ShopeeProductResponse struct {
    Code int    `json:"code"`
    Msg  string `json:"msg"`

    Data struct {
        ItemID      string `json:"item_id"`
        Commission  string `json:"commission"`
        LongLink    string `json:"long_link"`
        ProductLink string `json:"product_link"`

        CommissionRate struct {
            MaxCommissionRate    string `json:"max_commission_rate"`
            SellerCommissionRate string `json:"seller_commission_rate"`
            ShopeeCommissionRate string `json:"shopee_commission_rate"`
            SellerCommission     string `json:"seller_commission"`
            ShopeeCommission     string `json:"shopee_commission"`
            CommissionCap        string `json:"commission_cap"`
        } `json:"commission_rate"`

        Product struct {
            ItemID   string `json:"itemid"`
            ShopID   string `json:"shopid"`
            Name     string `json:"name"`
            Price    string `json:"price"`
            Image    string `json:"image"`
            ShopName string `json:"shop_name"`
        } `json:"batch_item_for_item_card_full"`
    } `json:"data"`
}
```

## 18. Normalize Response

Shopee có thể trả:

```text
"₫55.005"
"9,5%"
"57900000000"
```

Backend normalize thành:

```json
{
  "commission": 55005,
  "commissionRate": 9.5,
  "price": 579000
}
```

Tạo helpers riêng:

```text
parseVND()
parsePercent()
parseShopeePrice()
```

## 19. Price Conversion

Không hard-code scale ngay. Test tối thiểu 10-20 sản phẩm để xác nhận cách Shopee scale price nhất quán.

## 20. Affiliate Link

Response có `long_link`. Đây là affiliate link backend nên trả cho frontend. Chưa cần shortlink ở MVP.

## 21. Request Deduplication

Dùng `singleflight` để 20 request cùng một item chỉ tạo 1 browser job:

```text
20 requests
   ↓
same item
   ↓
singleflight
   ↓
1 browser job
   ↓
20 responses
```

Package:

```text
golang.org/x/sync/singleflight
```

## 22. Rate Limiting

Gợi ý ban đầu:

```text
10 requests / phút / IP
```

Nếu có account user:

```text
30-60 requests / phút / user
```

## 23. Timeout

```text
Resolve redirect: 5-8s
Queue wait:       10s
Browser task:     15-20s
Total:            ~25s
```

Nếu timeout:

```json
{
  "success": false,
  "error": {
    "code": "SHOPEE_TIMEOUT"
  }
}
```

## 24. Retry

Retry tối đa 1 lần cho lỗi transient:

```text
navigation timeout
browser target crash
temporary network error
```

Không retry:

```text
invalid URL
session expired
product not found
```

## 25. Error Codes

```text
INVALID_URL
UNSUPPORTED_DOMAIN
PRODUCT_ID_NOT_FOUND
QUEUE_FULL
BROWSER_UNAVAILABLE
SHOPEE_TIMEOUT
SHOPEE_LOGIN_REQUIRED
SHOPEE_PRODUCT_NOT_FOUND
SHOPEE_RESPONSE_INVALID
INTERNAL_ERROR
```

## 26. Chromium Auto Recovery

Browser Manager detect:

```text
browser disconnected
Chrome crashed
context canceled
```

Sau đó:

```text
restart browser
→ session check
→ mark ready
```

## 27. Block Resource để giảm RAM/Bandwidth

Có thể block:

```text
font
media
analytics
ads
```

Có thể test block `image`.

Không block:

```text
document
script
xhr
fetch
```

## 28. Logging

Structured log:

```json
{
  "level": "INFO",
  "itemId": "14916526784",
  "latencyMs": 1321,
  "cache": false,
  "worker": 1,
  "event": "product_checked"
}
```

Không log:

```text
cookies
localStorage
x-sap-*
af-ac-*
browser profile
session secrets
```

## 29. Metrics

Theo dõi:

```text
HTTP requests/min
success rate
cache hit %
queue length
browser jobs/min
browser latency
Shopee timeout %
Shopee auth state
browser restart count
RAM
CPU
```

## 30. Database

MVP chưa cần database.

Nếu cần history, thêm PostgreSQL.

### users

```text
id
email
created_at
```

### product_checks

```text
id
user_id
item_id
shop_id
original_url
product_name
price
commission_rate
commission
affiliate_link
created_at
```

Không lưu raw Shopee cookies trong DB.

## 31. Redis

Production có thể dùng Redis cho:

```text
product cache
rate limit
distributed lock
queue metadata
```

MVP có thể dùng memory cache.

## 32. Docker

Multi-stage build:

```dockerfile
FROM golang:1.24 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api ./cmd/api

FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y \
      chromium \
      ca-certificates \
      fonts-liberation \
      && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/api /app/api
WORKDIR /app
CMD ["/app/api"]
```

## 33. Environment Variables

```env
PORT=10000

CHROME_PATH=/usr/bin/chromium
CHROME_USER_DATA_DIR=/var/data/chrome-profile

BROWSER_WORKERS=2
QUEUE_SIZE=50

CACHE_TTL_SECONDS=600

RESOLVE_TIMEOUT_SECONDS=8
NAVIGATION_TIMEOUT_SECONDS=20

RATE_LIMIT_PER_MINUTE=10
LOG_LEVEL=info
```

## 34. Render Web Service

```text
Render
→ New
→ Web Service
→ Connect GitHub
→ Docker
```

Go server bind:

```go
port := os.Getenv("PORT")

srv := &http.Server{
    Addr: "0.0.0.0:" + port,
}
```

## 35. Render Persistent Disk

Production:

```text
Mount path: /var/data
```

Browser profile:

```text
/var/data/chrome-profile
```

Không dùng filesystem làm database.

## 36. Frontend

Frontend chỉ gọi:

```http
POST /api/v1/shopee/check
```

UI tối giản:

```text
┌────────────────────────────────┐
│ Paste Shopee link              │
│                                │
│ https://s.shopee.vn/...        │
│                                │
│       [ Check Commission ]     │
└────────────────────────────────┘
```

Result:

```text
Tên sản phẩm
Giá: 579.000đ
Hoa hồng: 9,5%
Dự kiến: 55.005đ
Seller: 7% / 40.530đ
Shopee: 2,5% / 14.475đ

[ Copy Affiliate Link ]
```

## 37. Security phía frontend/backend

Không expose:

```text
Shopee cookies
browser session
x-sap-*
af-ac-*
```

CORS production chỉ allow domain frontend của bạn.

Admin endpoint phải có authentication.

## 38. Session Expired Handling

Nếu session hết hạn:

```text
browser detects login page
 ↓
mark SESSION_EXPIRED
 ↓
stop browser jobs
 ↓
return SHOPEE_LOGIN_REQUIRED
```

## 39. Production Concurrency

MVP:

```text
1 Chromium
2 workers
50 queue
```

Không autoscale ngay vì browser session là stateful.

## 40. Scale Sau Này

Khi traffic lớn:

```text
Frontend
   ↓
Go API
   ↓
Redis Queue
   ↓
Browser Worker Service
   ↓
Chromium
```

Tách thành API service và Browser Worker service.

## 41. Local Development

Development:

```text
headless=false
```

Production:

```text
headless=true
```

Có thể dùng Docker Compose cho API + Redis + PostgreSQL nếu cần.

## 42. Test Plan

### Unit tests

```text
URL validation
shortlink parser
item ID extraction
VND parser
percent parser
price parser
response normalization
```

### Integration tests

```text
Shopee page opens
product API captured
response body parsed
```

### Failure tests

```text
invalid URL
expired session
invalid item
browser crash
network timeout
queue full
```

## 43. Fixtures

```text
testdata/
├── normal-product.json
├── zero-commission.json
├── capped-commission.json
└── missing-product.json
```

## 44. Load Test

Test:

```text
1 concurrent
5 concurrent
10 concurrent
```

Theo dõi:

```text
RAM
CPU
latency
queue
browser stability
```

## 45. Session Persistence Test

```text
login
 ↓
check product
 ↓
restart container
 ↓
check product again
```

Nếu vẫn logged in thì PASS.

## 46. Development Phases

### Phase A — Local Proof of Concept

```text
Go server
chromedp
manual Shopee login
capture product API
```

### Phase B — URL Resolver

```text
shortlink → itemId
```

### Phase C — Normalize Response

```text
name
price
commission
rate
affiliateLink
```

### Phase D — Cache + Queue

```text
worker pool
singleflight
cache
timeout
retry
```

### Phase E — Docker

```text
docker build
docker run
```

### Phase F — Render Staging

Deploy Render Web Service.

### Phase G — Session Persistence

Gắn Persistent Disk.

### Phase H — Frontend

Next.js/Vercel.

### Phase I — Production Hardening

```text
rate limit
logging
metrics
admin health
CORS
SSRF protection
```

## 47. Acceptance Criteria MVP

```text
✔ paste s.shopee.vn
✔ tự resolve itemId
✔ mở Shopee Affiliate bằng Chromium
✔ bắt đúng /api/v3/offer/product
✔ parse JSON
✔ trả tên sản phẩm
✔ trả giá
✔ trả % commission
✔ trả commission
✔ trả seller commission
✔ trả Shopee commission
✔ trả affiliate link
✔ cache hoạt động
✔ worker pool hoạt động
✔ browser crash có restart
✔ session status được monitor
```

## 48. Rủi ro kỹ thuật

### Shopee đổi API/frontend

Giải pháp: tách network interceptor thành module riêng để sửa nhanh.

### Session hết hạn

Giải pháp: session monitor + admin alert + manual re-login.

### Chromium ngốn RAM

Giải pháp: worker cap + cache + singleflight + resource blocking.

### Render restart

Giải pháp: Persistent Disk cho browser profile.

### Internal endpoint không ổn định

Đây không phải public API contract. Nếu sau này account được cấp Shopee Open API chính thức, nên chuyển dần sang API chính thức.

## 49. Security Notes

Không nên:

```text
hard-code cookie
hard-code x-sap-sec
hard-code af-ac-*
reverse-engineer security token
log session data
expose browser profile ra frontend
```

Nên:

```text
để browser thật chạy Shopee frontend
giữ profile ở backend
normalize response
rate limit
SSRF protection
CORS
secure admin endpoint
```

## 50. Thứ tự code đề xuất

```text
1. Go server
2. chromedp launch Chromium
3. login Shopee Affiliate
4. mở product_offer/{itemId}
5. bắt /api/v3/offer/product
6. parse response
7. POST /check
8. shortlink resolver
9. response normalization
10. cache
11. singleflight
12. worker pool
13. timeout/retry
14. Docker
15. Render
16. persistent profile
17. frontend
18. security/rate limit
19. logs/metrics
```

Nếu bước 5 chưa chạy ổn thì chưa cần làm frontend, database hay Render.

## 51. Proof of Concept Quan Trọng Nhất

```text
Go + chromedp
        ↓
mở product_offer/{itemId}
        ↓
capture:
/api/v3/offer/product
        ↓
fmt.Println(response)
```

Khi phần này chạy ổn local, phần rủi ro kỹ thuật lớn nhất của project đã được giải quyết.

## 52. Kiến trúc Chốt

### MVP

```text
Next.js
   ↓
Go API
   ↓
chi
   ↓
Link Resolver
   ↓
Memory Cache
   ↓
Worker Pool
   ↓
chromedp
   ↓
Chromium
   ↓
Shopee Affiliate
```

### Production

```text
Vercel
   ↓
Render Web Service
   │
   ├── Go API
   ├── Worker Pool
   ├── chromedp
   ├── Chromium
   │
   └── /var/data/chrome-profile
              │
              ▼
       Persistent Disk

+ Redis
+ PostgreSQL optional
```

## 53. Recommended Initial Configuration

```env
PORT=10000
CHROME_PATH=/usr/bin/chromium
CHROME_USER_DATA_DIR=/var/data/chrome-profile
BROWSER_WORKERS=2
QUEUE_SIZE=50
CACHE_TTL_SECONDS=600
RESOLVE_TIMEOUT_SECONDS=8
NAVIGATION_TIMEOUT_SECONDS=20
RATE_LIMIT_PER_MINUTE=10
LOG_LEVEL=info
```

## 54. Checklist Before Production

```text
[ ] Browser profile persists after restart
[ ] Shopee login survives container restart
[ ] No cookies in logs
[ ] No x-sap-* in logs
[ ] SSRF validation enabled
[ ] Rate limiting enabled
[ ] Queue has max size
[ ] Browser worker count limited
[ ] Health endpoint configured
[ ] Readiness endpoint configured
[ ] Cache enabled
[ ] singleflight enabled
[ ] Browser auto-restart enabled
[ ] CORS restricted
[ ] Admin endpoint protected
[ ] Error codes standardized
[ ] Timeouts configured
[ ] Docker tested locally
[ ] Render staging tested
[ ] Load test completed
```

## 55. Kết luận

Bắt đầu bằng PoC nhỏ nhất:

```text
Go
+ chromedp
+ Chromium local
+ 1 itemId cố định
```

Chỉ khi đã bắt được response:

```text
/api/v3/offer/product?item_id=...
```

thì mới thêm:

```text
shortlink resolver
cache
queue
Docker
Render
frontend
```

Cách này giảm rủi ro và tránh tốn thời gian xây frontend/deployment trước khi browser automation hoạt động ổn định.
