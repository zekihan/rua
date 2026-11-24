// Package sources groups record source IPs into senders: user rules first,
// then AS organisation, then the registrable domain of a forward-confirmed
// PTR, then the IP itself. Grouping happens at query time.
package sources

import (
	"context"
	"errors"
	"net/netip"
)

var errNotImplemented = errors.New("not implemented")

// Kinds of source.
const (
	KindRule = "rule"
	KindAS   = "as"
	KindPTR  = "ptr"
	KindIP   = "ip"
)

// Rule is one entry of the SOURCES_FILE YAML list.
type Rule struct {
	Name     string `yaml:"name"`
	Match    Match  `yaml:"match"`
	Expected bool   `yaml:"expected"`
}

// Match lists the alternatives of a rule; any one matching is enough.
type Match struct {
	CIDRs       []string `yaml:"cidrs"`
	PTRSuffixes []string `yaml:"ptr_suffixes"`
	DKIMDomains []string `yaml:"dkim_domains"`
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
	rules []Rule
}

// Empty returns a Set with no rules.
func Empty() *Set { return &Set{} }

// Parse validates and parses a SOURCES_FILE document.
func Parse(data []byte) (*Set, error) {
	_ = data

	return nil, errNotImplemented
}

// Rules returns the parsed rules in file order.
func (s *Set) Rules() []Rule { return s.rules }

// Classify returns the source of in; the first matching rule wins.
func (s *Set) Classify(in Input) Source {
	_ = in

	return Source{}
}

// Loader keeps the current Set for a file and reloads it when the file
// changes. A file that fails to parse keeps the previous Set and is logged.
type Loader struct {
	set  *Set
	path string
}

// NewLoader loads path; an empty path yields a Loader that always returns
// Empty().
func NewLoader(path string) (*Loader, error) {
	return &Loader{path: path, set: Empty()}, nil
}

// Current returns the latest Set. Safe for concurrent use.
func (l *Loader) Current() *Set { return l.set }

// Run polls the file for changes until ctx is done.
func (l *Loader) Run(ctx context.Context) error {
	_ = ctx

	return errNotImplemented
}
