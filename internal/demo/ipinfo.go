package demo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/store"
)

type ipInfo struct {
	ip        string
	ptr       string
	org       string
	asn       uint32
	confirmed bool
}

func writeIPInfo(ctx context.Context, st *store.Store, now time.Time) error {
	rows := ipCatalog()
	lookedUp := now.Unix()

	err := st.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		for index := range rows {
			err := insertIPInfo(ctx, tx, &rows[index], lookedUp)
			if err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("write demo ip info: %w", err)
	}

	return nil
}

func insertIPInfo(ctx context.Context, tx *sqlx.Tx, info *ipInfo, lookedUp int64) error {
	confirmed := 0
	if info.confirmed {
		confirmed = 1
	}

	_, err := tx.ExecContext(ctx, `INSERT INTO ip_info (
		ip, ptr, ptr_confirmed, asn, as_org, looked_up_at, error
	) VALUES (?, ?, ?, ?, ?, ?, '')`,
		info.ip, info.ptr, confirmed, info.asn, info.org, lookedUp)
	if err != nil {
		return fmt.Errorf("insert ip info for %s: %w", info.ip, err)
	}

	return nil
}

func ipCatalog() []ipInfo {
	noise := noiseInfo()
	known := knownIPs()
	rows := make([]ipInfo, 0, len(noise)+len(known))
	rows = append(rows, known...)

	return append(rows, noise...)
}

func knownIPs() []ipInfo {
	return []ipInfo{
		{
			ip:        ipProvider4,
			ptr:       "mail.example-provider.test",
			org:       orgProvider,
			asn:       asnProvider,
			confirmed: true,
		},
		{
			ip:        ipProvider6,
			ptr:       "mail.example-provider.test",
			org:       orgProvider,
			asn:       asnProvider,
			confirmed: true,
		},
		{ip: ipNews4, ptr: "mta.news.example.com", org: orgNewsHost, asn: asnNews, confirmed: true},
		{ip: ipCRM, ptr: "crm.example.invalid", org: orgCRMHost, asn: asnCRM, confirmed: true},
		{
			ip:        ipList4,
			ptr:       "list.example-relay.test",
			org:       orgListHost,
			asn:       asnList,
			confirmed: true,
		},
		{
			ip:        ipList6,
			ptr:       "list.example-relay.test",
			org:       orgListHost,
			asn:       asnList,
			confirmed: true,
		},
		{
			ip:        ipForward4,
			ptr:       "fwd.example-forwarder.test",
			org:       orgForwardH,
			asn:       asnForward,
			confirmed: true,
		},
		{ip: ipSpoof4, ptr: "", org: orgSpoofH, asn: asnSpoof, confirmed: false},
		{ip: ipSpoof6, ptr: "", org: orgSpoofH, asn: asnSpoof, confirmed: false},
	}
}

func noiseInfo() []ipInfo {
	rows := make([]ipInfo, len(noiseAddresses))
	for index, ip := range noiseAddresses {
		info := ipInfo{
			ip: ip, ptr: "n" + strconv.Itoa(index) + ".example-noise.test",
			org: orgNoiseH, asn: asnNoise, confirmed: true,
		}
		if strings.Contains(ip, ":") {
			info.ptr = ""
			info.org = orgNoise6H
			info.asn = asnNoise6
			info.confirmed = false
		}

		rows[index] = info
	}

	return rows
}
