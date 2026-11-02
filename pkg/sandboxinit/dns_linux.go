//go:build linux

package sandboxinit

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"
)

const (
	// egressDNSv4Address and egressDNSv6Address are the sandbox-local
	// resolvers. Traffic to any port 53 is redirected here so the workload
	// resolves through the policy DNS.
	egressDNSv4Address = "127.0.0.1:53"
	egressDNSv6Address = "[::1]:53"
	// egressDNSv4First/Last bound the synthetic 198.18.0.0/15 block reserved
	// for benchmark addresses. A synthetic answer has no meaning outside this
	// DNS stub, so a direct connection to one is either mapped back to a name or
	// refused.
	egressDNSv4First = 0xC6120001 // 198.18.0.1
	egressDNSv4Last  = 0xC613FFFE // 198.19.255.254
	// egressDNSRecordTTL keeps synthetic answers short-lived, matching the
	// OpenShell contract: the name, not the address, is the policy subject.
	egressDNSRecordTTL = 30
	// egressDNSMaxMessage bounds one UDP/TCP DNS message.
	egressDNSMaxMessage = 4096
)

// egressDNS is the per-sandbox policy DNS. It answers A queries with synthetic
// addresses from 198.18.0.0/15, leaves AAAA empty so a workload resolves over
// IPv4, and passes engine-owned hostnames through to their real addresses so
// the daemon facade stays reachable.
type egressDNS struct {
	sandboxID   string
	passthrough map[string][]net.IP

	mu       sync.Mutex
	nameToIP map[string]net.IP
	ipToName map[string]string
	next     uint32

	closers []io.Closer
}

// startEgressDNS binds the loopback resolvers. A guest without a usable IPv4
// loopback is an error; an unavailable IPv6 resolver is tolerated because the
// guest can still resolve over IPv4.
func startEgressDNS(sandboxID string, passthrough map[string][]net.IP) (*egressDNS, error) {
	dns := &egressDNS{
		sandboxID:   sandboxID,
		passthrough: passthrough,
		nameToIP:    make(map[string]net.IP),
		ipToName:    make(map[string]string),
		next:        egressDNSv4First - 1,
	}
	udp4, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53})
	if err != nil {
		return nil, fmt.Errorf("listen on the sandbox DNS resolver (IPv4 UDP): %w", err)
	}
	dns.closers = append(dns.closers, udp4)
	go dns.serveUDP(udp4)
	if tcp4, err := net.Listen("tcp4", egressDNSv4Address); err == nil {
		dns.closers = append(dns.closers, tcp4)
		go dns.serveTCP(tcp4)
	} else {
		slog.Warn("sandbox init DNS resolver could not listen for IPv4 TCP", "error", err)
	}
	if udp6, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 53}); err == nil {
		dns.closers = append(dns.closers, udp6)
		go dns.serveUDP(udp6)
	}
	if tcp6, err := net.Listen("tcp6", egressDNSv6Address); err == nil {
		dns.closers = append(dns.closers, tcp6)
		go dns.serveTCP(tcp6)
	}
	return dns, nil
}

// egressDNSPassthrough collects engine-owned hostnames that must resolve to
// their real addresses instead of a synthetic one: the daemon facade and the
// proxy are reached directly, and giving them a synthetic address would route
// the engine's own endpoint through the egress policy.
func egressDNSPassthrough(proxy *egressProxyTarget) map[string][]net.IP {
	passthrough := make(map[string][]net.IP)
	candidates := make([]string, 0, 2)
	if runtimeBase := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_RUNTIME_BASE_URL")); runtimeBase != "" {
		candidates = append(candidates, runtimeBase)
	}
	if proxy != nil {
		candidates = append(candidates, "http://"+proxy.authority)
	}
	for _, raw := range candidates {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := parsed.Hostname()
		if host == "" || net.ParseIP(host) != nil {
			continue
		}
		name, ok := normalizeDNSName(host)
		if !ok {
			continue
		}
		if _, exists := passthrough[name]; exists {
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			slog.Warn("sandbox init policy DNS cannot pass through an engine hostname", "host", host, "error", err)
			continue
		}
		passthrough[name] = ips
	}
	return passthrough
}

func (d *egressDNS) close() {
	if d == nil {
		return
	}
	for _, closer := range d.closers {
		_ = closer.Close()
	}
}

// rawAddrForDomain returns the resolver sockaddr matching the workload socket's
// address family, so an AF_INET6 socket is redirected to ::1 rather than to an
// AF_INET address it cannot connect to.
func (d *egressDNS) rawAddrForDomain(domain int) []byte {
	buffer := make([]byte, 28)
	switch domain {
	case unix.AF_INET6:
		binary.NativeEndian.PutUint16(buffer[0:2], unix.AF_INET6)
		binary.BigEndian.PutUint16(buffer[2:4], 53)
		copy(buffer[8:24], net.IPv6loopback.To16())
		return buffer
	default:
		buffer = make([]byte, 16)
		binary.NativeEndian.PutUint16(buffer[0:2], unix.AF_INET)
		binary.BigEndian.PutUint16(buffer[2:4], 53)
		copy(buffer[4:8], net.IPv4(127, 0, 0, 1).To4())
		return buffer
	}
}

// hostnameForIP maps a synthetic answer back to the name the workload asked
// for, which is the subject the daemon policy is declared against.
func (d *egressDNS) hostnameForIP(ip net.IP) (string, bool) {
	if d == nil || ip == nil {
		return "", false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	name, ok := d.ipToName[ip.String()]
	return name, ok
}

// syntheticIP reports whether ip belongs to the synthetic answer block.
func (d *egressDNS) syntheticIP(ip net.IP) bool {
	if d == nil || ip == nil {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	value := binary.BigEndian.Uint32(ip4)
	return value >= egressDNSv4First && value <= egressDNSv4Last
}

func (d *egressDNS) syntheticFor(name string) net.IP {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ip, ok := d.nameToIP[name]; ok {
		return ip
	}
	for attempts := 0; attempts < int(egressDNSv4Last-egressDNSv4First)+1; attempts++ {
		d.next++
		if d.next > egressDNSv4Last {
			d.next = egressDNSv4First
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, d.next)
		if _, used := d.ipToName[ip.String()]; used {
			continue
		}
		d.nameToIP[name] = ip
		d.ipToName[ip.String()] = name
		return ip
	}
	return nil
}

func (d *egressDNS) serveUDP(conn *net.UDPConn) {
	buffer := make([]byte, egressDNSMaxMessage)
	for {
		read, address, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		response := d.answer(buffer[:read])
		if response == nil {
			continue
		}
		_, _ = conn.WriteToUDP(response, address)
	}
}

func (d *egressDNS) serveTCP(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go d.handleTCPConn(conn)
	}
}

func (d *egressDNS) handleTCPConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	for {
		var length [2]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return
		}
		size := int(binary.BigEndian.Uint16(length[:]))
		if size < 1 || size > egressDNSMaxMessage {
			return
		}
		query := make([]byte, size)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		response := d.answer(query)
		if response == nil {
			return
		}
		binary.BigEndian.PutUint16(length[:], uint16(len(response)))
		if _, err := conn.Write(length[:]); err != nil {
			return
		}
		if _, err := conn.Write(response); err != nil {
			return
		}
	}
}

// answer parses one query and builds the response. A nil response means the
// query was malformed and the caller should drop it.
func (d *egressDNS) answer(query []byte) []byte {
	var parser dnsmessage.Parser
	header, err := parser.Start(query)
	if err != nil {
		return nil
	}
	question, err := parser.Question()
	if err != nil {
		return d.buildError(header, dnsmessage.RCodeFormatError)
	}
	name, ok := normalizeDNSName(question.Name.String())
	if !ok {
		return d.buildError(header, dnsmessage.RCodeNameError)
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		Authoritative:      true,
		RecursionDesired:   header.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeSuccess,
	})
	if err := builder.StartQuestions(); err != nil {
		return nil
	}
	if err := builder.Question(question); err != nil {
		return nil
	}
	answers := d.answers(name, question)
	if len(answers) > 0 {
		if err := builder.StartAnswers(); err != nil {
			return nil
		}
		for _, answer := range answers {
			if err := d.writeAnswer(&builder, question, answer); err != nil {
				return nil
			}
		}
	}
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

// dnsAnswer is one address the stub may return. Synthetic answers carry only an
// IPv4 address; passthrough answers may carry either family.
type dnsAnswer struct {
	ip net.IP
}

func (d *egressDNS) answers(name string, question dnsmessage.Question) []dnsAnswer {
	if ips, ok := d.passthrough[name]; ok {
		answers := make([]dnsAnswer, 0, len(ips))
		for _, ip := range ips {
			if !addressMatchesType(ip, question.Type) {
				continue
			}
			answers = append(answers, dnsAnswer{ip: ip})
		}
		return answers
	}
	if question.Type != dnsmessage.TypeA {
		// AAAA and every other type get an empty, successful answer so a
		// dual-stack resolver falls back to IPv4.
		return nil
	}
	ip := d.syntheticFor(name)
	if ip == nil {
		return nil
	}
	return []dnsAnswer{{ip: ip}}
}

func addressMatchesType(ip net.IP, queryType dnsmessage.Type) bool {
	switch queryType {
	case dnsmessage.TypeA:
		return ip.To4() != nil
	case dnsmessage.TypeAAAA:
		return ip.To4() == nil && ip.To16() != nil
	default:
		return false
	}
}

func (d *egressDNS) writeAnswer(builder *dnsmessage.Builder, question dnsmessage.Question, answer dnsAnswer) error {
	header := dnsmessage.ResourceHeader{
		Name:  question.Name,
		Class: dnsmessage.ClassINET,
		TTL:   egressDNSRecordTTL,
	}
	if ip4 := answer.ip.To4(); ip4 != nil {
		header.Type = dnsmessage.TypeA
		var value [4]byte
		copy(value[:], ip4)
		return builder.AResource(header, dnsmessage.AResource{A: value})
	}
	ip16 := answer.ip.To16()
	if ip16 == nil {
		return errors.New("DNS answer is not an IP address")
	}
	header.Type = dnsmessage.TypeAAAA
	var value [16]byte
	copy(value[:], ip16)
	return builder.AAAAResource(header, dnsmessage.AAAAResource{AAAA: value})
}

func (d *egressDNS) buildError(header dnsmessage.Header, code dnsmessage.RCode) []byte {
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		Authoritative:      true,
		RecursionDesired:   header.RecursionDesired,
		RecursionAvailable: true,
		RCode:              code,
	})
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

func normalizeDNSName(name string) (string, bool) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" {
		return "", false
	}
	return name, true
}
