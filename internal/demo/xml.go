package demo

import "strconv"

func appendAggregate(dst []byte, meta *aggregateMeta, rows []rec) []byte {
	dst = append(dst, `<?xml version="1.0" encoding="UTF-8"?>`...)
	if meta.policy.bis {
		dst = append(dst, `<feedback xmlns="urn:ietf:params:xml:ns:dmarc-2.0">`...)
	} else {
		dst = append(dst, "<feedback>"...)
	}

	dst = append(dst, "<report_metadata><org_name>"...)
	dst = appendText(dst, meta.org)
	dst = append(dst, "</org_name><email>"...)
	dst = appendText(dst, meta.email)
	dst = append(dst, "</email><report_id>"...)
	dst = appendText(dst, meta.reportID)
	dst = append(dst, "</report_id><date_range><begin>"...)
	dst = strconv.AppendInt(dst, meta.begin, base10)
	dst = append(dst, "</begin><end>"...)
	dst = strconv.AppendInt(dst, meta.end, base10)
	dst = append(dst, "</end></date_range></report_metadata>"...)

	dst = appendPolicy(dst, meta.domain, &meta.policy)
	for index := range rows {
		dst = appendRecord(dst, &rows[index])
	}

	dst = append(dst, "</feedback>"...)

	return dst
}

func appendPolicy(dst []byte, domain string, published *policy) []byte {
	dst = append(dst, "<policy_published>"...)
	dst = appendTag(dst, "domain", domain)
	dst = appendTag(dst, "adkim", published.adkim)
	dst = appendTag(dst, "aspf", published.aspf)
	dst = appendTag(dst, "p", published.p)

	dst = appendTag(dst, "sp", published.sp)
	if published.np != "" {
		dst = appendTag(dst, "np", published.np)
	}

	if !published.bis {
		dst = appendIntTag(dst, "pct", published.pct)
	}

	if published.fo != "" {
		dst = appendTag(dst, "fo", published.fo)
	}

	if published.testing != "" {
		dst = appendTag(dst, "testing", published.testing)
	}

	if published.discovery != "" {
		dst = appendTag(dst, "discovery_method", published.discovery)
	}

	dst = append(dst, "</policy_published>"...)

	return dst
}

func appendRecord(dst []byte, row *rec) []byte {
	dst = append(dst, "<record><row><source_ip>"...)
	dst = append(dst, row.ip...)
	dst = append(dst, "</source_ip><count>"...)
	dst = strconv.AppendInt(dst, int64(row.count), base10)
	dst = append(dst, "</count><policy_evaluated><disposition>"...)
	dst = append(dst, row.disposition...)
	dst = append(dst, "</disposition><dkim>"...)
	dst = append(dst, row.dkim...)
	dst = append(dst, "</dkim><spf>"...)
	dst = append(dst, row.spf...)
	dst = append(dst, "</spf>"...)
	dst = appendReason(dst, row.reason)
	dst = append(dst, "</policy_evaluated></row><identifiers><header_from>"...)
	dst = append(dst, row.headerFrom...)
	dst = append(dst, "</header_from><envelope_from>"...)
	dst = append(dst, row.envelopeFrom...)

	dst = append(dst, "</envelope_from></identifiers><auth_results>"...)
	for index := range row.auths {
		dst = appendAuth(dst, &row.auths[index])
	}

	dst = append(dst, "</auth_results></record>"...)

	return dst
}

func appendReason(dst []byte, reason string) []byte {
	if reason == "" {
		return dst
	}

	dst = append(dst, "<reason><type>"...)
	dst = append(dst, reason...)
	dst = append(dst, "</type><comment>"...)
	dst = appendText(dst, reasonComment(reason))
	dst = append(dst, "</comment></reason>"...)

	return dst
}

func reasonComment(reason string) string {
	switch reason {
	case reasonForwarded:
		return "relayed by an intermediate forwarder"
	case reasonList:
		return "mailing list expansion"
	default:
		return "policy override"
	}
}

func appendAuth(dst []byte, item *auth) []byte {
	dst = append(dst, '<')
	dst = append(dst, item.method...)
	dst = append(dst, '>')

	dst = appendTag(dst, "domain", item.domain)
	if item.selector != "" {
		dst = appendTag(dst, "selector", item.selector)
	}

	if item.scope != "" {
		dst = appendTag(dst, "scope", item.scope)
	}

	dst = appendTag(dst, "result", item.result)
	dst = append(dst, '<', '/')
	dst = append(dst, item.method...)
	dst = append(dst, '>')

	return dst
}

func appendTag(dst []byte, tag, value string) []byte {
	dst = append(dst, '<')
	dst = append(dst, tag...)
	dst = append(dst, '>')
	dst = appendText(dst, value)
	dst = append(dst, '<', '/')
	dst = append(dst, tag...)
	dst = append(dst, '>')

	return dst
}

func appendIntTag(dst []byte, tag string, value int) []byte {
	dst = append(dst, '<')
	dst = append(dst, tag...)
	dst = append(dst, '>')
	dst = strconv.AppendInt(dst, int64(value), base10)
	dst = append(dst, '<', '/')
	dst = append(dst, tag...)
	dst = append(dst, '>')

	return dst
}

func appendText(dst []byte, value string) []byte {
	for index := range len(value) {
		switch value[index] {
		case '&':
			dst = append(dst, "&amp;"...)
		case '<':
			dst = append(dst, "&lt;"...)
		case '>':
			dst = append(dst, "&gt;"...)
		default:
			dst = append(dst, value[index])
		}
	}

	return dst
}
