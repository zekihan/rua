package enrich

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxTextBytes    = 255
	cymruFieldCount = 5
	ipv4Octets      = 4
	ipv6Octets      = 16
	lowNibbleMask   = 0x0f
	highNibbleShift = 4
	origin4Zone     = "origin.asn.cymru.com"
	origin6Zone     = "origin6.asn.cymru.com"
	asnZone         = ".asn.cymru.com"
	hexDigits       = "0123456789abcdef"
)

var errCymruFormat = errors.New("invalid cymru response")

type resolved struct {
	ptr       string
	org       string
	problem   string
	asn       uint32
	confirmed bool
}

func failure(problem string) resolved {
	return resolved{
		ptr:       "",
		org:       "",
		problem:   sanitize(problem),
		asn:       0,
		confirmed: false,
	}
}

func (worker *Worker) resolve(ctx context.Context, addr netip.Addr) (resolved, error) {
	var none resolved

	ptr, confirmed, ptrErr := worker.lookupPTR(ctx, addr)
	if ctx.Err() != nil {
		return none, fmt.Errorf("resolve: %w", ctx.Err())
	}

	asn, org, asnErr := worker.lookupASN(ctx, addr)
	if ctx.Err() != nil {
		return none, fmt.Errorf("resolve: %w", ctx.Err())
	}

	return resolved{
		ptr:       ptr,
		org:       org,
		problem:   combineProblems(ptrErr, asnErr),
		asn:       asn,
		confirmed: confirmed,
	}, nil
}

func combineProblems(errs ...error) string {
	parts := make([]string, 0, len(errs))

	for _, err := range errs {
		if err == nil {
			continue
		}

		parts = append(parts, err.Error())
	}

	if len(parts) == 0 {
		return ""
	}

	return sanitize(strings.Join(parts, "; "))
}

func (worker *Worker) lookupPTR(ctx context.Context, addr netip.Addr) (string, bool, error) {
	names, err := worker.lookupAddr(ctx, addr.String())
	if err != nil {
		return "", false, err
	}

	if len(names) == 0 {
		return "", false, nil
	}

	name := sanitize(strings.TrimSuffix(strings.TrimSpace(names[0]), "."))
	if name == "" {
		return "", false, nil
	}

	confirmed, err := worker.confirmPTR(ctx, name, addr)
	if err != nil {
		return name, false, err
	}

	return name, confirmed, nil
}

func (worker *Worker) lookupAddr(ctx context.Context, addr string) ([]string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	names, err := worker.resolver.LookupAddr(lookupCtx, addr)

	return dnsValues(ctx, "lookup ptr", names, err)
}

func (worker *Worker) confirmPTR(ctx context.Context, name string, addr netip.Addr) (bool, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	found, err := worker.resolver.LookupIPAddr(lookupCtx, name)
	if err != nil {
		err = dnsError(ctx, "confirm ptr", err)
		if err != nil {
			return false, err
		}

		return false, nil
	}

	return containsIP(found, addr), nil
}

func (worker *Worker) lookupASN(ctx context.Context, addr netip.Addr) (uint32, string, error) {
	records, err := worker.lookupTXT(ctx, cymruOriginName(addr))
	if err != nil {
		return 0, "", err
	}

	record := firstRecord(records)
	if record == "" {
		return 0, "", nil
	}

	asn, err := parseOriginASN(record)
	if err != nil {
		return 0, "", err
	}

	if asn == 0 {
		return 0, "", nil
	}

	org, err := worker.asOrg(ctx, asn)
	if err != nil {
		return asn, "", err
	}

	return asn, org, nil
}

func (worker *Worker) asOrg(ctx context.Context, asn uint32) (string, error) {
	org, ok := worker.asnOrgs[asn]
	if ok {
		return org, nil
	}

	org, err := worker.lookupASOrg(ctx, asn)
	if err != nil {
		return "", err
	}

	worker.asnOrgs[asn] = org

	return org, nil
}

func (worker *Worker) lookupASOrg(ctx context.Context, asn uint32) (string, error) {
	records, err := worker.lookupTXT(ctx, asName(asn))
	if err != nil {
		return "", err
	}

	record := firstRecord(records)
	if record == "" {
		return "", nil
	}

	return parseASOrg(record)
}

func (worker *Worker) lookupTXT(ctx context.Context, name string) ([]string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	records, err := worker.resolver.LookupTXT(lookupCtx, name)

	return dnsValues(ctx, "txt "+name, records, err)
}

func dnsValues(
	ctx context.Context,
	operation string,
	values []string,
	err error,
) ([]string, error) {
	if err == nil {
		return values, nil
	}

	err = dnsError(ctx, operation, err)
	if err != nil {
		return nil, err
	}

	return []string{}, nil
}

func dnsError(ctx context.Context, operation string, err error) error {
	parent := ctx.Err()
	if parent != nil {
		return fmt.Errorf("%s: %w", operation, parent)
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil
	}

	return fmt.Errorf("%s: %w", operation, err)
}

func containsIP(addrs []net.IPAddr, want netip.Addr) bool {
	want = want.Unmap()

	for _, addr := range addrs {
		got, valid := netip.AddrFromSlice(addr.IP)
		if !valid {
			continue
		}

		if got.Unmap() == want {
			return true
		}
	}

	return false
}

func cymruOriginName(addr netip.Addr) string {
	addr = addr.Unmap()
	if addr.Is4() {
		return ipv4Origin(addr)
	}

	return ipv6Origin(addr)
}

func ipv4Origin(addr netip.Addr) string {
	raw := addr.As4()
	parts := make([]string, 0, ipv4Octets+1)

	for index := len(raw) - 1; index >= 0; index-- {
		parts = append(parts, strconv.FormatUint(uint64(raw[index]), 10))
	}

	parts = append(parts, origin4Zone)

	return strings.Join(parts, ".")
}

func ipv6Origin(addr netip.Addr) string {
	raw := addr.As16()

	var builder strings.Builder

	builder.Grow(ipv6Octets*highNibbleShift + len(origin6Zone))

	for index := len(raw) - 1; index >= 0; index-- {
		builder.WriteByte(hexDigits[raw[index]&lowNibbleMask])
		builder.WriteByte('.')
		builder.WriteByte(hexDigits[raw[index]>>highNibbleShift])
		builder.WriteByte('.')
	}

	builder.WriteString(origin6Zone)

	return builder.String()
}

func asName(asn uint32) string {
	return "AS" + strconv.FormatUint(uint64(asn), 10) + asnZone
}

func firstRecord(records []string) string {
	for _, record := range records {
		record = strings.TrimSpace(strings.Trim(strings.TrimSpace(record), `"`))
		if record != "" {
			return record
		}
	}

	return ""
}

func parseOriginASN(record string) (uint32, error) {
	fields := splitPipe(record)
	if len(fields) < cymruFieldCount || fields[0] == "" {
		return 0, fmt.Errorf("%w: %q", errCymruFormat, record)
	}

	tokens := strings.Fields(fields[0])

	value, err := strconv.ParseUint(tokens[0], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errCymruFormat, err)
	}

	return uint32(value), nil
}

func parseASOrg(record string) (string, error) {
	fields := splitPipe(record)
	if len(fields) < cymruFieldCount {
		return "", fmt.Errorf("%w: %q", errCymruFormat, record)
	}

	name := fields[cymruFieldCount-1]
	country := fields[1]

	if country != "" {
		name = strings.TrimSuffix(name, ", "+country)
	}

	return sanitize(name), nil
}

func splitPipe(record string) []string {
	parts := strings.SplitN(record, "|", cymruFieldCount)
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}

	return parts
}

func sanitize(value string) string {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}

	value = strings.ReplaceAll(value, "\x00", "")
	value = strings.TrimSpace(value)

	if len(value) <= maxTextBytes {
		return value
	}

	value = value[:maxTextBytes]
	for !utf8.ValidString(value) && value != "" {
		value = value[:len(value)-1]
	}

	return value
}
