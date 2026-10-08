package imports

import (
	"encoding/csv"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var exactID = regexp.MustCompile(`^[0-9]+$`)
var orderID = regexp.MustCompile(`^[A-Za-z0-9]+$`)

var reportMoney = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

// Floor decimal VND without passing through binary floating point.
func parseReportMoney(raw string) (int64, error) {
	if len(raw) > 64 || !reportMoney.MatchString(raw) {
		return 0, errors.New("Số tiền không hợp lệ")
	}
	parts := strings.SplitN(raw, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > 1e12 || (whole == 1e12 && len(parts) == 2 && strings.Trim(parts[1], "0") != "") {
		return 0, errors.New("Số tiền vượt giới hạn")
	}
	return whole, nil
}

func shopeeStatus(orderRaw, itemRaw string) (string, error) {
	order, item := strings.ToLower(strings.TrimSpace(orderRaw)), strings.ToLower(strings.TrimSpace(itemRaw))
	knownOrder := map[string]bool{"pending": true, "unpaid": true, "processing": true, "ongoing": true, "completed": true, "complete": true, "cancelled": true, "canceled": true}
	knownItem := map[string]bool{"pending": true, "unpaid": true, "processing": true, "ongoing": true, "approved": true, "validated": true, "completed": true, "complete": true, "cancelled": true, "canceled": true, "rejected": true, "invalid": true}
	if !knownOrder[order] || !knownItem[item] {
		return "", errors.New("Trạng thái Shopee chưa được nhận diện")
	}
	if order == "cancelled" || order == "canceled" || item == "cancelled" || item == "canceled" || item == "rejected" || item == "invalid" {
		return "rejected", nil
	}
	if (order == "completed" || order == "complete") && (item == "approved" || item == "validated" || item == "completed" || item == "complete") {
		return "approved", nil
	}
	return "pending", nil
}

func parseShopee(r *csv.Reader, header []string) ([]Row, error) {
	columns := map[string]int{}
	for i, h := range header {
		h = strings.TrimSpace(h)
		if _, exists := columns[h]; exists {
			return nil, errors.New("Cột CSV bị trùng: " + h)
		}
		columns[h] = i
	}
	required := []string{"Order id", "Conversion id", "Order Status", "Order Time", "Shop id", "Item id", "Model id", "Promotion id", "Item Name", "Purchase Value(₫)", "Item Total Commission(₫)", "Affiliate Item Status", "Sub_id1", "Sub_id2", "Sub_id3", "Sub_id4", "Sub_id5"}
	for _, h := range required {
		if _, ok := columns[h]; !ok {
			return nil, errors.New("Thiếu cột Shopee " + h)
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
		get := func(k string) string {
			if index, ok := columns[k]; ok {
				return strings.TrimSpace(record[index])
			}
			return ""
		}
		row := Row{NativeShopee: true, Channel: "shopee", OrderID: get("Order id"), ConversionID: get("Conversion id"), ShopID: get("Shop id"), ItemID: get("Item id"), ModelID: get("Model id"), PromotionID: get("Promotion id"), Name: get("Item Name")}
		if row.PromotionID == "" {
			row.PromotionID = "0"
		}
		row.ReportMetadata = ReportMetadata{ReportChannel: get("Channel"), ShopeeOrderStatus: get("Order Status"), AffiliateItemStatus: get("Affiliate Item Status"), ReportedValue: get("Purchase Value(₫)"), ReportedCommission: get("Item Total Commission(₫)")}
		if len(row.ReportChannel) > 128 || len(row.ShopeeOrderStatus) > 64 || len(row.AffiliateItemStatus) > 64 {
			row.Error = "Thông tin nguồn Shopee quá dài"
		}
		for i := range row.SubIDs {
			row.SubIDs[i] = get("Sub_id" + strconv.Itoa(i+1))
			if len(row.SubIDs[i]) > 50 {
				row.Error = "Sub_id vượt quá 50 ký tự"
			}
		}
		row.Tracking = row.SubIDs[2]
		if len(row.OrderID) > 100 || !orderID.MatchString(row.OrderID) {
			row.Error = "Order id không hợp lệ"
		}
		for _, id := range []string{row.ConversionID, row.ShopID, row.ItemID, row.ModelID, row.PromotionID} {
			if len(id) > 100 || !exactID.MatchString(id) {
				row.Error = "ID Shopee cần số đầy đủ; không nhận ######## hoặc dạng khoa học"
			}
		}
		row.LineID, _ = SourceLineID(row)
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "02/01/2006 15:04:05", "2006-01-02 15:04", "02/01/2006 15:04"} {
			row.Date, e = time.ParseInLocation(layout, get("Order Time"), time.FixedZone("Asia/Ho_Chi_Minh", 25200))
			if e == nil {
				break
			}
		}
		if e != nil {
			row.Error = "Order Time phải có ngày và giờ đầy đủ (giờ Việt Nam); không nhận ########"
		}
		money := func(k string) int64 {
			v, err := parseReportMoney(get(k))
			if err != nil {
				row.Error = "Cột " + k + " cần số VND không âm, tối đa 1.000.000.000.000"
			}
			return v
		}
		row.Value = money("Purchase Value(₫)")
		row.Commission = money("Item Total Commission(₫)")
		row.Status, e = shopeeStatus(row.ShopeeOrderStatus, row.AffiliateItemStatus)
		if e != nil {
			row.Error = e.Error()
		}
		if row.Name == "" || len(row.Name) > 500 {
			row.Error = "Tên sản phẩm thiếu hoặc quá dài"
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, errors.New("CSV không có dữ liệu")
	}
	return out, nil
}
