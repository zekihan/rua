package demo

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/zekihan/rua/internal/ingest"
)

func (gen *generator) tlsReports(ctx context.Context, pipe *ingest.Pipeline) error {
	for dayIndex := range spanDays {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("generate TLS reports: %w", err)
		}

		err = gen.tlsDay(ctx, pipe, dayIndex)
		if err != nil {
			return err
		}
	}

	return gen.noPolicy(ctx, pipe)
}

func (gen *generator) tlsDay(ctx context.Context, pipe *ingest.Pipeline, dayIndex int) error {
	for index := range gen.reporters {
		rep := &gen.reporters[index]
		if !rep.tls || dayIndex < rep.first || dayIndex > rep.last {
			continue
		}

		err := gen.oneTLS(ctx, pipe, rep, dayIndex)
		if err != nil {
			return err
		}
	}

	return nil
}

func (gen *generator) oneTLS(
	ctx context.Context, pipe *ingest.Pipeline, rep *reporter, dayIndex int,
) error {
	begin, end, ok := gen.window(dayIndex, rep.offset, int(dayLength/time.Hour))
	if !ok {
		return nil
	}

	success, failed := tlsCounts(dayIndex, begin)
	gen.buf = appendTLS(
		gen.buf[:0],
		rep,
		domainCom,
		begin,
		end,
		success,
		failed,
		incidentDay(dayIndex),
	)

	err := gen.ingest(ctx, pipe, gen.buf, end)
	if err != nil {
		return fmt.Errorf("ingest %s TLS report: %w", rep.name, err)
	}

	return nil
}

func incidentDay(dayIndex int) bool {
	start := spanDays - incidentAgo - incidentLead

	return dayIndex >= start && dayIndex < start+incidentLength
}

func tlsCounts(dayIndex int, when time.Time) (int, int) {
	volume := volumeFloor + dayIndex/volumeStep
	if when.Weekday() == time.Saturday || when.Weekday() == time.Sunday {
		volume = volume * weekendPercent / fullPct
	}

	if volume < 1 {
		volume = 1
	}

	if incidentDay(dayIndex) {
		return volume, incidentFetch + incidentExpired
	}

	return volume * tlsSuccessScale, 0
}

func (gen *generator) noPolicy(ctx context.Context, pipe *ingest.Pipeline) error {
	var north *reporter

	for index := range gen.reporters {
		if gen.reporters[index].prefix == prefixNorthwind {
			north = &gen.reporters[index]

			break
		}
	}

	if north == nil {
		return errNilPipeline
	}

	begin, end, ok := gen.window(zipDay, north.offset, int(dayLength/time.Hour))
	if !ok {
		return nil
	}

	gen.buf = appendNoPolicy(gen.buf[:0], north, begin, end)

	err := gen.ingest(ctx, pipe, gen.buf, end)
	if err != nil {
		return fmt.Errorf("ingest no-policy TLS report: %w", err)
	}

	return nil
}

func appendTLS(
	dst []byte,
	rep *reporter,
	domain string,
	begin, end time.Time,
	success, failed int,
	incident bool,
) []byte {
	reportID := rep.prefix + "-tls-" + domain + "-" + strconv.FormatInt(begin.Unix(), base10)
	dst = appendTLSHeader(dst, rep, reportID, begin, end)
	dst = append(dst, `{"policy":{"policy-type":"`...)
	dst = append(dst, policySTS...)
	dst = append(dst, `","policy-domain":"`...)
	dst = append(dst, domain...)
	dst = append(dst, `","policy-string":["`...)
	dst = append(dst, stsVersion...)
	dst = append(dst, `","`...)
	dst = append(dst, stsMode...)
	dst = append(dst, `","mx: `...)
	dst = append(dst, mxSuffix...)
	dst = append(dst, domain...)
	dst = append(dst, `","`...)
	dst = append(dst, stsMaxAge...)
	dst = append(dst, `"],"mx-host":["`...)
	dst = append(dst, mxSuffix...)
	dst = append(dst, domain...)
	dst = append(dst, `"]},"summary":{"total-successful-session-count":`...)
	dst = strconv.AppendInt(dst, int64(success), base10)
	dst = append(dst, `,"total-failure-session-count":`...)
	dst = strconv.AppendInt(dst, int64(failed), base10)

	dst = append(dst, '}')
	if incident {
		dst = appendIncident(dst, domain)
	}

	dst = append(dst, "}]}"...)

	return dst
}

func appendTLSHeader(dst []byte, rep *reporter, reportID string, begin, end time.Time) []byte {
	dst = append(dst, `{"organization-name":"`...)
	dst = append(dst, rep.name...)
	dst = append(dst, `","contact-info":"`...)
	dst = append(dst, rep.email...)
	dst = append(dst, `","report-id":"`...)
	dst = append(dst, reportID...)
	dst = append(dst, `","date-range":{"start-datetime":"`...)
	dst = append(dst, begin.Format(time.RFC3339)...)
	dst = append(dst, `","end-datetime":"`...)
	dst = append(dst, end.Format(time.RFC3339)...)
	dst = append(dst, `"},"policies":[`...)

	return dst
}

func appendIncident(dst []byte, domain string) []byte {
	dst = append(dst, `,"failure-details":[`...)
	dst = appendFailure(dst, failureFetch, ipProvider4, domain, incidentFetch, "")
	dst = append(dst, ',')
	dst = appendFailure(dst, failureExpired, ipProvider6, domain, incidentExpired, "expired")
	dst = append(dst, ']')

	return dst
}

func appendFailure(dst []byte, result, ip, domain string, count int, code string) []byte {
	dst = append(dst, `{"result-type":"`...)
	dst = append(dst, result...)
	dst = append(dst, `","sending-mta-ip":"`...)
	dst = append(dst, ip...)
	dst = append(dst, `","receiving-mx-hostname":"`...)
	dst = append(dst, mxSuffix...)
	dst = append(dst, domain...)
	dst = append(dst, `","failed-session-count":`...)

	dst = strconv.AppendInt(dst, int64(count), base10)
	if code != "" {
		dst = append(dst, `,"failure-reason-code":"`...)
		dst = append(dst, code...)
		dst = append(dst, '"')
	}

	dst = append(dst, '}')

	return dst
}

func appendNoPolicy(dst []byte, rep *reporter, begin, end time.Time) []byte {
	reportID := rep.prefix + "-tls-np-" + domainNet + "-" + strconv.FormatInt(begin.Unix(), base10)
	dst = appendTLSHeader(dst, rep, reportID, begin, end)
	dst = append(dst, `{"policy":{"policy-type":"`...)
	dst = append(dst, policyNone...)
	dst = append(dst, `","policy-domain":"`...)
	dst = append(dst, domainNet...)
	dst = append(dst, `","mx-host":["`...)
	dst = append(dst, mxSuffix...)
	dst = append(dst, domainNet...)
	dst = append(dst, `"]},"summary":{"total-successful-session-count":`...)
	dst = strconv.AppendInt(dst, noPolicySuccess, base10)
	dst = append(dst, `,"total-failure-session-count":`...)
	dst = strconv.AppendInt(dst, noPolicyFailures, base10)
	dst = append(dst, `},"failure-details":[`...)
	dst = appendFailure(dst, failureValidate, ipNews4, domainNet, noPolicyFailures, "")
	dst = append(dst, "]}]}"...)

	return dst
}
