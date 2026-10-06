package imports

import (
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
