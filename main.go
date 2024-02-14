package main

import (
	"fmt"

	"github.com/flashbots/mev-boost-relay/datastore"
)

func main() {
	fmt.Println("- run -")

	fmt.Println(datastore.NewRedisCache("custom", "127.0.0.1:6379", ""))
}
