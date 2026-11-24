package demo

import (
	"math/rand/v2"
	"time"
)

type policy struct {
	p         string
	sp        string
	np        string
	adkim     string
	aspf      string
	fo        string
	testing   string
	discovery string
	pct       int
	bis       bool
}

type auth struct {
	method   string
	domain   string
	selector string
	scope    string
	result   string
}

type rec struct {
	ip           string
	headerFrom   string
	envelopeFrom string
	disposition  string
	dkim         string
	spf          string
	reason       string
	auths        []auth
	count        int
}

func publishedPolicy(
	disposition, subdomain, tree, dkimAlign, spfAlign, failure, testing, discovery string,
	pct int,
	bis bool,
) policy {
	return policy{
		p: disposition, sp: subdomain, np: tree, adkim: dkimAlign, aspf: spfAlign, fo: failure,
		testing: testing, discovery: discovery, pct: pct, bis: bis,
	}
}

func policyFor(domain string, dayIndex int, bis bool) policy {
	switch domain {
	case domainCom:
		return publishedPolicy(
			dispReject,
			dispQuarantine,
			"",
			alignRelaxed,
			alignRelaxed,
			foAll,
			"",
			"",
			fullPct,
			false,
		)
	case domainOrg:
		return publishedPolicy(
			dispQuarantine, dispQuarantine, dispQuarantine, alignRelaxed, alignStrict, foAll,
			testingOff, discoveryPSL, fullPct, bis,
		)
	default:
		return netPolicy(dayIndex)
	}
}

func netPolicy(dayIndex int) policy {
	published := publishedPolicy(
		dispNone, dispQuarantine, "", alignRelaxed, alignRelaxed, foNone, "", "", fullPct, false,
	)
	if dayIndex >= spanDays-partialPolicyDays {
		published.p = dispQuarantine
		published.pct = partialPct
	}

	if dayIndex >= spanDays-fullPolicyDays {
		published.pct = fullPct
	}

	return published
}

func dailyVolume(dayIndex int, when time.Time, rng *rand.Rand) int {
	volume := volumeFloor + dayIndex/volumeStep
	if when.Weekday() == time.Saturday || when.Weekday() == time.Sunday {
		volume = volume * weekendPercent / fullPct
	}

	if volume < 1 {
		volume = 1
	}

	jitter := jitterMin + rng.IntN(jitterSpan)

	return volume * jitter / fullPct
}

func recordsFor(domain string, published *policy, volume, dayIndex int, when time.Time) []rec {
	if domain == domainOrg {
		return orgRecords(domain, published, volume, dayIndex)
	}

	return mainRecords(domain, published, volume, dayIndex, when)
}

func mainRecords(domain string, published *policy, volume, dayIndex int, when time.Time) []rec {
	rows := []rec{
		providerRecord(domain, ipProvider4, atLeastOne(volume)),
		newsRecord(domain, atLeastOne(newsVolume(volume, when))),
		forwarderRecord(domain, dayIndex, atLeastOne(volume/forwardDivisor)),
	}

	rows = appendCRM(rows, domain, published, atLeastOne(volume/crmDivisor+1))
	if domain == domainCom {
		rows = append(rows, providerRecord(domain, ipProvider6, atLeastOne(volume/v6Divisor)))
		if dayIndex%noiseEvery == 0 {
			rows = append(rows, noiseRecord(domain, published, dayIndex))
		}
	}

	return appendSpoofer(rows, domain, published, volume, dayIndex)
}

func orgRecords(domain string, published *policy, volume, dayIndex int) []rec {
	count := atLeastOne(volume / orgDivisor)

	rows := []rec{
		providerRecord(domain, ipProvider4, count),
		providerRecord(domain, ipProvider6, atLeastOne(count/v6Divisor)),
	}
	if dayIndex%longEvery == 0 {
		rows = append(rows, forwarderRecord(domain, dayIndex, atLeastOne(count/forwardDivisor)))
	}

	return appendSpoofer(rows, domain, published, count, dayIndex)
}

func appendSpoofer(rows []rec, domain string, published *policy, volume, dayIndex int) []rec {
	if dayIndex < spanDays-spooferDays {
		return rows
	}

	return append(rows,
		spooferRecord(domain, published, ipSpoof4, atLeastOne(volume)),
		spooferRecord(domain, published, ipSpoof6, atLeastOne(volume/v6Divisor)),
	)
}

func appendCRM(rows []rec, domain string, published *policy, count int) []rec {
	if published.p == dispQuarantine && published.pct == partialPct {
		first := count / halfSplit
		second := count - first

		return append(rows,
			crmRecord(domain, published, atLeastOne(first), false),
			crmRecord(domain, published, atLeastOne(second), true),
		)
	}

	return append(rows, crmRecord(domain, published, count, false))
}

func newsVolume(volume int, when time.Time) int {
	if when.Weekday() == time.Tuesday || when.Weekday() == time.Thursday {
		return volume * newsBurstNum / newsBurstShare
	}

	return volume / newsShare
}

func atLeastOne(count int) int {
	if count < 1 {
		return 1
	}

	return count
}

func providerRecord(domain, ip string, count int) rec {
	return rec{
		count: count, ip: ip, headerFrom: domain, envelopeFrom: domain,
		disposition: dispNone, dkim: resultPass, spf: resultPass, reason: "",
		auths: []auth{
			dkimResult(domain, selectorMail, resultPass),
			dkimResult(dkimProvider, selectorProvider, resultPass),
			spfResult(domain, resultPass),
		},
	}
}

func newsRecord(domain string, count int) rec {
	signing := newsDKIM(domain)

	return rec{
		count: count, ip: ipNews4, headerFrom: domain, envelopeFrom: envelopeNews,
		disposition: dispNone, dkim: resultPass, spf: resultFail, reason: "",
		auths: []auth{
			dkimResult(signing, selectorNews, resultPass),
			spfResult(signing, resultFail),
		},
	}
}

func newsDKIM(domain string) string {
	if domain == domainNet {
		return dkimNewsNet
	}

	return dkimNewsCom
}

func crmRecord(domain string, published *policy, count int, unenforced bool) rec {
	return rec{
		count:        count,
		ip:           ipCRM,
		headerFrom:   domain,
		envelopeFrom: envelopeCRM,
		disposition: failDisposition(
			published,
			unenforced,
		),
		dkim:   resultFail,
		spf:    resultFail,
		reason: "",
		auths: []auth{
			dkimResult(dkimCRM, selectorCRM, resultFail),
			spfResult(spfCRM, resultPass),
		},
	}
}

func failDisposition(published *policy, unenforced bool) string {
	if unenforced {
		return dispNone
	}

	switch published.p {
	case dispReject:
		return dispReject
	case dispQuarantine:
		return dispQuarantine
	default:
		return dispNone
	}
}

func forwarderRecord(domain string, dayIndex, count int) rec {
	switch dayIndex % forwarderKinds {
	case 0:
		return forwarder(ipList4, domain, count, "", spfList)
	case 1:
		return forwarder(ipForward4, domain, count, reasonForwarded, spfForward)
	default:
		return forwarder(ipList6, domain, count, reasonList, spfList)
	}
}

func forwarder(ip, domain string, count int, reason, spfDomain string) rec {
	return rec{
		count: count, ip: ip, headerFrom: domain, envelopeFrom: spfDomain,
		disposition: dispNone, dkim: resultFail, spf: resultFail, reason: reason,
		auths: []auth{
			dkimResult(domain, selectorList, resultPass),
			spfResult(spfDomain, resultFail),
		},
	}
}

func spooferRecord(domain string, published *policy, ip string, count int) rec {
	return rec{
		count:        count,
		ip:           ip,
		headerFrom:   domain,
		envelopeFrom: domain,
		disposition: failDisposition(
			published,
			false,
		),
		dkim:   resultFail,
		spf:    resultFail,
		reason: "",
		auths: []auth{
			dkimResult(domain, selectorMail, resultFail),
			spfResult(domain, resultFail),
		},
	}
}

func noiseRecord(domain string, published *policy, dayIndex int) rec {
	slot := (dayIndex / noiseEvery) % len(noiseAddresses)

	return rec{
		count:        1,
		ip:           noiseAddresses[slot],
		headerFrom:   domain,
		envelopeFrom: envelopeNoise,
		disposition: failDisposition(
			published,
			false,
		),
		dkim:   resultFail,
		spf:    resultFail,
		reason: "",
		auths: []auth{
			dkimResult(domain, selectorMail, resultFail),
			spfResult(envelopeNoise, resultFail),
		},
	}
}

func dkimResult(domain, selector, result string) auth {
	return auth{method: methodDKIM, domain: domain, selector: selector, scope: "", result: result}
}

func spfResult(domain, result string) auth {
	return auth{method: methodSPF, domain: domain, selector: "", scope: scopeMFrom, result: result}
}
