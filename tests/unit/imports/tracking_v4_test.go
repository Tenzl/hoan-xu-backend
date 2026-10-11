package imports

import (
	"context"
	"testing"
)

func TestLegacyTrackingCannotUpdateExistingOrders(t *testing.T) {
	row := Row{Channel: "shopee", Tracking: "legacy", OrderID: "old-order", LineID: "old-line"}
	status, err := validateRow(context.Background(), nil, nil, &row)
	if err != nil || status != "ignored" || row.Error == "" {
		t.Fatal(status, err)
	}
}
