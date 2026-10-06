package legacyimport

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const Batch = "legacy-server-2026-10"
const generatorVersion = 1
const maxOrders = 100000

type Summary struct {
	Batch            string `json:"batch"`
	Hash             string `json:"hash"`
	GeneratorVersion int    `json:"generatorVersion"`
	Customers        int    `json:"customers"`
	Orders           int    `json:"orders"`
	Cashback         int64  `json:"cashbackVnd"`
	AlreadyApplied   bool   `json:"alreadyApplied"`
}

type customer struct {
	STT    int
	Name   string
	First  time.Time
	Last   time.Time
	Count  int
	id     string
	orders []order
}

type order struct {
	id, link, user, tracking, external, line string
	at                                       time.Time
	cash                                     int64
}

// Plan is immutable to callers: only validated, deterministic source data can be applied.
type Plan struct {
	customers []customer
	summary   Summary
}

func (p *Plan) Summary() Summary { return p.summary }

// Prepare reads the five-column Markdown export without connecting to a database.
func Prepare(input io.Reader) (*Plan, error) {
	loc, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(io.LimitReader(input, 8*1024*1024+1))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	plan := &Plan{}
	seen := map[int]bool{}
	lineNumber, bytesRead, total := 0, 0, 0
	gotHeader, gotSeparator := false, false
	for scanner.Scan() {
		lineNumber++
		bytesRead += len(scanner.Bytes()) + 1
		if bytesRead > 8*1024*1024 {
			return nil, fmt.Errorf("legacy file exceeds 8 MiB")
		}
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\uFEFF"))
		if line == "" {
			continue
		}
		fail := func() (*Plan, error) { return nil, fmt.Errorf("invalid legacy row at line %d", lineNumber) }
		if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
			return fail()
		}
		cols := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|"), "|")
		if len(cols) != 5 {
			return fail()
		}
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		if !gotHeader {
			if strings.Join(cols, "|") != "STT|Tên hiển thị|Mua lần đầu|Mua gần nhất|Tổng đơn" {
				return fail()
			}
			gotHeader = true
			continue
		}
		if !gotSeparator {
			for _, col := range cols {
				if len(strings.Trim(col, ":")) < 3 || strings.Trim(col, ":-") != "" {
					return fail()
				}
			}
			gotSeparator = true
			continue
		}
		stt, e1 := strconv.Atoi(cols[0])
		first, e2 := time.ParseInLocation("02/01/2006", cols[2], loc)
		last, e3 := time.ParseInLocation("02/01/2006", cols[3], loc)
		count, e4 := strconv.Atoi(cols[4])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || stt < 1 || seen[stt] || cols[1] == "" || len([]rune(cols[1])) > 80 || count < 1 || count > maxOrders-total || first.After(last) || (count == 1 && !first.Equal(last)) {
			return fail()
		}
		seen[stt] = true
		total += count
		plan.customers = append(plan.customers, customer{STT: stt, Name: cols[1], First: first, Last: last, Count: count})
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	if len(plan.customers) == 0 {
		return nil, fmt.Errorf("legacy table has no customer rows")
	}
	sort.Slice(plan.customers, func(i, j int) bool { return plan.customers[i].STT < plan.customers[j].STT })
	// Unexported/generated fields are deliberately excluded from the canonical input.
	canonical, err := json.Marshal(plan.customers)
	if err != nil {
		return nil, err
	}
	seed := sha256.Sum256(canonical)
	rng := rand.New(rand.NewChaCha8(seed))
	plan.summary = Summary{Batch: Batch, Hash: hex.EncodeToString(seed[:]), GeneratorVersion: generatorVersion, Customers: len(plan.customers), Orders: total}
	for i := range plan.customers {
		c := &plan.customers[i]
		c.id = stableID("customer", c.STT, 0)
		first := c.First.Add(time.Duration(rng.Int64N(86400)) * time.Second)
		last := c.Last.Add(time.Duration(rng.Int64N(86400)) * time.Second)
		if last.Before(first) {
			first, last = last, first
		}
		times := make([]time.Time, c.Count)
		times[0] = first
		if c.Count > 1 {
			times[c.Count-1] = last
			span := int64(last.Sub(first)/time.Second) + 1
			for j := 1; j < c.Count-1; j++ {
				times[j] = first.Add(time.Duration(rng.Int64N(span)) * time.Second)
			}
		}
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		for j, at := range times {
			cash := int64(5000) + rng.Int64N(35001)
			c.orders = append(c.orders, order{
				id: stableID("order", c.STT, j+1), link: stableID("link", c.STT, j+1), user: c.id,
				tracking: "legacy_" + strings.ReplaceAll(stableID("tracking", c.STT, j+1), "-", ""),
				external: fmt.Sprintf("%s:%d:%d", Batch, c.STT, j+1), line: strconv.Itoa(j + 1), at: at.UTC(), cash: cash,
			})
			plan.summary.Cashback += cash
		}
	}
	return plan, nil
}

func stableID(kind string, stt, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%d", Batch, kind, stt, index)))
	// RFC 9562 version 8 UUID for a private, deterministic SHA-256 namespace.
	h[6] = (h[6] & 0x0f) | 0x80
	h[8] = (h[8] & 0x3f) | 0x80
	s := hex.EncodeToString(h[:16])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
