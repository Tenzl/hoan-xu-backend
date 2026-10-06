package legacyimport

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const header = "| STT | Tên hiển thị | Mua lần đầu | Mua gần nhất | Tổng đơn |\n| ---: | --- | --- | --- | ---: |\n"

func TestLegacyPlanPreservesDatesAndProducesRepeatableDraws(t *testing.T) {
	input := header + "| 1 | Một đơn | 27/06/2026 | 27/06/2026 | 1 |\n| 2 | Nhiều đơn | 31/01/2026 | 01/02/2026 | 71 |\n| 3 | Cùng ngày | 02/02/2026 | 02/02/2026 | 5 |\n"
	a, err := Prepare(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Prepare(strings.NewReader("\uFEFF" + strings.ReplaceAll(input, "\n", "\r\n")))
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("formatting changed the draw", err)
	}
	if a.Summary().Customers != 3 || a.Summary().Orders != 77 {
		t.Fatal(a.Summary())
	}
	for _, c := range a.customers {
		if len(c.orders) != c.Count {
			t.Fatal("incorrect count", c.Name)
		}
		if c.orders[0].at.In(c.First.Location()).Format("02/01/2006") != c.First.Format("02/01/2006") || c.orders[len(c.orders)-1].at.In(c.Last.Location()).Format("02/01/2006") != c.Last.Format("02/01/2006") {
			t.Fatal("lost endpoints", c.Name)
		}
		for i, o := range c.orders {
			if o.cash < 5000 || o.cash > 40000 || o.at.Before(c.First) || !o.at.Before(c.Last.AddDate(0, 0, 1)) {
				t.Fatal("invalid generated order", o)
			}
			if i > 0 && o.at.Before(c.orders[i-1].at) {
				t.Fatal("unordered timestamps")
			}
		}
	}
	// Row order and padding are not identity or randomness inputs.
	reordered := header + "|3|Cùng ngày|02/02/2026|02/02/2026|5|\n|2|Nhiều đơn|31/01/2026|01/02/2026|71|\n|1|Một đơn|27/06/2026|27/06/2026|1|\n"
	b, err = Prepare(strings.NewReader(reordered))
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("row order changed the draw", err)
	}
}

func TestLegacyPlanRejectsInvalidInput(t *testing.T) {
	for _, row := range []string{
		"|1|A|31/02/2026|01/03/2026|2|", "|1|A|02/03/2026|01/03/2026|2|",
		"|1|A|01/03/2026|02/03/2026|1|", "|1|A|01/03/2026|01/03/2026|0|",
		"|1||01/03/2026|01/03/2026|1|", "|0|A|01/03/2026|01/03/2026|1|",
		"|1|A|01/03/2026|01/03/2026|1|\n|1|B|01/03/2026|01/03/2026|1|",
		"|1|A|01/03/2026|01/03/2026|100001|", "not a table", "",
	} {
		t.Run(fmt.Sprintf("%q", row), func(t *testing.T) {
			if _, err := Prepare(strings.NewReader(header + row)); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}
