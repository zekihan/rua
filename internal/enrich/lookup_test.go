package enrich

import (
	"net/netip"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCymruNames(t *testing.T) {
	t.Parallel()

	v4 := netip.MustParseAddr("216.90.108.31")
	if cymruOriginName(v4) != "31.108.90.216.origin.asn.cymru.com" {
		t.Fatalf("ipv4 name = %s", cymruOriginName(v4))
	}

	v6 := netip.MustParseAddr("2001:4860:b002::68")

	const wantV6 = "8.6.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.2.0.0.b.0.6.8.4.1.0.0.2.origin6.asn.cymru.com"

	if cymruOriginName(v6) != wantV6 {
		t.Fatalf("ipv6 name = %s", cymruOriginName(v6))
	}

	if asName(23028) != "AS23028.asn.cymru.com" {
		t.Fatalf("asn name = %s", asName(23028))
	}
}

func TestParseCymru(t *testing.T) {
	t.Parallel()

	asn, err := parseOriginASN("701 1239 | 216.90.108.0/24 | US | arin | 1998-09-25")
	if err != nil || asn != 701 {
		t.Fatalf("origin = %d, %v", asn, err)
	}

	quoted := firstRecord([]string{`"23028 | 216.90.108.0/24 | US | arin | 1998-09-25"`})

	asn, err = parseOriginASN(quoted)
	if err != nil || asn != 23028 {
		t.Fatalf("quoted origin = %d, %v", asn, err)
	}

	_, err = parseOriginASN("not a record")
	if err == nil {
		t.Fatal("expected format error")
	}

	org, err := parseASOrg("23028 | US | arin | 2002-01-04 | TEAM-CYMRU - Team Cymru Inc., US")
	if err != nil || org != "TEAM-CYMRU - Team Cymru Inc." {
		t.Fatalf("org = %q, %v", org, err)
	}

	org, err = parseASOrg("15169 | US | arin | 2000-03-30 | GOOGLE - Google LLC, US")
	if err != nil || org != "GOOGLE - Google LLC" {
		t.Fatalf("google = %q, %v", org, err)
	}
}

func TestSanitize(t *testing.T) {
	t.Parallel()

	if sanitize("bad\xffname") != "badname" {
		t.Fatalf("invalid utf-8 = %q", sanitize("bad\xffname"))
	}

	long := strings.Repeat("a", maxTextBytes+10)
	got := sanitize(long)

	if len(got) != maxTextBytes || !utf8.ValidString(got) {
		t.Fatalf("capped len = %d valid = %v", len(got), utf8.ValidString(got))
	}

	if sanitize("  spaced \x00 ") != "spaced" {
		t.Fatalf("trimmed = %q", sanitize("  spaced \x00 "))
	}
}
