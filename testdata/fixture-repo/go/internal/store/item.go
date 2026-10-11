package store

// Item is a stock-keeping unit held by a Store.
type Item struct {
	ID       string
	Price    int
	Quantity int
}

// Total returns the value of all units of the item.
func (i Item) Total() int {
	return i.Price * i.Quantity
}
