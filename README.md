# 📡 mdns — Multicast DNS (mDNS) for Go

A pure Go implementation of **Multicast DNS (mDNS) and DNS-SD (Service Discovery)**.
This library allows you to **register services**, **publish proxy services**, and **discover services** in local networks.

---

## 🚀 Installation

```bash
go get github.com/elum-utils/mdns
```

---

## 🔧 Usage

### 1. Register a Local Service

```go
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/elum-utils/mdns"
)

var (
	name     = flag.String("name", "GoService", "Service instance name")
	service  = flag.String("service", "_workstation._tcp", "Service type")
	domain   = flag.String("domain", "local.", "Network domain")
	port     = flag.Int("port", 42424, "Service port")
	waitTime = flag.Int("wait", 10, "Time in seconds to keep service alive")
)

func main() {
	flag.Parse()

	server, err := mdns.Register(*name, *service, *domain, *port,
		[]string{"txtv=1", "lo=1"}, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer server.Shutdown()

	log.Printf("Published service %s on port %d\n", *name, *port)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	var tc <-chan time.Time
	if *waitTime > 0 {
		tc = time.After(time.Duration(*waitTime) * time.Second)
	}

	select {
	case <-sig:
	case <-tc:
	}
	log.Println("Shutting down.")
}
```

---

### 2. Register a Proxy Service

```go
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/elum-utils/mdns"
)

var (
	name     = flag.String("name", "ProxyService", "Service instance name")
	service  = flag.String("service", "_workstation._tcp", "Service type")
	domain   = flag.String("domain", "local.", "Network domain")
	host     = flag.String("host", "my-host", "Proxy hostname")
	ip       = flag.String("ip", "192.168.1.50", "Proxy IP address")
	port     = flag.Int("port", 42424, "Service port")
	waitTime = flag.Int("wait", 10, "Time in seconds to keep service alive")
)

func main() {
	flag.Parse()

	server, err := mdns.RegisterProxy(*name, *service, *domain, *port, *host,
		[]string{*ip}, []string{"txtv=1", "proxy=true"}, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer server.Shutdown()

	log.Printf("Published proxy service %s on %s:%d\n", *name, *ip, *port)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	var tc <-chan time.Time
	if *waitTime > 0 {
		tc = time.After(time.Duration(*waitTime) * time.Second)
	}

	select {
	case <-sig:
	case <-tc:
	}
	log.Println("Shutting down.")
}
```

---

### 3. Discover Services (Browse)

```go
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/elum-utils/mdns"
)

var (
	service  = flag.String("service", "_workstation._tcp", "Service type to discover")
	domain   = flag.String("domain", "local.", "Search domain")
	waitTime = flag.Int("wait", 10, "Discovery duration in seconds")
)

func main() {
	flag.Parse()

	resolver, err := mdns.NewResolver()
	if err != nil {
		log.Fatal("Failed to initialize resolver:", err)
	}

	entries := make(chan *mdns.ServiceEntry)
	go func(results <-chan *mdns.ServiceEntry) {
		for entry := range results {
			log.Printf("Discovered: %s (%s:%d)", entry.Instance, entry.AddrIPv4, entry.Port)
		}
		log.Println("Discovery finished.")
	}(entries)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*waitTime)*time.Second)
	defer cancel()

	err = resolver.Browse(ctx, *service, *domain, entries)
	if err != nil {
		log.Fatal("Failed to browse:", err)
	}

	<-ctx.Done()
	time.Sleep(1 * time.Second)
}
```

---

### 4. Lookup a Specific Service

```go
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/elum-utils/mdns"
)

var (
	name     = flag.String("name", "GoService", "Service instance name to lookup")
	service  = flag.String("service", "_workstation._tcp", "Service type")
	domain   = flag.String("domain", "local.", "Search domain")
	waitTime = flag.Int("wait", 5, "Lookup timeout in seconds")
)

func main() {
	flag.Parse()

	resolver, err := mdns.NewResolver()
	if err != nil {
		log.Fatal("Failed to initialize resolver:", err)
	}

	entries := make(chan *mdns.ServiceEntry, 1)
	go func(results <-chan *mdns.ServiceEntry) {
		for entry := range results {
			log.Printf("Found service %s at %s:%d", entry.Instance, entry.AddrIPv4, entry.Port)
		}
	}(entries)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*waitTime)*time.Second)
	defer cancel()

	err = resolver.Lookup(ctx, *name, *service, *domain, entries)
	if err != nil {
		log.Fatal("Failed to lookup:", err)
	}

	<-ctx.Done()
}
```

---