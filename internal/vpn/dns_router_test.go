package vpn

import (
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDNSRouterRoutesEachPrivateZoneToItsAssignedResolver(t *testing.T) {
	first := startFakeDNSResolver(t, netip.MustParseAddr("10.1.0.10"))
	second := startFakeDNSResolver(t, netip.MustParseAddr("10.2.0.20"))
	public := startFakeDNSResolver(t, netip.MustParseAddr("203.0.113.9"))

	router, err := startDNSRouter(
		netip.MustParseAddrPort("127.0.0.1:0"),
		map[string]netip.AddrPort{
			"alpha.runos.xyz":     first.endpoint,
			"beta.runos.xyz":      second.endpoint,
			"deep.beta.runos.xyz": first.endpoint,
		},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("start DNS router: %v", err)
	}
	t.Cleanup(func() { router.Close() })

	assertDNSAnswer(t, "udp", router.Addr(), "alpha.runos.xyz.", "10.1.0.10")
	assertDNSAnswer(t, "udp", router.Addr(), "api.alpha.runos.xyz.", "10.1.0.10")
	assertDNSAnswer(t, "tcp", router.Addr(), "beta.runos.xyz.", "10.2.0.20")
	assertDNSAnswer(t, "udp", router.Addr(), "api.deep.beta.runos.xyz.", "10.1.0.10")

	if first.Count() != 3 {
		t.Fatalf("first resolver received %d queries, want 3", first.Count())
	}
	if second.Count() != 1 {
		t.Fatalf("second resolver received %d queries, want 1", second.Count())
	}
	if public.Count() != 0 {
		t.Fatalf("public resolver received %d private queries", public.Count())
	}

	assertDNSRCode(t, router.Addr(), "public.example.", dnsmessage.RCodeRefused)
	if public.Count() != 0 {
		t.Fatal("an unconfigured query reached the public resolver")
	}
	response := exchangeDNS(t, "udp", router.Addr(), dnsQuestions(t, "alpha.runos.xyz.", "public.example."))
	var parser dnsmessage.Parser
	header, err := parser.Start(response)
	if err != nil || header.RCode != dnsmessage.RCodeRefused {
		t.Fatalf("mixed private and public questions did not fail closed: header=%+v err=%v", header, err)
	}

	first.Close()
	assertDNSRCode(t, router.Addr(), "alpha.runos.xyz.", dnsmessage.RCodeServerFailure)
	assertDNSAnswer(t, "udp", router.Addr(), "beta.runos.xyz.", "10.2.0.20")
	router.Update(map[string]netip.AddrPort{"beta.runos.xyz": second.endpoint})
	assertDNSRCode(t, router.Addr(), "alpha.runos.xyz.", dnsmessage.RCodeRefused)
}

func TestDNSRouterReturnsOneHundredCorrectAnswersPerCluster(t *testing.T) {
	first := startFakeDNSResolver(t, netip.MustParseAddr("10.1.0.10"))
	second := startFakeDNSResolver(t, netip.MustParseAddr("10.2.0.20"))
	router, err := startDNSRouter(
		netip.MustParseAddrPort("127.0.0.1:0"),
		map[string]netip.AddrPort{
			"alpha.runos.xyz": first.endpoint,
			"beta.runos.xyz":  second.endpoint,
		},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("start DNS router: %v", err)
	}
	t.Cleanup(router.Close)
	for index := 0; index < 100; index++ {
		assertDNSAnswer(t, "udp", router.Addr(), "api.alpha.runos.xyz.", "10.1.0.10")
		assertDNSAnswer(t, "udp", router.Addr(), "api.beta.runos.xyz.", "10.2.0.20")
	}
	if first.Count() != 100 || second.Count() != 100 {
		t.Fatalf("resolver counts are %d and %d, want 100 each", first.Count(), second.Count())
	}
}

func TestDNSRouterRejectsRemoteVPNPeers(t *testing.T) {
	listener := netip.MustParseAddr("172.24.8.10")
	if !isLocalDNSClient(listener, listener) {
		t.Fatal("the VPN client address must be local")
	}
	if !isLocalDNSClient(netip.MustParseAddr("127.0.0.1"), listener) {
		t.Fatal("loopback must be local")
	}
	if isLocalDNSClient(netip.MustParseAddr("172.24.8.11"), listener) {
		t.Fatal("a remote VPN peer must not query the local router")
	}
}

type fakeDNSResolver struct {
	endpoint netip.AddrPort
	answer   netip.Addr
	udp      *net.UDPConn
	tcp      *net.TCPListener
	mu       sync.Mutex
	count    int
}

func startFakeDNSResolver(t *testing.T, answer netip.Addr) *fakeDNSResolver {
	t.Helper()
	udp, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatalf("listen on fake UDP resolver: %v", err)
	}
	endpoint := udp.LocalAddr().(*net.UDPAddr).AddrPort()
	tcp, err := net.ListenTCP("tcp", net.TCPAddrFromAddrPort(endpoint))
	if err != nil {
		udp.Close()
		t.Fatalf("listen on fake TCP resolver: %v", err)
	}
	r := &fakeDNSResolver{endpoint: endpoint, answer: answer, udp: udp, tcp: tcp}
	t.Cleanup(r.Close)
	go r.serveUDP()
	go r.serveTCP()
	return r
}

func (r *fakeDNSResolver) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func (r *fakeDNSResolver) increment() {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
}

func (r *fakeDNSResolver) Close() {
	if r.udp != nil {
		r.udp.Close()
	}
	if r.tcp != nil {
		r.tcp.Close()
	}
}

func (r *fakeDNSResolver) serveUDP() {
	buf := make([]byte, 65535)
	for {
		n, client, err := r.udp.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		r.increment()
		response := dnsAResponse(buf[:n], r.answer)
		_, _ = r.udp.WriteToUDPAddrPort(response, client)
	}
}

func (r *fakeDNSResolver) serveTCP() {
	for {
		conn, err := r.tcp.AcceptTCP()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			request, err := readTCPDNSMessage(conn)
			if err != nil {
				return
			}
			r.increment()
			_ = writeTCPDNSMessage(conn, dnsAResponse(request, r.answer))
		}()
	}
}

func assertDNSAnswer(t *testing.T, network string, endpoint netip.AddrPort, name, want string) {
	t.Helper()
	response := exchangeDNS(t, network, endpoint, dnsQuery(t, name))
	var parser dnsmessage.Parser
	header, err := parser.Start(response)
	if err != nil {
		t.Fatalf("parse DNS response: %v", err)
	}
	if header.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("DNS response code is %v, want success", header.RCode)
	}
	if err := parser.SkipAllQuestions(); err != nil {
		t.Fatalf("skip DNS questions: %v", err)
	}
	if _, err := parser.AnswerHeader(); err != nil {
		t.Fatalf("parse DNS answer header: %v", err)
	}
	answer, err := parser.AResource()
	if err != nil {
		t.Fatalf("parse DNS answer: %v", err)
	}
	if got := netip.AddrFrom4(answer.A).String(); got != want {
		t.Fatalf("DNS answer is %s, want %s", got, want)
	}
}

func assertDNSRCode(t *testing.T, endpoint netip.AddrPort, name string, want dnsmessage.RCode) {
	t.Helper()
	response := exchangeDNS(t, "udp", endpoint, dnsQuery(t, name))
	var parser dnsmessage.Parser
	header, err := parser.Start(response)
	if err != nil {
		t.Fatalf("parse DNS response: %v", err)
	}
	if header.RCode != want {
		t.Fatalf("DNS response code is %v, want %v", header.RCode, want)
	}
}

func dnsQuery(t *testing.T, rawName string) []byte {
	return dnsQuestions(t, rawName)
}

func dnsQuestions(t *testing.T, rawNames ...string) []byte {
	t.Helper()
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 41, RecursionDesired: true})
	if err := builder.StartQuestions(); err != nil {
		t.Fatalf("start DNS questions: %v", err)
	}
	for _, rawName := range rawNames {
		name, err := dnsmessage.NewName(rawName)
		if err != nil {
			t.Fatalf("make DNS name: %v", err)
		}
		if err := builder.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}); err != nil {
			t.Fatalf("write DNS question: %v", err)
		}
	}
	query, err := builder.Finish()
	if err != nil {
		t.Fatalf("finish DNS query: %v", err)
	}
	return query
}

func dnsAResponse(request []byte, answer netip.Addr) []byte {
	var parser dnsmessage.Parser
	header, err := parser.Start(request)
	if err != nil {
		return nil
	}
	question, err := parser.Question()
	if err != nil {
		return nil
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: header.ID, Response: true, RecursionDesired: header.RecursionDesired, RecursionAvailable: true,
	})
	_ = builder.StartQuestions()
	_ = builder.Question(question)
	_ = builder.StartAnswers()
	_ = builder.AResource(dnsmessage.ResourceHeader{
		Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30,
	}, dnsmessage.AResource{A: answer.As4()})
	response, _ := builder.Finish()
	return response
}

func exchangeDNS(t *testing.T, network string, endpoint netip.AddrPort, request []byte) []byte {
	t.Helper()
	conn, err := net.DialTimeout(network, endpoint.String(), time.Second)
	if err != nil {
		t.Fatalf("dial DNS router: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if network == "tcp" {
		if err := writeTCPDNSMessage(conn, request); err != nil {
			t.Fatalf("write TCP DNS request: %v", err)
		}
		response, err := readTCPDNSMessage(conn)
		if err != nil {
			t.Fatalf("read TCP DNS response: %v", err)
		}
		return response
	}
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write UDP DNS request: %v", err)
	}
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read UDP DNS response: %v", err)
	}
	return buf[:n]
}

func readTCPDNSMessage(r io.Reader) ([]byte, error) {
	var size [2]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	message := make([]byte, binary.BigEndian.Uint16(size[:]))
	_, err := io.ReadFull(r, message)
	return message, err
}

func writeTCPDNSMessage(w io.Writer, message []byte) error {
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(message)))
	if _, err := w.Write(size[:]); err != nil {
		return err
	}
	_, err := w.Write(message)
	return err
}
