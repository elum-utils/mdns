package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

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

	server, err := mdns.Register(*name, *service, *domain, *port, []string{"txtv=0", "lo=1", "la=2"}, nil)
	if err != nil {
		panic(err)
	}
	defer server.Shutdown()
	log.Println("Published service:")
	log.Println("- Name:", *name)
	log.Println("- Type:", *service)
	log.Println("- Domain:", *domain)
	log.Println("- Port:", *port)

	// Clean exit.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	<-sig

	log.Println("Shutting down.")
}
