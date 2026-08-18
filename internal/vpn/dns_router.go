package vpn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const defaultDNSUpstreamDeadline = 2 * time.Second

// dnsRouter sends each private zone only to its assigned resolver.
type dnsRouter struct {
	mu       sync.RWMutex
	routes   map[string]netip.AddrPort
	bindAddr netip.Addr
	deadline time.Duration
	udp      *net.UDPConn
	tcp      *net.TCPListener
	addr     netip.AddrPort
	wg       sync.WaitGroup
	once     sync.Once
}

func startDNSRouter(listen netip.AddrPort, routes map[string]netip.AddrPort, deadline time.Duration) (*dnsRouter, error) {
	if !listen.IsValid() {
		return nil, fmt.Errorf("invalid DNS listen address")
	}
	if deadline <= 0 {
		deadline = defaultDNSUpstreamDeadline
	}
	udp, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(listen))
	if err != nil {
		return nil, fmt.Errorf("listen for UDP DNS: %w", err)
	}
	addr := udp.LocalAddr().(*net.UDPAddr).AddrPort()
	tcp, err := net.ListenTCP("tcp", net.TCPAddrFromAddrPort(addr))
	if err != nil {
		udp.Close()
		return nil, fmt.Errorf("listen for TCP DNS: %w", err)
	}
	router := &dnsRouter{
		routes: cloneDNSRoutes(routes), bindAddr: addr.Addr(), deadline: deadline,
		udp: udp, tcp: tcp, addr: addr,
	}
	router.wg.Add(2)
	go router.serveUDP()
	go router.serveTCP()
	return router, nil
}

func (r *dnsRouter) Addr() netip.AddrPort { return r.addr }

func (r *dnsRouter) Update(routes map[string]netip.AddrPort) {
	r.mu.Lock()
	r.routes = cloneDNSRoutes(routes)
	r.mu.Unlock()
}

func (r *dnsRouter) Close() {
	r.once.Do(func() {
		_ = r.udp.Close()
		_ = r.tcp.Close()
		r.wg.Wait()
	})
}

func (r *dnsRouter) serveUDP() {
	defer r.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, client, err := r.udp.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		request := append([]byte(nil), buf[:n]...)
		if !isLocalDNSClient(client.Addr(), r.bindAddr) {
			continue
		}
		go func() {
			response := r.resolve("udp", request)
			_, _ = r.udp.WriteToUDPAddrPort(response, client)
		}()
	}
}

func (r *dnsRouter) serveTCP() {
	defer r.wg.Done()
	for {
		conn, err := r.tcp.AcceptTCP()
		if err != nil {
			return
		}
		if !isLocalDNSClient(conn.RemoteAddr().(*net.TCPAddr).AddrPort().Addr(), r.bindAddr) {
			_ = conn.Close()
			continue
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer conn.Close()
			for {
				_ = conn.SetDeadline(time.Now().Add(r.deadline))
				request, err := readDNSPacket(conn)
				if err != nil {
					return
				}
				if err := writeDNSPacket(conn, r.resolve("tcp", request)); err != nil {
					return
				}
			}
		}()
	}
}

func (r *dnsRouter) resolve(network string, request []byte) []byte {
	resolver, ok := r.resolverFor(request)
	if !ok {
		return dnsErrorResponse(request, dnsmessage.RCodeRefused)
	}
	response, err := exchangeWithResolver(network, resolver, request, r.deadline)
	if err != nil || !validDNSResponse(request, response) {
		return dnsErrorResponse(request, dnsmessage.RCodeServerFailure)
	}
	return response
}

func (r *dnsRouter) resolverFor(request []byte) (netip.AddrPort, bool) {
	var parser dnsmessage.Parser
	header, err := parser.Start(request)
	if err != nil || header.Response {
		return netip.AddrPort{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var resolver netip.AddrPort
	questions := 0
	for {
		question, err := parser.Question()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return netip.AddrPort{}, false
		}
		candidate, ok := resolverForDNSName(question.Name.String(), r.routes)
		if !ok || (questions > 0 && candidate != resolver) {
			return netip.AddrPort{}, false
		}
		resolver = candidate
		questions++
	}
	return resolver, questions > 0
}

func validDNSResponse(request, response []byte) bool {
	return len(request) >= 2 && len(response) >= 12 &&
		request[0] == response[0] && request[1] == response[1] && response[2]&0x80 != 0
}

func resolverForDNSName(rawName string, routes map[string]netip.AddrPort) (netip.AddrPort, bool) {
	name := strings.TrimSuffix(strings.ToLower(rawName), ".")
	var selected string
	var resolver netip.AddrPort
	for zone, candidate := range routes {
		if name != zone && !strings.HasSuffix(name, "."+zone) {
			continue
		}
		if len(zone) > len(selected) {
			selected = zone
			resolver = candidate
		}
	}
	return resolver, selected != ""
}

func isLocalDNSClient(client, listener netip.Addr) bool {
	return client.IsLoopback() || client == listener
}

func cloneDNSRoutes(routes map[string]netip.AddrPort) map[string]netip.AddrPort {
	cloned := make(map[string]netip.AddrPort, len(routes))
	for zone, resolver := range routes {
		zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
		if zone != "" && resolver.IsValid() {
			cloned[zone] = resolver
		}
	}
	return cloned
}

func dnsRoutesForPlans(plans []ResolverPlan) map[string]netip.AddrPort {
	routes := make(map[string]netip.AddrPort, len(plans))
	for _, plan := range plans {
		if plan.Resolver.IsValid() {
			routes[plan.Zone] = netip.AddrPortFrom(plan.Resolver, 53)
		}
	}
	return routes
}

func exchangeWithResolver(network string, resolver netip.AddrPort, request []byte, deadline time.Duration) ([]byte, error) {
	conn, err := net.DialTimeout(network, resolver.String(), deadline)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(deadline)); err != nil {
		return nil, err
	}
	if network == "tcp" {
		if err := writeDNSPacket(conn, request); err != nil {
			return nil, err
		}
		return readDNSPacket(conn)
	}
	if _, err := conn.Write(request); err != nil {
		return nil, err
	}
	response := make([]byte, 65535)
	n, err := conn.Read(response)
	if err != nil {
		return nil, err
	}
	return response[:n], nil
}

func dnsErrorResponse(request []byte, code dnsmessage.RCode) []byte {
	var parser dnsmessage.Parser
	header, err := parser.Start(request)
	if err != nil {
		return minimalDNSError(request, code)
	}
	question, err := parser.Question()
	if err != nil {
		return minimalDNSError(request, code)
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: header.ID, Response: true, OpCode: header.OpCode,
		RecursionDesired: header.RecursionDesired, RCode: code,
	})
	if err := builder.StartQuestions(); err != nil {
		return minimalDNSError(request, code)
	}
	if err := builder.Question(question); err != nil {
		return minimalDNSError(request, code)
	}
	response, err := builder.Finish()
	if err != nil {
		return minimalDNSError(request, code)
	}
	return response
}

func minimalDNSError(request []byte, code dnsmessage.RCode) []byte {
	response := make([]byte, 12)
	if len(request) >= 2 {
		copy(response[:2], request[:2])
	}
	response[2] = 0x80
	response[3] = byte(code)
	return response
}

func readDNSPacket(reader io.Reader) ([]byte, error) {
	var size [2]byte
	if _, err := io.ReadFull(reader, size[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint16(size[:])
	if length == 0 {
		return nil, errors.New("empty TCP DNS message")
	}
	message := make([]byte, length)
	_, err := io.ReadFull(reader, message)
	return message, err
}

func writeDNSPacket(writer io.Writer, message []byte) error {
	if len(message) == 0 || len(message) > 65535 {
		return fmt.Errorf("invalid TCP DNS message length %d", len(message))
	}
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(message)))
	if _, err := writer.Write(size[:]); err != nil {
		return err
	}
	_, err := writer.Write(message)
	return err
}
