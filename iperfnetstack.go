package main

import (
	"log"
	"math/rand"
	"net"
	"os"
	"time"

	"github.com/google/netstack/tcpip"
	"github.com/google/netstack/tcpip/link/fdbased"
	"github.com/google/netstack/tcpip/link/rawfile"
	"github.com/google/netstack/tcpip/link/sniffer"
	"github.com/google/netstack/tcpip/link/tun"
	"github.com/google/netstack/tcpip/network/ipv4"
	"github.com/google/netstack/tcpip/stack"
	"github.com/google/netstack/tcpip/transport/tcp"
)

func main() {
	if len(os.Args) != 4 {
		log.Fatal("Usage: ", os.Args[0], " <tun-device> <local-ipv4-address> <remote-ipv4-address>")
	}

	tunName := os.Args[1]
	addrName := os.Args[2]
	remoteAddrName := os.Args[3]

	rand.Seed(time.Now().UnixNano())

	addr := tcpip.Address(net.ParseIP(addrName).To4())
	remoteAddr := tcpip.Address(net.ParseIP(remoteAddrName).To4())
	log.Printf("remoteAddr: %v", remoteAddr)

	s := stack.New([]string{ipv4.ProtocolName}, []string{tcp.ProtocolName})

	mtu, err := rawfile.GetMTU(tunName)
	if err != nil {
		log.Fatalf("mtu: %v", err)
	}
	log.Printf("MTU: %v", mtu)

	fd, err := tun.Open(tunName)
	if err != nil {
		log.Fatalf("tun.Open: %v", err)
	}

	linkID := fdbased.New(fd, mtu, nil)
	if err := s.CreateNIC(1, sniffer.New(newDrop(linkID))); err != nil {
		log.Fatalf("create NIC: %v", err)
	}

	if err := s.AddAddress(1, ipv4.ProtocolNumber, addr); err != nil {
		log.Fatalf("add address: %v", err)
	}

	s.SetRouteTable([]tcpip.Route{
		{
			Destination: "\x00\x00\x00\x00",
			Mask:        "\x00\x00\x00\x00",
			Gateway:     "",
			NIC:         1,
		},
	})

	c := new(iperfClient)
	if err := c.run(s, remoteAddr); err != nil {
		log.Fatal(err)
	}
}
