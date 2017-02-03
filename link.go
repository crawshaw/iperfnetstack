package main

import (
	"log"
	"time"

	"github.com/google/netstack/tcpip"
	"github.com/google/netstack/tcpip/buffer"
	"github.com/google/netstack/tcpip/header"
	"github.com/google/netstack/tcpip/stack"
)

type dropLink struct {
	count      int
	count2     int
	dispatcher stack.NetworkDispatcher
	lower      stack.LinkEndpoint
}

func newDrop(lower tcpip.LinkEndpointID) tcpip.LinkEndpointID {
	return stack.RegisterLinkEndpoint(&dropLink{
		lower: stack.FindLinkEndpoint(lower),
	})
}

func (e *dropLink) DeliverNetworkPacket(linkEP stack.LinkEndpoint, remoteLinkAddr tcpip.LinkAddress, protocol tcpip.NetworkProtocolNumber, vv *buffer.VectorisedView) {
	if e.count > 800 {
		time.Sleep(50 * time.Millisecond)
	}

	e.dispatcher.DeliverNetworkPacket(e, remoteLinkAddr, protocol, vv)
}

func (e *dropLink) Attach(dispatcher stack.NetworkDispatcher) {
	e.dispatcher = dispatcher
	e.lower.Attach(e)
}

func (e *dropLink) MTU() uint32                    { return e.lower.MTU() }
func (e *dropLink) MaxHeaderLength() uint16        { return e.lower.MaxHeaderLength() }
func (e *dropLink) LinkAddress() tcpip.LinkAddress { return e.lower.LinkAddress() }

func (e *dropLink) WritePacket(r *stack.Route, hdr *buffer.Prependable, payload buffer.View, protocol tcpip.NetworkProtocolNumber) error {
	e.count++
	if e.count%800 == 0 {
		log.Printf("dropping outgoing packet %d", e.count)
		return nil
	}
	err := e.lower.WritePacket(r, hdr, payload, protocol)
	if isRetransmit(protocol, hdr.UsedBytes(), payload) {
		err = e.lower.WritePacket(r, hdr, payload, protocol)
	}
	return err
}

func isRetransmit(protocol tcpip.NetworkProtocolNumber, b, plb []byte) bool {
	var transProto uint8
	switch protocol {
	case header.IPv4ProtocolNumber:
		ipv4 := header.IPv4(b)
		transProto = ipv4.Protocol()
		b = b[ipv4.HeaderLength():]
	default:
		log.Printf("%s unknown network protocol", "")
		return false
	}

	switch tcpip.TransportProtocolNumber(transProto) {
	case header.TCPProtocolNumber:
		tcp := header.TCP(b)

		flags := tcp.Flags()
		if flags&(1<<3) != 0 {
			log.Print("is a retrasmit packet")
			return true
		}
	}

	return false
}
