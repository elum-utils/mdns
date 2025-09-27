package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/elum-utils/mdns"
)

var (
	service = flag.String("service", "_workstation._tcp", "Service type")
	domain  = flag.String("domain", "local.", "Search domain")
)

// joinIPs объединяет IPv4
func joinIPs(se *mdns.ServiceEntry) string {
	ips := []string{}
	for _, ip := range se.AddrIPv4 {
		ips = append(ips, ip.String())
	}
	if len(ips) == 0 {
		return "-"
	}
	return strings.Join(ips, " ")
}

// truncateString обрезает строку до максимальной длины и добавляет "..." если нужно
func truncateString(str string, maxLen int) string {
	if len(str) <= maxLen {
		return str
	}
	return str[:maxLen-3] + "..."
}

func main() {
	flag.Parse()

	resolver, err := mdns.NewResolver()
	if err != nil {
		log.Fatal("Failed to initialize resolver:", err)
	}

	err = resolver.Browse(context.Background(), *service, *domain, func(se *mdns.ServiceEntry) {
		ts := time.Now().Format("15:04:05.000")
		ar := "Add"
		if strings.EqualFold(se.Event, "Rmv") {
			ar = "Rmv"
		}
		ipStr := joinIPs(se)
		
		// Ограничиваем длину полей для красивого вывода
		domainStr := truncateString(se.Domain, 20)
		serviceStr := truncateString(se.Service, 19)
		instanceStr := truncateString(se.Instance, 22)
		
		// Форматируем с фиксированными ширинами
		fmt.Printf("%s  %-4s  %4d   %2d  %-20s %-20s %-23s %s:%d\n",
			ts, ar, se.Flags, se.IfIndex, domainStr, serviceStr, instanceStr, ipStr, se.Port)
	})
	if err != nil {
		log.Fatal("Failed to lookup:", err)
	}

	// Бесконечный цикл вместо select{}
	for {
		time.Sleep(time.Hour)
	}
}