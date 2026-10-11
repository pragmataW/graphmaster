// Command server is the fixture's entry point.
package main

import (
	"fmt"
	"log"

	"example.com/fixture/internal/gen"
	"example.com/fixture/internal/service"
	"example.com/fixture/internal/store"
	"example.com/fixture/internal/util"
)

func main() {
	st := store.NewAudited()
	st.Put(store.Item{ID: "sku-1", Price: 1999, Quantity: 10})

	svc := service.NewOrderService(st)
	line, err := svc.PlaceOrder("sku-1", 2)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(line, gen.Shipped.String(), util.Mirror("ok"))
}
