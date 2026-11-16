package ci

import (
	"errors"
	"testing"
)

// The CIDR rules, without a database.
//
// These are what the IPAM module refuses and what it counts, and every one of
// them is a statement about two ranges — so they are driven here rather than
// through a subnet tree.

func TestParseCIDRAcceptsAndRefuses(t *testing.T) {
	accept := []struct {
		in   string
		want string
	}{
		{"10.0.0.0/24", "10.0.0.0/24"},
		{"192.168.1.0/24", "192.168.1.0/24"},
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"10.0.0.0/31", "10.0.0.0/31"},
		{" 10.0.0.0/24 ", "10.0.0.0/24"},
	}
	for _, tc := range accept {
		got, err := ParseCIDR(tc.in)
		if err != nil {
			t.Fatalf("ParseCIDR(%q) refused: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseCIDR(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	refuse := []struct {
		in      string
		because string
	}{
		// HOST BITS SET IS REFUSED, which is Python's `ip_network` default.
		// Go's own parser ACCEPTS it and silently masks, so without this check
		// the port would store a second spelling of a range that already
		// exists — and "which subnet is this address in" would become a string
		// comparison between two rows that mean the same thing.
		{"10.0.0.1/24", "host bits are set"},
		{"10.0.0.128/24", "host bits are set"},
		{"10.0.0.0/32", "a single address is not a subnet"},
		{"10.0.0.0/7", "wider than the shortest accepted prefix"},
		{"10.0.0.0/0", "the whole internet is not a subnet"},
		{"10.0.0.0/33", "no such prefix"},
		{"10.0.0.0", "no prefix"},
		{"", "empty"},
		{"garbage", "not an address"},
		{"2001:db8::/32", "IPv6 is outside the four-number model"},
	}
	for _, tc := range refuse {
		if _, err := ParseCIDR(tc.in); !errors.Is(err, ErrCIDRInvalid) {
			t.Fatalf("ParseCIDR(%q) = %v, want a refusal (%s)", tc.in, err, tc.because)
		}
	}
}

func TestHostCount(t *testing.T) {
	cases := []struct {
		cidr    string
		want    int32
		because string
	}{
		{"10.0.0.0/24", 254, "2^8 - 2: the network and broadcast addresses are not hosts"},
		{"10.0.0.0/30", 2, ""},
		{"10.0.0.0/29", 6, ""},
		{"10.0.0.0/8", 16777214, ""},
		// A /31 REPORTS TWO, where the usual formula gives zero. Python's
		// `ipaddress` returns both addresses for a /31, and a /31 is the
		// standard way to write a point-to-point link — reporting it as full
		// would make every such link unusable.
		{"10.0.0.0/31", 2, "a point-to-point link holds both of its addresses"},
	}
	for _, tc := range cases {
		got, err := HostCount(tc.cidr)
		if err != nil {
			t.Fatalf("HostCount(%q): %v", tc.cidr, err)
		}
		if got != tc.want {
			t.Fatalf("HostCount(%q) = %d, want %d\nbecause %s", tc.cidr, got, tc.want, tc.because)
		}
	}
}

func TestHostsListsTheRange(t *testing.T) {
	got, err := Hosts("10.0.0.0/29", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5", "10.0.0.6"}
	if len(got) != len(want) {
		t.Fatalf("Hosts(/29) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Hosts(/29) = %v, want %v", got, want)
		}
	}

	// A /31 lists both of its addresses, matching HostCount.
	got, err = Hosts("10.0.0.0/31", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "10.0.0.0" || got[1] != "10.0.0.1" {
		t.Fatalf("Hosts(/31) = %v, want both addresses", got)
	}

	// The limit is the caller's, and it truncates rather than walking a /8.
	got, err = Hosts("10.0.0.0/8", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "10.0.0.1" {
		t.Fatalf("Hosts(/8, limit 3) = %v", got)
	}

	// The last address of each prefix, which is where an off-by-one in the
	// host-bit arithmetic shows up.
	for _, tc := range []struct{ cidr, last string }{
		{"10.0.0.0/24", "10.0.0.254"},
		{"10.0.0.0/25", "10.0.0.126"},
		{"10.0.0.128/25", "10.0.0.254"},
		{"10.0.1.0/23", "10.0.1.254"},
	} {
		got, err := Hosts(tc.cidr, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 || got[len(got)-1] != tc.last {
			t.Fatalf("Hosts(%s) ends at %v, want %s", tc.cidr, got[len(got)-1], tc.last)
		}
	}
}

func TestSubnetRangeRelations(t *testing.T) {
	must := func(cidr string) SubnetRange {
		t.Helper()
		r, err := RangeOf(cidr)
		if err != nil {
			t.Fatalf("RangeOf(%q): %v", cidr, err)
		}
		return r
	}

	cases := []struct {
		a, b               string
		overlaps, contains bool
		because            string
	}{
		{"10.0.0.0/24", "10.0.0.0/24", true, true,
			"a range is a subset of itself: Contains is not strict, and the rule that a " +
				"child must be SMALLER than its parent is the prefix check, which cantor " +
				"made separately"},
		{"10.0.0.0/24", "10.0.0.0/25", true, true, "a half sits inside the whole"},
		{"10.0.0.0/24", "10.0.0.128/25", true, true, "either half"},
		{"10.0.0.0/24", "10.0.1.0/24", false, false, "the next range along"},
		{"10.0.0.0/16", "10.0.99.0/24", true, true, ""},
		{"10.0.0.0/25", "10.0.0.0/24", true, false, "a half does not hold the whole"},
	}
	for _, tc := range cases {
		a, b := must(tc.a), must(tc.b)
		if got := a.Overlaps(b); got != tc.overlaps {
			t.Fatalf("%s.Overlaps(%s) = %v, want %v\nbecause %s", tc.a, tc.b, got, tc.overlaps, tc.because)
		}
		if got := a.Contains(b); got != tc.contains {
			t.Fatalf("%s.Contains(%s) = %v, want %v\nbecause %s", tc.a, tc.b, got, tc.contains, tc.because)
		}
	}
}

func TestContainsAddress(t *testing.T) {
	r, err := RangeOf("10.0.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"10.0.0.0", "10.0.0.1", "10.0.0.255"} {
		if !r.ContainsAddress(addr) {
			t.Fatalf("%s is in %s", addr, r)
		}
	}
	for _, addr := range []string{"10.0.1.0", "10.0.0.256", "garbage", "2001:db8::1"} {
		if r.ContainsAddress(addr) {
			t.Fatalf("%s is not in %s", addr, r)
		}
	}
}
