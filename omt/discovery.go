package main

// Finding OMT sources.
//
// Senders advertise over DNS-SD as "HOST (Source Name)._omt._tcp.local". libomt
// browses through Avahi on Linux, and the PLAY's rootfs has no avahi-daemon —
// that is the trap the spike hit, where stock libomt either returned a NULL
// receiver or aborted. So this is a small mDNS querier of its own, using two
// routes because the PLAY's own decoder may already own port 5353 for NDI:
//
//   - a legacy unicast query from an ephemeral port (RFC 6762 §6.7), which
//     every responder must answer straight back to that port. Needs no shared
//     socket at all.
//   - a normal query from port 5353 joined to 224.0.0.251, if the bind is
//     allowed, which also catches responders that only answer by multicast.
//
// An address can always be typed instead (omt://host:port or host:port), which
// is how a sender on another subnet, where multicast does not reach, is used.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	omtService   = "_omt._tcp.local."
	mdnsAddr     = "224.0.0.251:5353"
	defaultOMTPt = 6400
)

// Source names an OMT sender: by its advertised name, by address, or both. The
// name is what survives a DHCP renewal; the address is what is dialled.
type Source struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address,omitempty"`
}

func (s Source) IsZero() bool { return s.Name == "" && s.Address == "" }

// Label is how the source is shown in logs and the tab.
func (s Source) Label() string {
	switch {
	case s.Name != "":
		return s.Name
	case s.Address != "":
		return s.Address
	}
	return "(no source)"
}

// Discovered is one source found on the network.
type Discovered struct {
	Name    string   `json:"name"`  // "HOST (Source Name)"
	Host    string   `json:"host"`  // HOST
	Label   string   `json:"label"` // Source Name
	Port    int      `json:"port"`
	IPs     []string `json:"ips"`
	Address string   `json:"address"` // omt://first-ip:port
}

// splitSourceName splits "HOST (Source Name)" the way libomtnet's OMTAddress
// does: the machine name is everything before the first "(".
func splitSourceName(full string) (host, label string) {
	i := strings.Index(full, "(")
	if i < 0 {
		return strings.TrimSpace(full), ""
	}
	host = strings.TrimSpace(full[:i])
	label = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(full[i+1:]), ")"))
	return host, label
}

// parseAddress turns a typed address into host and port. It accepts
// omt://host:port, host:port and a bare host (port 6400, the first port a
// libomtnet sender tries).
func parseAddress(s string) (host string, port int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, errors.New("empty address")
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", 0, fmt.Errorf("address %q: %v", s, err)
		}
		if u.Scheme != "omt" {
			return "", 0, fmt.Errorf("address %q: expected omt://host:port", s)
		}
		s = u.Host
	}
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		// No port: a bare host or IP (including an unbracketed IPv6 address).
		if strings.Count(s, ":") > 1 || !strings.Contains(s, ":") {
			return strings.Trim(s, "[]"), defaultOMTPt, nil
		}
		return "", 0, fmt.Errorf("address %q: %v", s, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return "", 0, fmt.Errorf("address %q: bad port", s)
	}
	if h == "" {
		return "", 0, fmt.Errorf("address %q: no host", s)
	}
	return h, n, nil
}

func isHostPort(s string) bool {
	_, _, err := net.SplitHostPort(s)
	return err == nil
}

// Discovery browses for sources and resolves them for the receiver.
type Discovery struct {
	// Browse is the network query; tests replace it.
	Browse func(ctx context.Context, wait time.Duration) ([]Discovered, error)
	Log    *Logger

	mu      sync.Mutex
	last    []Discovered
	lastAt  time.Time
	running chan struct{}
}

func NewDiscovery(log *Logger) *Discovery {
	return &Discovery{Browse: browseMDNS, Log: log}
}

// Sources returns what was found, browsing again if the last answer is older
// than maxAge. Concurrent callers share one browse.
func (d *Discovery) Sources(ctx context.Context, maxAge time.Duration) ([]Discovered, error) {
	d.mu.Lock()
	if time.Since(d.lastAt) < maxAge {
		out := d.last
		d.mu.Unlock()
		return out, nil
	}
	if ch := d.running; ch != nil {
		d.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		d.mu.Lock()
		out := d.last
		d.mu.Unlock()
		return out, nil
	}
	ch := make(chan struct{})
	d.running = ch
	d.mu.Unlock()

	found, err := d.Browse(ctx, 1500*time.Millisecond)

	d.mu.Lock()
	if err == nil {
		d.last, d.lastAt = found, time.Now()
	}
	d.running = nil
	out := d.last
	d.mu.Unlock()
	close(ch)
	return out, err
}

// Resolve turns a Source into addresses to dial. A name is looked up by
// browsing (the address it was saved with is the fallback, for a sender that
// has stopped answering mDNS but not TCP); an address is used as typed, with
// its host resolved.
func (d *Discovery) Resolve(ctx context.Context, s Source) ([]string, error) {
	var out []string
	if s.Name != "" {
		for _, maxAge := range []time.Duration{30 * time.Second, 0} {
			found, _ := d.Sources(ctx, maxAge)
			for _, f := range found {
				if f.Name == s.Name {
					for _, ip := range f.IPs {
						out = append(out, net.JoinHostPort(ip, strconv.Itoa(f.Port)))
					}
				}
			}
			if len(out) > 0 {
				return out, nil
			}
		}
	}
	if s.Address == "" {
		return nil, fmt.Errorf("%q is not being advertised on this network right now", s.Name)
	}
	host, port, err := parseAddress(s.Address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		return []string{net.JoinHostPort(host, strconv.Itoa(port))}, nil
	}
	// A machine name, as omt:// URLs carry: try DNS, then the hosts mDNS
	// told us about.
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	ips, err := net.DefaultResolver.LookupHost(rctx, host)
	cancel()
	if err != nil || len(ips) == 0 {
		found, _ := d.Sources(ctx, 30*time.Second)
		for _, f := range found {
			if strings.EqualFold(f.Host, host) || strings.EqualFold(f.Host, strings.TrimSuffix(host, ".local")) {
				ips = append(ips, f.IPs...)
			}
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("cannot resolve %q", host)
	}
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	return out, nil
}

// browseMDNS asks the local network for _omt._tcp services and waits for
// answers.
func browseMDNS(ctx context.Context, wait time.Duration) ([]Discovered, error) {
	group, err := net.ResolveUDPAddr("udp4", mdnsAddr)
	if err != nil {
		return nil, err
	}
	var conns []*net.UDPConn

	// Route 1: legacy unicast from an ephemeral port.
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, fmt.Errorf("mdns: %w", err)
	}
	conns = append(conns, uc)
	// Route 2: the shared multicast socket, if the box lets us have it.
	if mc, err := net.ListenMulticastUDP("udp4", nil, group); err == nil {
		conns = append(conns, mc)
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	q, err := mdnsQuery(omtService, dnsmessage.TypePTR)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	col := newCollector()
	var wg sync.WaitGroup
	for _, c := range conns {
		c := c
		_ = c.SetReadDeadline(deadline)
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 9000)
			for {
				n, _, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				col.add(buf[:n])
			}
		}()
	}
	// Ask twice, a moment apart: a lost first datagram is the usual reason a
	// source is missing from a list.
	for i := 0; i < 2; i++ {
		for _, c := range conns {
			_, _ = c.WriteToUDP(q, group)
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
		}
	}
	// Instances that answered without their SRV or address get asked directly.
	for _, name := range col.incomplete() {
		for _, t := range []dnsmessage.Type{dnsmessage.TypeSRV, dnsmessage.TypeA} {
			qn := name
			if t == dnsmessage.TypeA {
				qn = col.target(name)
				if qn == "" {
					continue
				}
			}
			if pkt, err := mdnsQuery(qn, t); err == nil {
				_, _ = uc.WriteToUDP(pkt, group)
			}
		}
	}
	wg.Wait()
	return col.sources(), nil
}

func mdnsQuery(name string, t dnsmessage.Type) ([]byte, error) {
	n, err := dnsmessage.NewName(name)
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: n, Type: t, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// collector merges records from every answer heard.
type collector struct {
	mu        sync.Mutex
	instances map[string]bool            // instance names from PTR records
	srv       map[string]srvRec          // instance -> target, port
	addrs     map[string]map[string]bool // host -> IPs
}

type srvRec struct {
	target string
	port   int
}

func newCollector() *collector {
	return &collector{instances: map[string]bool{}, srv: map[string]srvRec{}, addrs: map[string]map[string]bool{}}
}

func (c *collector) add(pkt []byte) {
	var p dnsmessage.Parser
	h, err := p.Start(pkt)
	if err != nil || !h.Response {
		return
	}
	if err := p.SkipAllQuestions(); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Responders put the SRV and A records a PTR answer needs in the
	// additional section, so read the answers and the additionals both.
	for {
		rh, err := p.AnswerHeader()
		if err != nil {
			if err != dnsmessage.ErrSectionDone {
				return
			}
			break
		}
		if !c.record(&p, rh, p.SkipAnswer) {
			return
		}
	}
	if err := p.SkipAllAuthorities(); err != nil {
		return
	}
	for {
		rh, err := p.AdditionalHeader()
		if err != nil {
			return
		}
		if !c.record(&p, rh, p.SkipAdditional) {
			return
		}
	}
}

// record takes in one resource, or skips it. It reports false when the packet
// cannot be read further.
func (c *collector) record(p *dnsmessage.Parser, rh dnsmessage.ResourceHeader, skip func() error) bool {
	name := strings.ToLower(rh.Name.String())
	switch rh.Type {
	case dnsmessage.TypePTR:
		r, err := p.PTRResource()
		if err != nil {
			return false
		}
		if name == omtService {
			// Keep the instance name's case: it is the source's display name.
			c.instances[r.PTR.String()] = true
		}
	case dnsmessage.TypeSRV:
		r, err := p.SRVResource()
		if err != nil {
			return false
		}
		c.srv[name] = srvRec{target: strings.ToLower(r.Target.String()), port: int(r.Port)}
	case dnsmessage.TypeA:
		r, err := p.AResource()
		if err != nil {
			return false
		}
		if c.addrs[name] == nil {
			c.addrs[name] = map[string]bool{}
		}
		c.addrs[name][net.IP(r.A[:]).String()] = true
	default:
		if skip() != nil {
			return false
		}
	}
	return true
}

// incomplete lists instances still missing a port or an address.
func (c *collector) incomplete() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for inst := range c.instances {
		s, ok := c.srv[strings.ToLower(inst)]
		if !ok || len(c.addrs[s.target]) == 0 {
			out = append(out, inst)
		}
	}
	return out
}

func (c *collector) target(inst string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.srv[strings.ToLower(inst)].target
}

func (c *collector) sources() []Discovered {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Discovered
	for inst := range c.instances {
		s, ok := c.srv[strings.ToLower(inst)]
		if !ok {
			continue
		}
		var ips []string
		for ip := range c.addrs[s.target] {
			ips = append(ips, ip)
		}
		if len(ips) == 0 {
			continue
		}
		sort.Strings(ips)
		full := unescapeInstance(strings.TrimSuffix(inst, "."+omtService))
		host, label := splitSourceName(full)
		out = append(out, Discovered{
			Name:    full,
			Host:    host,
			Label:   label,
			Port:    s.port,
			IPs:     ips,
			Address: "omt://" + net.JoinHostPort(ips[0], strconv.Itoa(s.port)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// unescapeInstance undoes DNS presentation escaping (\032 for a space, \. for
// a dot), which dnsmessage applies to labels holding those characters.
func unescapeInstance(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if i+3 < len(s) && isDigit(s[i+1]) && isDigit(s[i+2]) && isDigit(s[i+3]) {
				n, _ := strconv.Atoi(s[i+1 : i+4])
				b.WriteByte(byte(n))
				i += 3
				continue
			}
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
