package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync/atomic"
	"time"

	"github.com/google/netstack/tcpip"
	"github.com/google/netstack/tcpip/buffer"
	"github.com/google/netstack/tcpip/network/ipv4"
	"github.com/google/netstack/tcpip/transport/tcp"
	"github.com/google/netstack/waiter"
)

type state int8

const (
	TEST_START       state = 1
	TEST_RUNNING     state = 2
	RESULT_REQUEST   state = 3
	TEST_END         state = 4
	STREAM_BEGIN     state = 5
	STREAM_RUNNING   state = 6
	STREAM_END       state = 7
	ALL_STREAMS_END  state = 8
	PARAM_EXCHANGE   state = 9
	CREATE_STREAMS   state = 10
	SERVER_TERMINATE state = 11
	CLIENT_TERMINATE state = 12
	EXCHANGE_RESULTS state = 13
	DISPLAY_RESULTS  state = 14
	IPERF_START      state = 15
	IPERF_DONE       state = 16
	ACCESS_DENIED    state = -1
	SERVER_ERROR     state = -2
)

var stateNames = map[state]string{
	TEST_START:       "TEST_START",
	TEST_RUNNING:     "TEST_RUNNING",
	RESULT_REQUEST:   "RESULT_REQUEST",
	TEST_END:         "TEST_END",
	STREAM_BEGIN:     "STREAM_BEGIN",
	STREAM_RUNNING:   "STREAM_RUNNING",
	STREAM_END:       "STREAM_END",
	ALL_STREAMS_END:  "ALL_STREAMS_END",
	PARAM_EXCHANGE:   "PARAM_EXCHANGE",
	CREATE_STREAMS:   "CREATE_STREAMS",
	SERVER_TERMINATE: "SERVER_TERMINATE",
	CLIENT_TERMINATE: "CLIENT_TERMINATE",
	EXCHANGE_RESULTS: "EXCHANGE_RESULTS",
	DISPLAY_RESULTS:  "DISPLAY_RESULTS",
	IPERF_START:      "IPERF_START",
	IPERF_DONE:       "IPERF_DONE",
	ACCESS_DENIED:    "ACCESS_DENIED",
	SERVER_ERROR:     "SERVER_ERROR",
}

func (s state) String() string {
	n := stateNames[s]
	if n == "" {
		return fmt.Sprintf("state(unknown:%d)", s)
	}
	return n
}

type params struct {
	TCP           bool   `json:"tcp"`
	Omit          int    `json:"omit"`
	Time          int    `json:"time"`
	Parallel      int    `json:"parallel"`
	Len           int    `json:"len"`
	ClientVersion string `json:"client_version"`
}

type stream struct {
	wq waiter.Queue
	ep tcpip.Endpoint
}

func (s *stream) send(cmdch chan state, sent *uint64) {
	waitEntry, notifyCh := waiter.NewChannelEntry(nil)
	s.wq.EventRegister(&waitEntry, waiter.EventOut)
	defer s.wq.EventUnregister(&waitEntry)
	//sendloop:
	for {
		/* TODO
		select {
		case <-done:
			break sendloop
		default:
		}*/

		v := buffer.NewView(2048)
		v[0] = '\x1f'
		n, err := s.ep.Write(v, nil)
		if err == tcpip.ErrWouldBlock {
			<-notifyCh
			continue
		}
		if err == tcpip.ErrClosedForSend {
			log.Printf("iperf: connection closed")
			cmdch <- CLIENT_TERMINATE
			return
		}
		if err != nil {
			log.Printf("iperf: write failed: %v", err)
			cmdch <- CLIENT_TERMINATE
			return
		}
		if n > 0 {
			atomic.AddUint64(sent, uint64(n))
		}
	}
}

type iperfClient struct {
	s      tcpip.Stack
	remote tcpip.FullAddress

	wq waiter.Queue
	ep tcpip.Endpoint

	streams []*stream
}

func (c *iperfClient) sendJSON(data interface{}) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("iperf: send JSON: %v", err)
	}
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, uint32(len(b)))
	if _, err = c.ep.Write(buffer.View(size), nil); err != nil {
		return fmt.Errorf("iperf: send JSON size: %v", err)
	}
	if _, err = c.ep.Write(buffer.View(b), nil); err != nil {
		return fmt.Errorf("iperf: send JSON: %v", err)
	}
	return nil
}

func (c *iperfClient) read() (v buffer.View, err error) {
	waitEntry, notifyCh := waiter.NewChannelEntry(nil)
	c.wq.EventRegister(&waitEntry, waiter.EventIn)
	defer c.wq.EventUnregister(&waitEntry)
	for {
		v, err := c.ep.Read(nil)
		if err == tcpip.ErrClosedForReceive {
			return nil, io.EOF
		}
		if err == tcpip.ErrWouldBlock {
			<-notifyCh
			continue
		}
		return v, err
	}
}

const verbose = true

func (c *iperfClient) readControlMessage() (state, error) {
	v, err := c.read()
	if err != nil || len(v) != 1 {
		return SERVER_ERROR, fmt.Errorf("iperf: could not read control message: %v (%d)", err, len(v))
	}
	cmd := state(v[0])
	if cmd > IPERF_DONE {
		return SERVER_ERROR, fmt.Errorf("iperf: unknown control message: %d", cmd)
	}
	if verbose {
		log.Printf("iperf: got control message: %v", cmd)
	}
	return cmd, nil
}

func (c *iperfClient) connectStream(num int) (s *stream, err error) {
	s = new(stream)
	s.ep, err = c.s.NewEndpoint(tcp.ProtocolNumber, ipv4.ProtocolNumber, &s.wq)
	if err != nil {
		return nil, fmt.Errorf("iperf: stream endpoint failed %v", err)
	}

	waitEntry, notifyCh := waiter.NewChannelEntry(nil)
	s.wq.EventRegister(&waitEntry, waiter.EventOut)
	remote := c.remote
	remote.Port += uint16(num)
	err = s.ep.Connect(c.remote)
	if err == tcpip.ErrConnectStarted {
		<-notifyCh
		err = s.ep.GetSockOpt(tcpip.ErrorOption{})
	}
	s.wq.EventUnregister(&waitEntry)
	if err != nil {
		return nil, fmt.Errorf("iperf: stream connect: %v", err)
	}

	if _, err := s.ep.Write(buffer.View(iperfCookie), nil); err != nil {
		return nil, fmt.Errorf("iperf: could not send cookie: %v", err)
	}

	return s, nil
}

const COOKIE_SIZE = 37
const iperfCookie = "fuchsia.gonetstack.012345678901234567"

func init() {
	if len(iperfCookie) != COOKIE_SIZE {
		panic(fmt.Sprintf("bad cookie len: %d", len(iperfCookie)))
	}
}

func (c *iperfClient) run(s tcpip.Stack, remoteAddr tcpip.Address) error {
	c.s = s
	c.remote = tcpip.FullAddress{
		NIC:  1,
		Addr: remoteAddr,
		Port: 5201,
	}
	ep, err := s.NewEndpoint(tcp.ProtocolNumber, ipv4.ProtocolNumber, &c.wq)
	if err != nil {
		return fmt.Errorf("iperf: endpoint failed %v", err)
	}
	c.ep = ep

	if verbose {
		//log.Printf("iperf: connecting to %s", c.remote)
	}
	waitEntry, notifyCh := waiter.NewChannelEntry(nil)
	c.wq.EventRegister(&waitEntry, waiter.EventOut)
	err = ep.Connect(c.remote)
	if err == tcpip.ErrConnectStarted {
		<-notifyCh
		err = ep.GetSockOpt(tcpip.ErrorOption{})
	}
	c.wq.EventUnregister(&waitEntry)
	if err != nil {
		return fmt.Errorf("iperf: unable to connect %v", err)
	}
	if verbose {
		log.Print("iperf: connected")
	}

	if _, err := ep.Write(buffer.View(iperfCookie), nil); err != nil {
		return fmt.Errorf("iperf: could not send cookie: %v", err)
	}
	cmd, err := c.readControlMessage()
	if err != nil {
		return err
	}
	if cmd != PARAM_EXCHANGE {
		return fmt.Errorf("iperf: expected PARAM_EXCHANGE, got: %v", cmd)
	}
	err = c.sendJSON(params{
		TCP:           true,
		Time:          10,
		Parallel:      1,
		Len:           131072,
		ClientVersion: "3-CURRENT",
	})
	if err != nil {
		return err
	}
	cmd, err = c.readControlMessage()
	if err != nil {
		return err
	}
	if cmd != CREATE_STREAMS {
		return fmt.Errorf("iperf: expected CREATE_STREAMS, got: %v", cmd)
	}
	strm, err := c.connectStream(0)
	if err != nil {
		return err
	}
	c.streams = append(c.streams, strm)

	cmd, err = c.readControlMessage()
	if err != nil {
		return err
	}
	if cmd != TEST_START {
		return fmt.Errorf("iperf: expected TEST_START, got: %v", cmd)
	}

	cmdch := make(chan state)
	sent := uint64(0)

	go func() {
		cmd, err = c.readControlMessage()
		if err != nil {
			log.Printf("iperf: failed to read control message: %v", err)
			return
		}
		cmdch <- cmd
	}()

	for cmd := range cmdch {
		switch cmd {
		case TEST_RUNNING:
			for _, s := range c.streams {
				go s.send(cmdch, &sent)
			}
			go timeAndReport(cmdch, &sent)
		default:
			return fmt.Errorf("iperf: got control message: %v", cmd)
		}
	}

	if verbose {
		log.Printf("iperf: send complete")
	}
	return nil
}

func timeAndReport(cmdch chan state, sent *uint64) {
	start := time.Now()
	//done := make(chan struct{}) // TODO: use a context.Context

	last := start
	lastSent := uint64(0)
	for {
		time.Sleep(1 * time.Second)

		now := time.Now()
		d := now.Sub(last)
		sentSoFar := atomic.LoadUint64(sent)
		log.Printf("iperf: %s: %d bytes", d, sentSoFar-lastSent)
		/* TODO fix time.Now if now.Sub(start) > 5*time.Second {
			cmdch <- TEST_END
			return
		}*/
		last = now
		lastSent = sentSoFar
	}
}
