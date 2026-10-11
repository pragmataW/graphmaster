package service

import (
	"errors"

	"example.com/fixture/internal/store"
	"example.com/fixture/internal/util"
)

// ErrOutOfStock is returned when an order exceeds available quantity.
var ErrOutOfStock = errors.New("out of stock")

// OrderService places orders against a Store.
type OrderService struct {
	store store.Store
}

// NewOrderService wires an OrderService to s.
func NewOrderService(s store.Store) *OrderService {
	return &OrderService{store: s}
}

// PlaceOrder reserves qty units of id and returns a receipt line.
func (s *OrderService) PlaceOrder(id string, qty int) (string, error) {
	item, ok := s.store.Get(id)
	if !ok || !validate(item, qty) {
		return "", ErrOutOfStock
	}
	item.Quantity -= qty
	s.store.Put(item)
	return util.FormatID(id) + " " + util.FormatMoney(item.Price*qty), nil
}

// validate is an unexported, same-file helper.
func validate(item store.Item, qty int) bool {
	return qty > 0 && item.Quantity >= qty
}
