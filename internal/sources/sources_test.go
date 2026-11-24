package sources

import (
	"errors"
	"net/netip"
	"testing"
)

func TestParseEmpty(t *testing.T) {
	t.Parallel()

	documents := []string{"", "   \n", "---\n", "null\n", "[]\n", "# comment only\n"}

	for _, document := range documents {
		set, err := Parse([]byte(document))
		if err != nil {
			t.Fatalf("Parse(%q): %v", document, err)
		}

		if len(set.Rules()) != 0 {
			t.Fatalf("Parse(%q) rules = %v", document, set.Rules())
		}
	}
}

func TestParseNormalizesRules(t *testing.T) {
	t.Parallel()

	set := mustParse(t, `
- name: " Mail provider "
  expected: true
  match:
    cidrs: ["192.0.2.1", "2001:db8::1", "192.0.2.0/24"]
    ptr_suffixes: ["Mailer.Example.NET."]
    dkim_domains: ["Example.COM."]
    asns: [64500]
`)

	rules := set.Rules()
	if len(rules) != 1 {
		t.Fatalf("rules = %d", len(rules))
	}

	rule := rules[0]
	if rule.Name != "Mail provider" || !rule.Expected {
		t.Fatalf("rule identity = %+v", rule)
	}

	if len(rule.Match.CIDRs) != 3 || rule.Match.CIDRs[0] != "192.0.2.1/32" ||
		rule.Match.CIDRs[1] != "2001:db8::1/128" || rule.Match.CIDRs[2] != "192.0.2.0/24" {
		t.Fatalf("cidrs = %v", rule.Match.CIDRs)
	}

	if len(rule.Match.PTRSuffixes) != 1 || rule.Match.PTRSuffixes[0] != "mailer.example.net" {
		t.Fatalf("ptr = %v", rule.Match.PTRSuffixes)
	}

	if len(rule.Match.DKIMDomains) != 1 || rule.Match.DKIMDomains[0] != "example.com" {
		t.Fatalf("dkim = %v", rule.Match.DKIMDomains)
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()

	documents := map[string]string{
		"unknown rule field":  "- name: One\n  note: x\n  match:\n    asns: [1]\n",
		"unknown match field": "- name: One\n  match:\n    asn: [1]\n",
		"not a list":          "name: One\n",
		"missing name":        "- match:\n    asns: [1]\n",
		"blank name":          "- name: \" \"\n  match:\n    asns: [1]\n",
		"duplicate name": "- name: One\n  match:\n    asns: [1]\n" +
			"- name: \" One \"\n  match:\n    asns: [2]\n",
		"no match":           "- name: One\n  expected: true\n",
		"bad cidr":           "- name: One\n  match:\n    cidrs: [not-a-cidr]\n",
		"empty ptr":          "- name: One\n  match:\n    ptr_suffixes: [\"\"]\n",
		"zero asn":           "- name: One\n  match:\n    asns: [0]\n",
		"multiple documents": "- name: One\n  match:\n    asns: [1]\n---\n- name: Two\n  match:\n    asns: [2]\n",
	}
	targets := map[string]error{
		"missing name":       errNameRequired,
		"blank name":         errNameRequired,
		"duplicate name":     errDuplicateName,
		"no match":           errNoMatch,
		"empty ptr":          errEmptyMatchValue,
		"zero asn":           errASNNotPositive,
		"multiple documents": errMultipleDocuments,
	}

	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(document))
			if err == nil {
				t.Fatal("expected error")
			}

			target := targets[name]
			if target != nil && !errors.Is(err, target) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestClassifyCIDRAndPrecedence(t *testing.T) {
	t.Parallel()

	set := sampleSet(t)
	got := set.Classify(inputWith(mustAddr(t, "192.0.2.10"), func(in *Input) {
		in.ASOrg = "Other"
		in.PTR = "mx.example.com"
		in.PTRConfirmed = true
	}))
	want := newSource(
		"rule:Newsletter service",
		"Newsletter service",
		KindRule,
		"Newsletter service",
		true,
	)

	if got != want {
		t.Fatalf("cidr precedence = %+v", got)
	}

	laterOnly := mustParse(t, `
- name: Later
  match:
    cidrs: [198.51.100.0/24]
- name: Earlier
  match:
    cidrs: [198.51.100.0/24]
`)
	got = laterOnly.Classify(inputWith(mustAddr(t, "198.51.100.9"), nil))

	if got.Rule != "Later" {
		t.Fatalf("first rule = %+v", got)
	}
}

func TestClassifyPTR(t *testing.T) {
	t.Parallel()

	set := sampleSet(t)
	outside := mustAddr(t, "198.51.100.10")

	for _, ptr := range []string{"mailer.example.net", "Mailer.Example.NET.", "a.mailer.example.net"} {
		got := set.Classify(inputWith(outside, func(in *Input) {
			in.PTR = ptr
			in.PTRConfirmed = true
		}))
		if got.Rule != "Newsletter service" || !got.Expected {
			t.Fatalf("ptr %q = %+v", ptr, got)
		}
	}

	unconfirmed := set.Classify(inputWith(outside, func(in *Input) {
		in.PTR = "mailer.example.net"
	}))
	if unconfirmed.Kind == KindRule || unconfirmed.Kind == KindPTR {
		t.Fatalf("unconfirmed ptr used: %+v", unconfirmed)
	}

	notSuffix := set.Classify(inputWith(outside, func(in *Input) {
		in.PTR = "notmailer.example.net"
		in.PTRConfirmed = true
		in.ASN = 1
	}))
	if notSuffix.Kind == KindRule {
		t.Fatalf("partial label matched: %+v", notSuffix)
	}
}

func TestClassifyDKIMAndASN(t *testing.T) {
	t.Parallel()

	set := sampleSet(t)
	outside := mustAddr(t, "198.51.100.10")

	for _, domain := range []string{"example.com", "Example.COM.", "a.b.example.com"} {
		got := set.Classify(inputWith(outside, func(in *Input) {
			in.DKIMDomains = []string{"other.test", domain}
		}))
		if got.Rule != "Newsletter service" {
			t.Fatalf("dkim %q = %+v", domain, got)
		}
	}

	sibling := set.Classify(inputWith(outside, func(in *Input) {
		in.DKIMDomains = []string{"notexample.com", "example.com.evil"}
	}))
	if sibling.Kind == KindRule {
		t.Fatalf("dkim sibling matched: %+v", sibling)
	}

	byASN := set.Classify(inputWith(outside, func(in *Input) {
		in.ASN = 64500
	}))
	if byASN.Rule != "Newsletter service" || !byASN.Expected {
		t.Fatalf("asn = %+v", byASN)
	}

	unknownASN := set.Classify(inputWith(outside, func(in *Input) {
		in.ASN = 0
	}))
	if unknownASN.Kind == KindRule {
		t.Fatalf("asn 0 matched: %+v", unknownASN)
	}

	v6 := set.Classify(inputWith(mustAddr(t, "2001:db8::1"), nil))
	if v6.Rule != "Newsletter service" {
		t.Fatalf("ipv6 = %+v", v6)
	}
}

func TestClassifyFallbacks(t *testing.T) {
	t.Parallel()

	set := Empty()
	ip := mustAddr(t, "203.0.113.5")

	got := set.Classify(inputWith(ip, func(in *Input) {
		in.ASOrg = "  Acme Mail  "
		in.PTR = "mx.example.com"
		in.PTRConfirmed = true
	}))
	if got != newSource("as:acme mail", "Acme Mail", KindAS, "", false) {
		t.Fatalf("as = %+v", got)
	}

	got = set.Classify(inputWith(ip, func(in *Input) {
		in.PTR = "mx1.mail.example.com."
		in.PTRConfirmed = true
	}))
	if got != newSource("ptr:example.com", "example.com", KindPTR, "", false) {
		t.Fatalf("ptr = %+v", got)
	}

	got = set.Classify(inputWith(ip, func(in *Input) {
		in.PTR = "mx.example.com"
	}))
	if got.Kind != KindIP || got.Expected {
		t.Fatalf("unconfirmed ptr fallback = %+v", got)
	}

	got = set.Classify(inputWith(ip, func(in *Input) {
		in.PTR = "localhost"
		in.PTRConfirmed = true
	}))
	if got != newSource("ip:203.0.113.5", "203.0.113.5", KindIP, "", false) {
		t.Fatalf("non-registrable = %+v", got)
	}

	unexpected := mustParse(t, "- name: Shadow\n  match:\n    asns: [1]\n")

	got = unexpected.Classify(inputWith(ip, func(in *Input) {
		in.ASN = 1
		in.ASOrg = "Acme"
	}))
	if got.Kind != KindRule || got.Expected || got.Name != "Shadow" {
		t.Fatalf("unexpected rule = %+v", got)
	}
}

func TestClassifyNilSet(t *testing.T) {
	t.Parallel()

	var set *Set

	got := set.Classify(inputWith(mustAddr(t, "192.0.2.1"), nil))
	if got.Kind != KindIP {
		t.Fatalf("nil set = %+v", got)
	}
}

func sampleSet(t *testing.T) *Set {
	t.Helper()

	return mustParse(t, `
- name: Newsletter service
  expected: true
  match:
    cidrs: [192.0.2.0/24, "2001:db8::1"]
    ptr_suffixes: [mailer.example.net]
    dkim_domains: [example.com]
    asns: [64500]
- name: Later
  match:
    cidrs: [192.0.2.0/24]
`)
}

func mustParse(t *testing.T, document string) *Set {
	t.Helper()

	set, err := Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}

	return set
}

func mustAddr(t *testing.T, raw string) netip.Addr {
	t.Helper()

	addr, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatal(err)
	}

	return addr
}

func inputWith(ip netip.Addr, edit func(*Input)) Input {
	in := Input{
		IP:           ip,
		PTR:          "",
		ASOrg:        "",
		DKIMDomains:  nil,
		ASN:          0,
		PTRConfirmed: false,
	}
	if edit != nil {
		edit(&in)
	}

	return in
}
