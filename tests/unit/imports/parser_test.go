package imports

import (
	"strings"
	"testing"
)

func TestCSVPreview(t *testing.T) {
	raw := "channel,publisher,order_id,line_id,tracking_code,date,product_name,value,commission,status\nshopee,pub,o1,l1,abc,2026-10-05,Test,100000,5000,approved\nshopee,pub,o2,l1,abc,2026-10-05,Test,-1,5000,pending\n"
	rows, e := Parse(strings.NewReader(raw), nil)
	if e != nil || len(rows) != 2 || rows[0].Error != "" || rows[1].Error == "" {
		t.Fatal(rows, e)
	}
}
func TestCSVMappingAndBOM(t *testing.T) {
	raw := "\ufeffkenh,publisher,order_id,line_id,tracking_code,date,product_name,value,commission,status\nshopee,p,o,l,t,2026-10-05,Test,1000,0,pending\n"
	r, e := Parse(strings.NewReader(raw), map[string]string{"channel": "kenh"})
	if e != nil || len(r) != 1 || r[0].Error != "" {
		t.Fatal(r, e)
	}
}
