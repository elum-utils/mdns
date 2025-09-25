package main

import (
	"context"
	"flag"
	"log"

	"github.com/elum-utils/mdns"
)

var (
	name    = flag.String("name", "GoService", "Service instance name to lookup")
	service = flag.String("service", "_workstation._tcp", "Service type")
	domain  = flag.String("domain", "local.", "Search domain")
)

func main() {
	flag.Parse() 

	resolver, err := mdns.NewResolver()
	if err != nil {
		log.Fatal("Failed to initialize resolver:", err)
	}

	err = resolver.Lookup(context.Background(), *name, *service, *domain, func(se *mdns.ServiceEntry) {
		log.Printf("Found service %s at %s:%d", se.Instance, se.AddrIPv4, se.Port)
	})

	if err != nil {
		log.Fatal("Failed to lookup:", err)
	}

	select {}
}
