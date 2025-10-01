package main

import (
	"context"
	"flag"
	"log"

	"github.com/elum-utils/mdns"
)

var (
	name    = flag.String("name", "GMELUM MDNS", "The name for the service.")
	service = flag.String("service", "_workstation._tcp", "Set the service type of the new service.")
	domain  = flag.String("domain", "local.", "Set the network domain. Default should be fine.")
	port    = flag.Int("port", 42424, "Set the port the service is listening to.")
)

func main() {
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, err := mdns.Register(ctx, *name, *service, *domain, *port, []string{"txtv=0", "lo=1", "la=2"}, nil)
	if err != nil {
		panic(err)
	}

	log.Println("Published service:")
	log.Println("- Name:", *name)
	log.Println("- Type:", *service)
	log.Println("- Domain:", *domain)
	log.Println("- Port:", *port)

	err = server.Start()
	if err != nil {
		println(err.Error())
	}

	log.Println("Shutting down.")

}
