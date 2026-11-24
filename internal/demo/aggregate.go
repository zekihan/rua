package demo

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/zekihan/rua/internal/ingest"
)

type aggregateMeta struct {
	org      string
	email    string
	reportID string
	domain   string
	policy   policy
	begin    int64
	end      int64
}

type copyKind int

const (
	copyNone copyKind = iota
	copyGzip
	copyZip
	copyMIME
	copyConflict
)

func (gen *generator) aggregates(ctx context.Context, pipe *ingest.Pipeline) error {
	for dayIndex := range spanDays {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("generate aggregate reports: %w", err)
		}

		err = gen.aggregateDay(ctx, pipe, dayIndex)
		if err != nil {
			return err
		}
	}

	return nil
}

func (gen *generator) aggregateDay(ctx context.Context, pipe *ingest.Pipeline, dayIndex int) error {
	for index := range gen.reporters {
		rep := &gen.reporters[index]
		if dayIndex < rep.first || dayIndex > rep.last {
			continue
		}

		err := gen.aggregateReporter(ctx, pipe, rep, dayIndex)
		if err != nil {
			return err
		}
	}

	return nil
}

func (gen *generator) aggregateReporter(
	ctx context.Context, pipe *ingest.Pipeline, rep *reporter, dayIndex int,
) error {
	hours := int(dayLength / time.Hour)
	if rep.long && dayIndex%longEvery == 0 && dayIndex < spanDays-longWindowLead {
		hours = longHours
	}

	begin, end, ok := gen.window(dayIndex, rep.offset, hours)
	if !ok {
		return nil
	}

	volume := dailyVolume(dayIndex, begin, gen.rng)
	for _, domain := range reportDomains {
		err := gen.oneAggregate(ctx, pipe, rep, dayIndex, domain, volume, begin, end)
		if err != nil {
			return err
		}
	}

	return nil
}

func (gen *generator) oneAggregate(
	ctx context.Context,
	pipe *ingest.Pipeline,
	rep *reporter,
	dayIndex int,
	domain string,
	volume int,
	begin, end time.Time,
) error {
	published := policyFor(domain, dayIndex, rep.bisOrg && domain == domainOrg)
	rows := recordsFor(domain, &published, volume, dayIndex, begin)
	meta := aggregateMeta{
		org: rep.name, email: rep.email, domain: domain, policy: published,
		reportID: rep.prefix + "-" + domain + "-" + strconv.FormatInt(begin.Unix(), base10),
		begin:    begin.Unix(), end: end.Unix(),
	}
	gen.buf = appendAggregate(gen.buf[:0], &meta, rows)

	err := gen.ingest(ctx, pipe, gen.buf, end)
	if err != nil {
		return fmt.Errorf("ingest %s aggregate for %s: %w", rep.name, domain, err)
	}

	err = gen.deliverCopy(ctx, pipe, rep, copyFor(rep, dayIndex, domain), end, &meta, rows)
	if err != nil {
		return fmt.Errorf("redeliver %s aggregate for %s: %w", rep.name, domain, err)
	}

	return nil
}

func copyFor(rep *reporter, dayIndex int, domain string) copyKind {
	if rep.prefix == prefixQuill && dayIndex == rep.first && domain == domainCom {
		return copyConflict
	}

	return scheduledCopy(rep.prefix, dayIndex, domain)
}

func scheduledCopy(prefix string, dayIndex int, domain string) copyKind {
	switch {
	case prefix == prefixNorthwind && dayIndex == gzipDay && domain == domainCom:
		return copyGzip
	case prefix == prefixHarbor && dayIndex == zipDay && domain == domainNet:
		return copyZip
	case prefix == prefixCedar && dayIndex == mimeDay && domain == domainCom:
		return copyMIME
	case prefix == prefixNorthwind && dayIndex == mimeOrgDay && domain == domainOrg:
		return copyMIME
	default:
		return copyNone
	}
}

func (gen *generator) deliverCopy(
	ctx context.Context,
	pipe *ingest.Pipeline,
	rep *reporter,
	kind copyKind,
	when time.Time,
	meta *aggregateMeta,
	rows []rec,
) error {
	switch kind {
	case copyNone:
		return nil
	case copyGzip:
		return gen.deliverCompressed(ctx, pipe, rep, when, subjectGzip, mediaGzip, fileGzip, true)
	case copyZip:
		return gen.deliverCompressed(ctx, pipe, rep, when, subjectZip, mediaZip, fileZip, false)
	case copyMIME:
		return gen.ingest(
			ctx,
			pipe,
			mimeAttachment(rep.email, subjectMIME, mediaXML, fileXML, gen.buf),
			when,
		)
	case copyConflict:
		return gen.deliverConflict(ctx, pipe, rep, when, meta, rows)
	default:
		return fmt.Errorf("delivery %d: %w", kind, errUnknownCopy)
	}
}

func (gen *generator) deliverCompressed(
	ctx context.Context,
	pipe *ingest.Pipeline,
	rep *reporter,
	when time.Time,
	subject, mediaType, filename string,
	gzipped bool,
) error {
	var (
		payload []byte
		err     error
	)
	if gzipped {
		payload, err = gzipBytes(gen.buf)
	} else {
		payload, err = zipBytes(gen.buf)
	}

	if err != nil {
		return err
	}

	raw := mimeAttachment(rep.email, subject, mediaType, filename, payload)

	return gen.ingest(ctx, pipe, raw, when)
}

func (gen *generator) deliverConflict(
	ctx context.Context,
	pipe *ingest.Pipeline,
	rep *reporter,
	when time.Time,
	meta *aggregateMeta,
	rows []rec,
) error {
	if len(rows) == 0 {
		return errEmptyRecords
	}

	rows[0].count++
	alt := appendAggregate(nil, meta, rows)
	raw := mimeAttachment(rep.email, subjectConflict, mediaXML, fileXML, alt)

	return gen.ingest(ctx, pipe, raw, when)
}
