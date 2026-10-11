package service

import (
	"testing"

	"example.com/fixture/internal/store"
)

func TestPlaceOrder(t *testing.T) {
	mem := store.NewMemory()
	mem.Put(store.Item{ID: "a1", Price: 250, Quantity: 3})
	svc := NewOrderService(mem)

	got, err := svc.PlaceOrder("a1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := "A1 5.00"; got != want {
		t.Fatalf("PlaceOrder = %q, want %q", got, want)
	}
}

func TestPlaceOrderOutOfStock(t *testing.T) {
	svc := NewOrderService(store.NewMemory())
	if _, err := svc.PlaceOrder("missing", 1); err == nil {
		t.Fatal("expected ErrOutOfStock")
	}
}
