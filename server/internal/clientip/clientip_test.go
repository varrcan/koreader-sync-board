package clientip

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func mustPrefixes(t *testing.T, s string) []netip.Prefix {
	t.Helper()
	p, err := ParsePrefixes(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAddr(t *testing.T) {
	tests := []struct {
		name    string
		trusted string
		remote  string
		xff     []string
		want    string
	}{
		{"no trusted proxies ignore xff", "", "203.0.113.5:4000", []string{"1.2.3.4"}, "203.0.113.5"},
		{"untrusted source ignores xff", "10.0.0.0/8", "203.0.113.5:4000", []string{"1.2.3.4"}, "203.0.113.5"},
		{"trusted proxy", "10.0.0.0/8", "10.0.0.2:4000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"trusted proxy without xff", "10.0.0.0/8", "10.0.0.2:4000", nil, "10.0.0.2"},
		{"spoofed left element", "10.0.0.0/8", "10.0.0.2:4000", []string{"1.1.1.1, 198.51.100.7"}, "198.51.100.7"},
		{"proxy chain", "10.0.0.0/8, 172.16.0.0/12", "10.0.0.2:4000", []string{"1.1.1.1, 198.51.100.7, 172.16.0.3"}, "198.51.100.7"},
		{"all trusted take leftmost", "192.168.0.0/16", "192.168.0.10:4000", []string{"192.168.0.74"}, "192.168.0.74"},
		{"all trusted chain", "192.168.0.0/16", "192.168.0.10:4000", []string{"192.168.0.74, 192.168.0.2"}, "192.168.0.74"},
		{"several headers", "10.0.0.0/8", "10.0.0.2:4000", []string{"1.1.1.1", "198.51.100.7, 10.0.0.9"}, "198.51.100.7"},
		{"unparsable element", "10.0.0.0/8", "10.0.0.2:4000", []string{"198.51.100.7, garbage"}, "10.0.0.2"},
		{"unparsable left of client", "10.0.0.0/8", "10.0.0.2:4000", []string{"garbage, 198.51.100.7"}, "198.51.100.7"},
		{"empty element", "10.0.0.0/8", "10.0.0.2:4000", []string{"198.51.100.7, "}, "10.0.0.2"},
		{"mapped remote", "10.0.0.0/8", "[::ffff:10.0.0.2]:4000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"mapped in xff", "10.0.0.0/8", "10.0.0.2:4000", []string{"::ffff:198.51.100.7"}, "198.51.100.7"},
		{"mapped trusted hop in xff", "10.0.0.0/8", "10.0.0.2:4000", []string{"198.51.100.7, ::ffff:10.0.0.3"}, "198.51.100.7"},
		{"ipv6 remote", "", "[2001:db8::1]:4000", nil, "2001:db8::1"},
		{"ipv6 loopback proxy", "::1", "[::1]:4000", []string{"2001:db8::7"}, "2001:db8::7"},
		{"zone dropped", "", "[fe80::1%eth0]:4000", nil, "fe80::1"},
		{"single address proxy", "10.0.0.2", "10.0.0.3:4000", []string{"1.2.3.4"}, "10.0.0.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := New(mustPrefixes(t, tt.trusted)).Addr(r); got.String() != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNilResolver(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:4000"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	var res *Resolver
	if got := res.Addr(r); got.String() != "10.0.0.2" {
		t.Fatalf("got %s", got)
	}
	r.RemoteAddr = "@"
	if got := res.Addr(r); got.IsValid() {
		t.Fatalf("unix socket address parsed as %s", got)
	}
}

func TestParsePrefixes(t *testing.T) {
	got := mustPrefixes(t, " 10.0.0.0/8 ,192.168.1.10, ::1,, 2001:db8::/32, ::ffff:172.16.0.0/108, 10.1.2.3/16")
	want := []string{"10.0.0.0/8", "192.168.1.10/32", "::1/128", "2001:db8::/32", "172.16.0.0/12", "10.1.0.0/16"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("%d: got %s, want %s", i, got[i], want[i])
		}
	}
	if p := mustPrefixes(t, ""); len(p) != 0 {
		t.Fatalf("empty: %v", p)
	}
	for _, bad := range []string{"10.0.0.0/33", "example.com", "10.0.0.0/8, nope"} {
		if _, err := ParsePrefixes(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	_, err := ParsePrefixes("10.0.0.0/8, nope")
	if err == nil || err.Error() != `invalid address or CIDR "nope"` {
		t.Fatalf("error does not name the element: %v", err)
	}
}
