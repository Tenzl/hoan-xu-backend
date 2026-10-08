package api

import (
	"context"
	"fmt"
	"hoanxu/internal/rewards"
	"testing"
)

func TestGiftImagePositionPersistsAndValidates(t *testing.T) {
	s, _, admin := testStore(t)
	ctx := context.Background()
	svc := &rewards.Service{Store: s}
	created, err := svc.CreateGift(ctx, admin, "position-create", rewards.GiftInput{Name: "Quà có ảnh", CostXu: 1000, Stock: 2, Active: true, ImageURL: "https://example.com/gift.png"})
	if err != nil {
		t.Fatal(err)
	}
	gift := created.(rewards.Gift)
	check := func(want int) {
		t.Helper()
		var got int
		if err := s.Pool.QueryRow(ctx, `SELECT image_position_y FROM gift_catalog WHERE id=$1`, gift.ID).Scan(&got); err != nil || got != want {
			t.Fatalf("position = %d, want %d: %v", got, want, err)
		}
	}
	check(50)
	for _, position := range []int{0, 83, 100} {
		result, err := svc.UpdateGift(ctx, admin, gift.ID, fmt.Sprintf("position-update-%d", position), rewards.GiftPatch{ImagePositionY: &position})
		if err != nil {
			t.Fatal(err)
		}
		if value := result.(rewards.Gift).ImagePositionY; value == nil || *value != position {
			t.Fatal(result)
		}
		check(position)
	}
	price := int64(2000)
	if _, err = svc.UpdateGift(ctx, admin, gift.ID, "position-price", rewards.GiftPatch{CostXu: &price}); err != nil {
		t.Fatal(err)
	}
	check(100)
	for _, position := range []int{-1, 101} {
		if _, err = svc.UpdateGift(ctx, admin, gift.ID, fmt.Sprintf("position-invalid-%d", position), rewards.GiftPatch{ImagePositionY: &position}); err == nil {
			t.Fatal("accepted invalid position", position)
		}
		if _, err = svc.CreateGift(ctx, admin, fmt.Sprintf("position-create-invalid-%d", position), rewards.GiftInput{Name: "Quà", CostXu: 1000, Stock: 1, ImagePositionY: &position}); err == nil {
			t.Fatal("created invalid position", position)
		}
		check(100)
	}
	position := 0
	created, err = svc.CreateGift(ctx, admin, "position-create-top", rewards.GiftInput{Name: "Quà phía trên", CostXu: 1000, Stock: 1, ImagePositionY: &position})
	if err != nil {
		t.Fatal(err)
	}
	gift = created.(rewards.Gift)
	check(0)
}
