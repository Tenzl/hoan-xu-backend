package api

import (
	"context"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/imports"
	"testing"
	"time"
)

func TestUnexpiredReportedLinkRemainsReusableWhenAnOrderIsRejected(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	id, row := savedFixture(t, s, user, time.Now().UTC().Truncate(time.Second).Add(-24*time.Hour))
	importRows(t, s, admin, []imports.Row{row})
	// Returning an existing link should work without contacting Shopee again.
	svc := &affiliate.Service{Store: s}
	for _, status := range []string{"pending", "rejected"} {
		row.Status = status
		if status == "rejected" {
			importRows(t, s, admin, []imports.Row{row})
		}
		v, err := svc.CreateLink(ctx, user, "https://shopee.vn/tai-nghe-i.83496725.6939920023?variant=4")
		if err != nil {
			t.Fatal(err)
		}
		link := v.(map[string]any)
		if link["id"] != id || link["reused"] != true || link["status"] == "cancelled" {
			t.Fatal("a reported order made a valid reused link unavailable", link)
		}
	}
}
