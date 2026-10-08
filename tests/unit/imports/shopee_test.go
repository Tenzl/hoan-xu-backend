package imports

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const nativeCSV = "Order id,Conversion id,Order Status,Order Time,Shop id,Item id,Model id,Promotion id,Item Name,Purchase Value(₫),Item Total Commission(₫),Affiliate Item Status,Sub_id1,Sub_id2,Sub_id3,Sub_id4,Sub_id5\n260908EGG3Y5PS,242123456789012,Completed,2026-10-06 12:34:56,83496725,6939920023,247123456789,0,Product,31000,1234,Approved,customer,hoanxu,token,0p63,signature\n"

func TestShopeeCSVKeepsExactIDsTimeAndStableSourceKey(t *testing.T) {
	rows, e := Parse(strings.NewReader(nativeCSV), nil)
	if e != nil {
		t.Fatal(e)
	}
	r := rows[0]
	if r.Error != "" || r.Status != "approved" || r.Tracking != "token" || r.ConversionID != "242123456789012" || r.Commission != 1234 || r.Date.UTC().Format(time.RFC3339) != "2026-10-06T05:34:56Z" {
		t.Fatal(r)
	}
	double, e := Parse(strings.NewReader(nativeCSV+strings.Split(nativeCSV, "\n")[1]+"\n"), nil)
	if e != nil || double[0].LineID != double[1].LineID {
		t.Fatal(double, e)
	}
	for _, bad := range []string{strings.Replace(nativeCSV, "242123456789012", "2.42E+14", 1), strings.Replace(nativeCSV, "2026-10-06 12:34:56", "########", 1), strings.Replace(nativeCSV, "2026-10-06 12:34:56", "2026-10-06", 1)} {
		rows, e = Parse(strings.NewReader(bad), nil)
		if e == nil && rows[0].Error == "" {
			t.Fatal("accepted damaged report")
		}
	}
}

func TestShopeeActualExportShape(t *testing.T) {
	file, err := os.Open("../../tests/fixtures/imports/shopee-report.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := Parse(file, nil)
	if err != nil || len(rows) != 3 {
		t.Fatal("expected three report rows", err)
	}
	for _, row := range rows {
		if row.Error != "" {
			t.Fatal(row.Error)
		}
	}
	if rows[0].Status != "pending" || rows[0].ReportChannel != "Zalo" || rows[0].Commission != 20872 || rows[0].ReportedCommission != "20872.44" {
		t.Fatal("pending source values changed")
	}
	if rows[1].Status != "rejected" || rows[2].Status != "rejected" || rows[1].ReportChannel != "Code Sharing" || rows[1].LineID == rows[2].LineID {
		t.Fatal("cancelled items merged or mislabeled")
	}
}

func TestShopeeReportLimits(t *testing.T) {
	if _, err := Parse(strings.NewReader(strings.Repeat("x", 10*1024*1024+1)), nil); err == nil {
		t.Fatal("accepted oversized file")
	}
	header := strings.SplitN(nativeCSV, "\n", 2)[0] + "\n"
	row := "A,1,Pending,2026-10-06 12:34:56,1,1,1,,P,0,0,Pending,,,,,\n"
	rows, err := Parse(strings.NewReader(header+strings.Repeat(row, 50000)), nil)
	if err != nil || len(rows) != 50000 || rows[0].Error != "" {
		t.Fatal("50,000-row boundary", err)
	}
	if _, err := Parse(strings.NewReader(header+strings.Repeat(row, 50001)), nil); err == nil {
		t.Fatal("accepted more than 50,000 rows")
	}
}

func TestShopeeReportDecimalsEmptyPromotionAndMetadata(t *testing.T) {
	csv := strings.Replace(nativeCSV, "Sub_id5\n", "Sub_id5,Channel\n", 1)
	csv = strings.Replace(csv, "signature\n", "signature,Zalo\n", 1)
	csv = strings.Replace(csv, ",0,Product,31000,1234,", ",,\"Product, quoted\",463832,20872.44,", 1)
	rows, err := Parse(strings.NewReader("\ufeff"+csv), nil)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	r := rows[0]
	if r.Error != "" || r.Commission != 20872 || r.Value != 463832 {
		t.Fatal(r)
	}
	data, _ := json.Marshal(r)
	var payload map[string]any
	_ = json.Unmarshal(data, &payload)
	for field, want := range map[string]string{"reportChannel": "Zalo", "shopeeOrderStatus": "Completed", "affiliateItemStatus": "Approved", "reportedValue": "463832", "reportedCommission": "20872.44"} {
		if payload[field] != want {
			t.Fatalf("%s: got %v, want %s", field, payload[field], want)
		}
	}
	zero, err := Parse(strings.NewReader(strings.Replace(csv, ",,\"Product", ",0,\"Product", 1)), nil)
	if err != nil || zero[0].LineID != r.LineID {
		t.Fatal("empty promotion must match zero", zero, err)
	}
}

func TestShopeeMoneyAndStatusValidation(t *testing.T) {
	for _, amount := range []string{"-1", "1e3", "NaN", "1,000", "1000000000000.01", "20872.", ".44"} {
		t.Run(amount, func(t *testing.T) {
			rows, err := Parse(strings.NewReader(strings.Replace(nativeCSV, ",1234,", ","+amount+",", 1)), nil)
			if err == nil && rows[0].Error == "" {
				t.Fatal("accepted invalid money", amount)
			}
		})
	}
	for _, test := range []struct {
		order, item, status string
		invalid             bool
	}{
		{"Pending", "Pending", "pending", false}, {"Completed", "Pending", "pending", false},
		{"Completed", "Completed", "approved", false}, {"Completed", "Validated", "approved", false},
		{"Completed", "Cancelled", "rejected", false}, {"Cancelled", "Cancelled", "rejected", false},
		{"Completed", "Mystery", "", true}, {"Mystery", "Approved", "", true},
	} {
		t.Run(test.order+"/"+test.item, func(t *testing.T) {
			csv := strings.Replace(strings.Replace(nativeCSV, ",Completed,", ","+test.order+",", 1), ",Approved,", ","+test.item+",", 1)
			rows, err := Parse(strings.NewReader(csv), nil)
			if err != nil {
				t.Fatal(err)
			}
			if (rows[0].Error != "") != test.invalid || rows[0].Status != test.status {
				t.Fatal(rows[0])
			}
		})
	}
}
