package ci

import (
	"fmt"
	"net/netip"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// CIDR arithmetic.
//
// A SUBNET IS A RANGE, and every rule the IPAM module enforces is a statement
// about how two ranges sit relative to each other: a root may not overlap
// another root, a child must sit inside its parent, siblings may not overlap.
// The functions here are the whole of that, PURE AND FREE OF STORAGE — which is
// how they are tested, and which is what lets the store apply them inside the
// transaction that writes them.
//
// THEY LIVE IN biz AND ARE CALLED FROM data, for the same reason the rack
// arithmetic does: a rule checked outside a transaction is a rule two callers
// can both pass.

// The CIDR rules, and where they come from.
const (
	// MinSubnetPrefix is the shortest prefix cantor accepted. A /7 or wider is
	// refused, which makes the largest expressible subnet a /8.
	MinSubnetPrefix = 8

	// MaxSubnetPrefix is the longest. A /32 — a single address — is REFUSED, so
	// a one-address range cannot be expressed as a subnet at all. That is
	// cantor's bound and the console's own range picker shares it.
	MaxSubnetPrefix = 31
)

// IPAM errors.
var (
	// ErrCIDRInvalid is returned for a range that is not a network, or whose
	// prefix is outside the accepted bounds.
	//
	// HOST BITS SET IS INVALID, which is Python's `ip_network` default and worth
	// keeping: `10.0.0.1/24` names the same range as `10.0.0.0/24`, and a
	// database holding both spellings makes "which subnet is this address in"
	// a string comparison. Go's parser ACCEPTS the host bits and silently
	// masks them, so the check is explicit here.
	ErrCIDRInvalid = fmt.Errorf("%w: not a valid subnet", errs.ErrInvalid)

	// ErrSubnetOverlap is returned when a range collides with a sibling or a
	// root's peer.
	ErrSubnetOverlap = fmt.Errorf("%w: the subnet overlaps another", errs.ErrConflict)

	// ErrSubnetPrefix is returned when a child's prefix is not longer than its
	// parent's, so it cannot be a subdivision of it.
	ErrSubnetPrefix = fmt.Errorf("%w: the subnet is not smaller than its parent", errs.ErrInvalid)

	// ErrSubnetOutsideParent is returned when a child's range is not inside its
	// parent's.
	ErrSubnetOutsideParent = fmt.Errorf("%w: the subnet is not inside its parent", errs.ErrInvalid)

	// ErrParentCIDRMissing is returned when a subnet is placed under another
	// subnet that declares no range — so there is nothing to be inside of.
	ErrParentCIDRMissing = fmt.Errorf("%w: the parent subnet has no range", errs.ErrInvalid)

	// ErrAddressOutsideSubnet is returned when an address is assigned to a
	// subnet whose range does not hold it.
	//
	// IT IS A NEW CHECK. cantor's assignment path never compared the address to
	// the range: its lookup was "does this subnet already have an address with
	// this string", and an address it had never seen was created wherever the
	// caller pointed. A subnet could therefore hold addresses from anywhere.
	ErrAddressOutsideSubnet = fmt.Errorf("%w: the address is not in the subnet's range", errs.ErrInvalid)
)

// ParseCIDR reads a subnet's range.
//
// It returns the network's canonical string, so the same range has one spelling
// in the database whichever way a caller wrote it.
func ParseCIDR(raw string) (string, error) {
	prefix, err := netip.ParsePrefix(trimSpace(raw))
	if err != nil {
		return "", ErrCIDRInvalid
	}
	// An IPv4-mapped or IPv6 form is refused by the bounds below rather than by
	// a separate check: the bits count only means what the rule says for IPv4.
	if !prefix.Addr().Is4() {
		return "", ErrCIDRInvalid
	}
	if prefix.Bits() < MinSubnetPrefix || prefix.Bits() > MaxSubnetPrefix {
		return "", ErrCIDRInvalid
	}
	// Host bits set: `netip` masks them for us, so the test is whether masking
	// changed anything.
	if prefix.Masked() != prefix {
		return "", ErrCIDRInvalid
	}
	return prefix.String(), nil
}

// HostCount is how many hosts a range holds.
//
// A /31 REPORTS TWO, not zero. The usual arithmetic — 2^(32-p) - 2, reserving
// the network and broadcast addresses — gives zero for a two-address range,
// which is why the formula is special-cased: Python's `ipaddress` returns both
// addresses for a /31 and the port follows it, because a /31 is the standard
// way to express a point-to-point link and reporting it as full is unusable.
func HostCount(cidr string) (int32, error) {
	prefix, err := netip.ParsePrefix(trimSpace(cidr))
	if err != nil {
		return 0, ErrCIDRInvalid
	}
	bits := 32 - prefix.Bits()
	if bits <= 1 {
		// A /31 or /32: every address in it.
		return 1 << bits, nil
	}
	return 1<<bits - 2, nil
}

// SubnetRange is a range two ranges can be compared by.
type SubnetRange struct{ prefix netip.Prefix }

// RangeOf reads a range for comparison. A range that does not parse is
// reported, because a subnet holding an unreadable `cidr` is a row a caller
// has to fix rather than one to compare against.
func RangeOf(cidr string) (SubnetRange, error) {
	prefix, err := netip.ParsePrefix(trimSpace(cidr))
	if err != nil {
		return SubnetRange{}, ErrCIDRInvalid
	}
	return SubnetRange{prefix: prefix.Masked()}, nil
}

// String renders the range.
func (r SubnetRange) String() string { return r.prefix.String() }

// PrefixLen is how many of the range's leading bits are the network's.
//
// A shorter prefix is a WIDER range, which is the comparison the parent rule is
// stated in: a child's prefix must be longer than its parent's or it is not a
// subdivision of it.
func (r SubnetRange) PrefixLen() int { return r.prefix.Bits() }

// Overlaps reports whether two ranges share any address.
func (r SubnetRange) Overlaps(other SubnetRange) bool {
	return r.prefix.Overlaps(other.prefix)
}

// Contains reports whether a range lies entirely inside another. It is not
// strict: a range is inside itself.
//
// BOTH HALVES ARE NEEDED. Testing the other range's network address alone is
// not enough — `10.0.0.0/25` holds the ADDRESS `10.0.0.0`, so it would "contain"
// `10.0.0.0/24`, whose range runs far past it. The width comparison is what
// makes the question about ranges rather than about their first addresses.
func (r SubnetRange) Contains(other SubnetRange) bool {
	return r.prefix.Bits() <= other.prefix.Bits() && r.prefix.Contains(other.prefix.Addr())
}

// Hosts lists a range's addresses, network and broadcast excluded the way
// `ipaddress.hosts()` excludes them.
//
// A /31 or /32 lists every address in it, matching HostCount.
func Hosts(cidr string, limit int) ([]string, error) {
	prefix, err := netip.ParsePrefix(trimSpace(cidr))
	if err != nil {
		return nil, ErrCIDRInvalid
	}
	prefix = prefix.Masked()
	bits := 32 - prefix.Bits()

	var first, last netip.Addr
	if bits <= 1 {
		first, last = prefix.Addr(), lastAddr(prefix)
	} else {
		first = prefix.Addr().Next()
		last = lastAddr(prefix).Prev()
	}
	if !first.IsValid() || !last.IsValid() || first.Compare(last) > 0 {
		return nil, nil
	}

	out := make([]string, 0, 16)
	for addr := first; addr.Compare(last) <= 0; addr = addr.Next() {
		if limit > 0 && len(out) >= limit {
			// THE LIMIT IS THE CALLER'S, and the caller reports it. A /8 holds
			// sixteen million addresses; the console renders a page of them.
			break
		}
		out = append(out, addr.String())
		if !addr.IsValid() {
			break
		}
	}
	return out, nil
}

// lastAddr is the highest address in a range: the network address with every
// host bit set.
func lastAddr(prefix netip.Prefix) netip.Addr {
	b := prefix.Masked().Addr().As4()
	bits := prefix.Bits()
	for i := 0; i < 4; i++ {
		// How many of this byte's bits are host bits, counting from the least
		// significant byte.
		hostBits := 32 - bits - (3-i)*8
		switch {
		case hostBits >= 8:
			b[i] = 0xFF
		case hostBits > 0:
			b[i] |= byte(0xFF >> uint(8-hostBits))
		}
	}
	return netip.AddrFrom4(b)
}

// ContainsAddress reports whether a range holds one address.
func (r SubnetRange) ContainsAddress(raw string) bool {
	addr, err := netip.ParseAddr(trimSpace(raw))
	if err != nil || !addr.Is4() {
		return false
	}
	return r.prefix.Contains(addr)
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// --- counting a subnet's addresses ---

// AddressState is one address's contribution to a subnet's counters.
type AddressState struct {
	// Status is the stored assignment state. Known says whether it reads as one
	// of the three: an address whose status was never written, or was written
	// as something else, is not claimed by anybody.
	Status AddressStatus
	Known  bool

	// Used is whether the address was observed in use.
	Used bool
}

// AddressCounters is what a subnet's three maintained numbers should be.
type AddressCounters struct {
	// Assign is how many addresses are spoken for.
	Assign int32

	// Used is how many are NOT AVAILABLE: spoken for, or observed in use.
	Used int32

	// Free is what the addresses leave.
	Free int32
}

// CountAddresses applies the module's counting rule.
//
// AN ADDRESS IS SPOKEN FOR WHEN IT IS ASSIGNED OR RESERVED. "Unassigned" is a
// STATE, not the absence of one — cantor kept the row and the value, so an
// address somebody gave back is explicitly free rather than merely unmarked.
//
// AN ADDRESS IS UNAVAILABLE WHEN IT IS SPOKEN FOR **OR OBSERVED IN USE**, and
// that union is what the free count is measured against. It is cantor's rule,
// established by running its own code over nine addresses covering every
// combination: the query behind it is an OR, and the measured counts are
// six claimed, three observed, seven unavailable. The union is also the safer
// reading — an address a scanner saw answering must not be handed out just
// because nobody recorded it.
//
// `used` THEREFORE COUNTS THE SAME SET THE FREE COUNT SUBTRACTS, so the three
// numbers always agree. cantor's STORED `used_count` was instead hard-set to
// the size of the last batch the caller sent, so it and its own `free_count`
// could describe different sets — measured, a subnet reporting two used and
// six free at the same time. See the plan's deviation table.
func CountAddresses(hosts int32, addrs []AddressState) AddressCounters {
	var claimed, unavailable int32
	for _, addr := range addrs {
		spokenFor := addr.Known && (addr.Status == AddressAssigned || addr.Status == AddressReserved)
		if spokenFor {
			claimed++
		}
		if spokenFor || addr.Used {
			unavailable++
		}
	}
	return AddressCounters{Assign: claimed, Used: unavailable, Free: hosts - unavailable}
}
