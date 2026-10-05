package imports

import (
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Row struct {
	Channel    string    `json:"channel"`
	Publisher  string    `json:"publisher"`
	OrderID    string    `json:"orderId"`
	LineID     string    `json:"lineId"`
	Tracking   string    `json:"trackingCode"`
	Date       time.Time `json:"date"`
	Name       string    `json:"productName"`
	Value      int64     `json:"value"`
	Commission int64     `json:"commission"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
}

var fields = []string{"channel", "publisher", "order_id", "line_id", "tracking_code", "date", "product_name", "value", "commission", "status"}

func Parse(reader io.Reader, mapping map[string]string) ([]Row, error) {
	raw, e := io.ReadAll(io.LimitReader(reader, 10*1024*1024+1))
	if e != nil {
		return nil, e
	}
	if len(raw) > 10*1024*1024 {
		return nil, errors.New("CSV tối đa 10 MB")
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("CSV cần encoding UTF-8")
	}
	text := strings.TrimPrefix(string(raw), "\ufeff")
	r := csv.NewReader(strings.NewReader(text))
	if first := strings.SplitN(text, "\n", 2)[0]; strings.Count(first, ";") > strings.Count(first, ",") {
		r.Comma = ';'
	}
	header, e := r.Read()
	if e != nil {
		return nil, e
	}
	indices := map[string]int{}
	for _, f := range fields {
		col := f
		if mapping[f] != "" {
			col = mapping[f]
		}
		found := false
		for i, h := range header {
			if strings.TrimSpace(h) == col {
				indices[f] = i
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("Thiếu cột " + col)
		}
	}
	out := []Row{}
	for {
		record, e := r.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if len(out) >= 50000 {
			return nil, errors.New("CSV tối đa 50.000 dòng")
		}
		get := func(k string) string { return strings.TrimSpace(record[indices[k]]) }
		row := Row{Channel: get("channel"), Publisher: get("publisher"), OrderID: get("order_id"), LineID: get("line_id"), Tracking: get("tracking_code"), Name: get("product_name"), Status: get("status")}
		row.Value, e = strconv.ParseInt(get("value"), 10, 64)
		if e != nil || row.Value < 0 || row.Value > 1e12 {
			row.Error = "Giá trị đơn phải là số nguyên VND không âm"
		}
		row.Commission, e = strconv.ParseInt(get("commission"), 10, 64)
		if e != nil || row.Commission < 0 || row.Commission > 1e12 {
			row.Error = "Hoa hồng phải là số nguyên VND không âm"
		}
		row.Date, e = time.Parse(time.RFC3339, get("date"))
		if e != nil {
			row.Date, e = time.ParseInLocation("2006-01-02", get("date"), time.FixedZone("ICT", 25200))
		}
		if e != nil {
			row.Error = "Ngày phải là YYYY-MM-DD hoặc RFC3339"
		}
		if row.Status != "pending" && row.Status != "approved" && row.Status != "rejected" {
			row.Error = "Trạng thái phải pending/approved/rejected"
		}
		if row.Channel != "shopee" && row.Channel != "lazada" && row.Channel != "tiktok" && row.Channel != "tiki" {
			row.Error = "Kênh không hợp lệ"
		}
		if row.Publisher == "" || row.OrderID == "" || row.LineID == "" || row.Tracking == "" || row.Name == "" {
			row.Error = "Thiếu mã nguồn, tracking hoặc tên sản phẩm"
		}
		for _, v := range []string{row.Publisher, row.OrderID, row.LineID, row.Tracking, row.Name} {
			if len(v) > 500 {
				row.Error = "Giá trị CSV quá dài"
			}
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, errors.New("CSV không có dữ liệu")
	}
	return out, nil
}
