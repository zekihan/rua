// Package sources groups record source IPs into senders: user rules first,
// then AS organization, then the registrable domain of a forward-confirmed
// PTR, then the IP itself. Grouping happens at query time.
package sources

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/net/publicsuffix"
)

// Kinds of source.
const (
	KindRule = "rule"
	KindAS   = "as"
	KindPTR  = "ptr"
	KindIP   = "ip"
)

const (
	keyRule = KindRule + ":"
	keyAS   = KindAS + ":"
	keyPTR  = KindPTR + ":"
	keyIP   = KindIP + ":"

	ipv4PrefixBits = 32
	ipv6PrefixBits = 128
)

var (
	errNameRequired      = errors.New("name is required")
	errDuplicateName     = errors.New("duplicate name")
	errNoMatch           = errors.New("match requires a cidr, ptr suffix, dkim domain or asn")
	errASNNotPositive    = errors.New("asn must be greater than zero")
	errEmptyMatchValue   = errors.New("empty match value")
	errMultipleDocuments = errors.New("multiple documents")
)

// Rule is one entry of the SOURCES_FILE YAML list.
type Rule struct {
	Name     string `yaml:"name"`
	Match    Match  `yaml:"match"`
	Expected bool   `yaml:"expected"`
}

// Match lists the alternatives of a rule; any one matching is enough.
type Match struct {
	CIDRs []string `yaml:"cidrs"`
	// SOURCES_FILE keys are snake_case in the documented file format.
	PTRSuffixes []string `yaml:"ptr_suffixes"` //nolint:tagliatelle // File format is snake_case.
	DKIMDomains []string `yaml:"dkim_domains"` //nolint:tagliatelle // File format is snake_case.
	ASNs        []uint32 `yaml:"asns"`
}

// Input is what is known about one source IP within a group of records.
type Input struct {
	IP    netip.Addr
	PTR   string // empty when unknown
	ASOrg string
	// DKIMDomains are the signing domains seen in its auth_results.
	DKIMDomains  []string
	ASN          uint32 // 0 when unknown
	PTRConfirmed bool
}

// Source is the sender an IP belongs to.
type Source struct {
	// Key is stable and URL-safe after escaping: "rule:<name>",
	// "as:<lower-cased org>", "ptr:<registrable domain>" or "ip:<addr>".
	Key      string
	Name     string // display name
	Kind     string
	Rule     string // matching rule name, empty unless Kind is rule
	Expected bool
}

// Set is an immutable, parsed rule list.
type Set struct {
	rules    []Rule
	compiled []compiledRule
}

type compiledRule struct {
	name     string
	cidrs    []netip.Prefix
	ptrs     []string
	dkims    []string
	asns     []uint32
	expected bool
}

// Empty returns a Set with no rules.
func Empty() *Set { return new(Set) }

// Parse validates and parses a SOURCES_FILE document.
func Parse(data []byte) (*Set, error) {
	rules, err := decodeRules(data)
	if err != nil {
		return nil, err
	}

	if len(rules) == 0 {
		return Empty(), nil
	}

	set := &Set{
		rules:    make([]Rule, 0, len(rules)),
		compiled: make([]compiledRule, 0, len(rules)),
	}
	seen := make(map[string]struct{}, len(rules))

	for index := range rules {
		compiled, compileErr := compileRule(&rules[index], index+1)
		if compileErr != nil {
			return nil, compileErr
		}

		if _, ok := seen[rules[index].Name]; ok {
			return nil, ruleError(rules[index].Name, errDuplicateName)
		}

		seen[rules[index].Name] = struct{}{}
		set.rules = append(set.rules, rules[index])
		set.compiled = append(set.compiled, compiled)
	}

	return set, nil
}

func decodeRules(data []byte) ([]Rule, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return []Rule{}, nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var rules []Rule

	err := decoder.Decode(&rules)
	if errors.Is(err, io.EOF) {
		return []Rule{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("parse sources: %w", err)
	}

	var extra yaml.Node

	err = decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return rules, nil
	}

	if err != nil {
		return nil, fmt.Errorf("parse sources: %w", err)
	}

	return nil, fmt.Errorf("parse sources: %w", errMultipleDocuments)
}

func compileRule(rule *Rule, position int) (compiledRule, error) {
	var none compiledRule

	name := strings.TrimSpace(rule.Name)
	if name == "" {
		return none, ruleIndexError(position, errNameRequired)
	}

	cidrs, cidrText, err := compileCIDRs(name, rule.Match.CIDRs)
	if err != nil {
		return none, err
	}

	ptrs, ptrText, err := compileDomains(name, rule.Match.PTRSuffixes)
	if err != nil {
		return none, err
	}

	dkims, dkimText, err := compileDomains(name, rule.Match.DKIMDomains)
	if err != nil {
		return none, err
	}

	asns, err := compileASNs(name, rule.Match.ASNs)
	if err != nil {
		return none, err
	}

	compiled := compiledRule{
		name:     name,
		cidrs:    cidrs,
		ptrs:     ptrs,
		dkims:    dkims,
		asns:     asns,
		expected: rule.Expected,
	}
	if !compiled.hasMatch() {
		return none, ruleError(name, errNoMatch)
	}

	rule.Name = name
	rule.Match.CIDRs = cidrText
	rule.Match.PTRSuffixes = ptrText
	rule.Match.DKIMDomains = dkimText

	return compiled, nil
}

func (rule *compiledRule) hasMatch() bool {
	return len(rule.cidrs) > 0 || len(rule.ptrs) > 0 || len(rule.dkims) > 0 || len(rule.asns) > 0
}

func compileCIDRs(name string, raw []string) ([]netip.Prefix, []string, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}

	prefixes := make([]netip.Prefix, 0, len(raw))
	texts := make([]string, 0, len(raw))

	for _, item := range raw {
		prefix, err := parsePrefix(item)
		if err != nil {
			return nil, nil, ruleError(name, fmt.Errorf("%w: %q", err, item))
		}

		prefixes = append(prefixes, prefix)
		texts = append(texts, prefix.String())
	}

	return prefixes, texts, nil
}

func parsePrefix(raw string) (netip.Prefix, error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return netip.Prefix{}, errEmptyMatchValue
	}

	prefix, prefixErr := netip.ParsePrefix(token)
	if prefixErr == nil {
		return prefix, nil
	}

	addr, addrErr := netip.ParseAddr(token)
	if addrErr != nil {
		return netip.Prefix{}, fmt.Errorf("invalid cidr: %w", prefixErr)
	}

	bits := ipv6PrefixBits
	if addr.Is4() {
		bits = ipv4PrefixBits
	}

	prefix = netip.PrefixFrom(addr.WithZone(""), bits)
	if !prefix.IsValid() {
		return netip.Prefix{}, fmt.Errorf("invalid cidr: %w", prefixErr)
	}

	return prefix, nil
}

func compileDomains(name string, raw []string) ([]string, []string, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}

	domains := make([]string, 0, len(raw))

	for _, item := range raw {
		domain, err := normalizeDomain(item)
		if err != nil {
			return nil, nil, ruleError(name, fmt.Errorf("%w: %q", err, item))
		}

		domains = append(domains, domain)
	}

	return domains, slices.Clone(domains), nil
}

func normalizeDomain(raw string) (string, error) {
	domain := normalizeHost(raw)
	if domain == "" {
		return "", errEmptyMatchValue
	}

	return domain, nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimRight(host, ".")

	return strings.ToLower(host)
}

func compileASNs(name string, raw []uint32) ([]uint32, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	asns := make([]uint32, 0, len(raw))

	for _, asn := range raw {
		if asn == 0 {
			return nil, ruleError(name, errASNNotPositive)
		}

		asns = append(asns, asn)
	}

	return asns, nil
}

func ruleError(name string, err error) error {
	return fmt.Errorf("parse sources: rule %q: %w", name, err)
}

func ruleIndexError(position int, err error) error {
	return fmt.Errorf("parse sources: rule %d: %w", position, err)
}

// Rules returns the parsed rules in file order.
func (set *Set) Rules() []Rule {
	if set == nil {
		return nil
	}

	return slices.Clone(set.rules)
}

// Classify returns the source of in; the first matching rule wins.
//
//nolint:gocritic // hugeParam: the exported signature takes Input by value.
func (set *Set) Classify(in Input) Source {
	if match, ok := set.ruleMatch(&in); ok {
		return match
	}

	return fallback(&in)
}

func (set *Set) ruleMatch(in *Input) (Source, bool) {
	var none Source

	if set == nil {
		return none, false
	}

	for index := range set.compiled {
		rule := &set.compiled[index]
		if !rule.matches(in) {
			continue
		}

		return Source{
			Key:      keyRule + rule.name,
			Name:     rule.name,
			Kind:     KindRule,
			Rule:     rule.name,
			Expected: rule.expected,
		}, true
	}

	return none, false
}

func (rule *compiledRule) matches(in *Input) bool {
	if prefixContains(rule.cidrs, in.IP) || ptrMatches(rule.ptrs, in) {
		return true
	}

	return dkimMatches(rule.dkims, in.DKIMDomains) || asnMatches(rule.asns, in.ASN)
}

func prefixContains(prefixes []netip.Prefix, addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}

	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

func ptrMatches(suffixes []string, in *Input) bool {
	if !in.PTRConfirmed {
		return false
	}

	return domainMatches(suffixes, in.PTR)
}

func dkimMatches(suffixes, domains []string) bool {
	for _, domain := range domains {
		if domainMatches(suffixes, domain) {
			return true
		}
	}

	return false
}

func domainMatches(suffixes []string, host string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}

	for _, suffix := range suffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}

	return false
}

func asnMatches(asns []uint32, asn uint32) bool {
	return asn > 0 && slices.Contains(asns, asn)
}

func fallback(in *Input) Source {
	org := strings.TrimSpace(in.ASOrg)
	if org != "" {
		return newSource(keyAS+strings.ToLower(org), org, KindAS, "", false)
	}

	if in.PTRConfirmed {
		if domain, ok := registrableDomain(in.PTR); ok {
			return newSource(keyPTR+domain, domain, KindPTR, "", false)
		}
	}

	addr := in.IP.String()

	return newSource(keyIP+addr, addr, KindIP, "", false)
}

func newSource(key, name, kind, rule string, expected bool) Source {
	return Source{Key: key, Name: name, Kind: kind, Rule: rule, Expected: expected}
}

func registrableDomain(ptr string) (string, bool) {
	domain := normalizeHost(ptr)
	if domain == "" {
		return "", false
	}

	base, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return "", false
	}

	return base, true
}
