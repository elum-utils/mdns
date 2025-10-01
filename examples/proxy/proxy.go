package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/elum-utils/mdns"
)

var (
	name    = flag.String("name", "ProxyService", "Service instance name")
	service = flag.String("service", "_workstation._tcp", "Service type")
	domain  = flag.String("domain", "local.", "Network domain")
	host    = flag.String("host", "my-host", "Proxy hostname")
	ip      = flag.String("ip", "192.168.1.50", "Proxy IP address")
	port    = flag.Int("port", 42424, "Service port")
)

func main() {
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, err := mdns.RegisterProxy(ctx, *name, *service, *domain, *port, *host,
		[]string{*ip}, []string{"txtv=1", "proxy=true"}, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer server.Shutdown()

	log.Printf("Published proxy service %s on %s:%d\n", *name, *ip, *port)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	<-sig

	log.Println("Shutting down.")
}
