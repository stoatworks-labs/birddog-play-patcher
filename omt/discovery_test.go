package main

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func mustName(t *testing.T, s string) dnsmessage.Name {
	t.Helper()
	n, err := dnsmessage.NewName(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// mdnsResponse is what a libomtnet sender's responder sends for a browse: the
// PTR as the answer, its SRV, TXT and A in the additionals.
func mdnsResponse(t *testing.T, instance, target string, port uint16, ip [4]byte, withAAAA bool) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	b.EnableCompression()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	inst := mustName(t, instance+"."+omtService)
	hdr := func(n dnsmessage.Name, typ dnsmessage.Type) dnsmessage.ResourceHeader {
		return dnsmessage.ResourceHeader{Name: n, Type: typ, Class: dnsmessage.ClassINET, TTL: 120}
	}
	must(b.StartAnswers())
	must(b.PTRResource(hdr(mustName(t, omtService), dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: inst}))
	must(b.StartAdditionals())
	must(b.TXTResource(hdr(inst, dnsmessage.TypeTXT), dnsmessage.TXTResource{TXT: []string{""}}))
	must(b.SRVResource(hdr(inst, dnsmessage.TypeSRV), dnsmessage.SRVResource{Target: mustName(t, target), Port: port}))
	if withAAAA {
		must(b.AAAAResource(hdr(mustName(t, target), dnsmessage.TypeAAAA), dnsmessage.AAAAResource{AAAA: [16]byte{0xfe, 0x80, 15: 1}}))
	}
	must(b.AResource(hdr(mustName(t, target), dnsmessage.TypeA), dnsmessage.AResource{A: ip}))
	pkt, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return pkt
}

func TestCollectorReadsABrowseAnswer(t *testing.T) {
	c := newCollector()
	// An AAAA before the A is what macOS sends; skipping it must not lose the A.
	c.add(mdnsResponse(t, "STUDIO-PC (vMix - Output 1)", "studio-pc.local.", 6400, [4]byte{192, 168, 1, 20}, true))
	c.add(mdnsResponse(t, "MAC (omtspike)", "mac.local.", 6530, [4]byte{192, 168, 1, 90}, false))
	if inc := c.incomplete(); len(inc) != 0 {
		t.Fatalf("incomplete after full answers: %v", inc)
	}
	got := c.sources()
	want := []Discovered{
		{Name: "MAC (omtspike)", Host: "MAC", Label: "omtspike", Port: 6530, IPs: []string{"192.168.1.90"}, Address: "omt://192.168.1.90:6530"},
		{Name: "STUDIO-PC (vMix - Output 1)", Host: "STUDIO-PC", Label: "vMix - Output 1", Port: 6400, IPs: []string{"192.168.1.20"}, Address: "omt://192.168.1.20:6400"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestCollectorWaitsForTheSRVAndA(t *testing.T) {
	c := newCollector()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true})
	_ = b.StartAnswers()
	_ = b.PTRResource(dnsmessage.ResourceHeader{Name: mustName(t, omtService), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET},
		dnsmessage.PTRResource{PTR: mustName(t, "HOST (Cam)."+omtService)})
	pkt, _ := b.Finish()
	c.add(pkt)
	if len(c.sources()) != 0 || len(c.incomplete()) != 1 {
		t.Fatal("a PTR alone must be incomplete, not a source")
	}
	// Queries (not responses) are ignored.
	q, _ := mdnsQuery(omtService, dnsmessage.TypePTR)
	c.add(q)
	c.add([]byte{1, 2, 3})
}

func TestUnescapeInstance(t *testing.T) {
	for in, want := range map[string]string{
		`MAC\032(omtspike)`: "MAC (omtspike)",
		`a\.b`:              "a.b",
		`plain`:             "plain",
	} {
		if got := unescapeInstance(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestParseAddress(t *testing.T) {
	for in, want := range map[string]struct {
		host string
		port int
	}{
		"omt://192.168.1.5:6401": {"192.168.1.5", 6401},
		"192.168.1.5:6402":       {"192.168.1.5", 6402},
		"192.168.1.5":            {"192.168.1.5", 6400},
		"studio-pc.local":        {"studio-pc.local", 6400},
		"[fe80::1]:6400":         {"fe80::1", 6400},
		"fe80::1":                {"fe80::1", 6400},
		" omt://host:7000 ":      {"host", 7000},
	} {
		h, p, err := parseAddress(in)
		if err != nil || h != want.host || p != want.port {
			t.Errorf("%q: got %q %d %v, want %q %d", in, h, p, err, want.host, want.port)
		}
	}
	for _, bad := range []string{"", "http://host:80", "host:0", "host:99999", ":6400", "host:port"} {
		if _, _, err := parseAddress(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestResolve(t *testing.T) {
	browses := 0
	d := &Discovery{Log: NewLogger(), Browse: func(context.Context, time.Duration) ([]Discovered, error) {
		browses++
		return []Discovered{{Name: "PC (Out)", Host: "pc-omt-test.invalid", Port: 6401, IPs: []string{"10.0.0.2", "10.0.0.3"}}}, nil
	}}
	ctx := context.Background()
	got, err := d.Resolve(ctx, Source{Name: "PC (Out)"})
	if err != nil || !reflect.DeepEqual(got, []string{"10.0.0.2:6401", "10.0.0.3:6401"}) {
		t.Fatalf("by name: %v %v", got, err)
	}
	// A name no longer advertised falls back to the address it was saved with.
	got, err = d.Resolve(ctx, Source{Name: "Gone (x)", Address: "omt://10.0.0.9:6405"})
	if err != nil || !reflect.DeepEqual(got, []string{"10.0.0.9:6405"}) {
		t.Fatalf("fallback: %v %v", got, err)
	}
	if _, err = d.Resolve(ctx, Source{Name: "Gone (x)"}); err == nil {
		t.Fatal("an unadvertised name with no address resolved")
	}
	// A machine name DNS does not know comes from what mDNS saw.
	got, err = d.Resolve(ctx, Source{Address: "pc-omt-test.invalid:6401"})
	if err != nil || len(got) != 2 {
		if _, lerr := net.LookupHost("pc-omt-test.invalid"); lerr == nil {
			t.Skip("this network resolves a .invalid name itself")
		}
		t.Fatalf("mDNS host: %v %v", got, err)
	}
	if browses > 3 {
		t.Fatalf("%d browses; the cache is not being used", browses)
	}
}
