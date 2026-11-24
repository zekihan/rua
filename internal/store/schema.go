package store

// Migrations are applied in order; PRAGMA user_version records how many ran.
// Never edit a released migration, append a new one.
//
// Conventions: times are UTC unix seconds; SHA-256 digests are 32-byte
// BLOBs; JSON columns hold arrays encoded by encoding/json; text from
// reports is already length-capped by the parsers.
var Migrations = []string{
	`
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) STRICT;

-- One row per configured IMAP mailbox. Owned by internal/imapsrc.
CREATE TABLE mailboxes (
	name             TEXT PRIMARY KEY,
	uidvalidity      INTEGER NOT NULL DEFAULT 0,
	next_uid         INTEGER NOT NULL DEFAULT 1,
	last_fetch_start INTEGER,
	last_fetch_end   INTEGER,
	-- ok, error or empty before the first fetch
	last_result      TEXT NOT NULL DEFAULT '',
	last_error       TEXT NOT NULL DEFAULT '',
	last_success     INTEGER
) STRICT;

-- Notable ingest events shown on the Status page, e.g. uidvalidity-reset.
CREATE TABLE events (
	id      INTEGER PRIMARY KEY,
	at      INTEGER NOT NULL,
	kind    TEXT NOT NULL,
	mailbox TEXT NOT NULL DEFAULT '',
	detail  TEXT NOT NULL DEFAULT ''
) STRICT;

-- zstd-compressed raw bytes, addressed by the SHA-256 of the uncompressed
-- bytes. Holds whole messages and every extracted report document.
CREATE TABLE blobs (
	sha256 BLOB PRIMARY KEY,
	size   INTEGER NOT NULL,
	data   BLOB NOT NULL
) STRICT;

CREATE TABLE messages (
	id             INTEGER PRIMARY KEY,
	-- SHA-256 of the raw message; for skipped messages of the header bytes,
	-- mailbox, UIDVALIDITY and UID
	sha256         BLOB NOT NULL UNIQUE,
	-- NULL for skipped (oversized) messages
	blob           BLOB REFERENCES blobs (sha256),
	-- empty for local imports
	mailbox        TEXT NOT NULL DEFAULT '',
	uid            INTEGER NOT NULL DEFAULT 0,
	internaldate   INTEGER NOT NULL,
	size           INTEGER NOT NULL,
	from_addr      TEXT NOT NULL DEFAULT '',
	subject        TEXT NOT NULL DEFAULT '',
	-- dmarc-aggregate, tls-report, dmarc-failure, other
	class          TEXT NOT NULL,
	-- ok, partial (some reports failed), error, skipped
	status         TEXT NOT NULL,
	error          TEXT NOT NULL DEFAULT '',
	parser_version INTEGER NOT NULL,
	ingested_at    INTEGER NOT NULL
) STRICT;

CREATE INDEX messages_class ON messages (class, status);

CREATE TABLE reports (
	id            INTEGER PRIMARY KEY,
	-- dmarc-aggregate or tls-report
	kind          TEXT NOT NULL,
	message       INTEGER NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
	-- extracted XML/JSON document
	blob          BLOB NOT NULL REFERENCES blobs (sha256),
	org           TEXT NOT NULL,
	email         TEXT NOT NULL DEFAULT '',
	extra_contact TEXT NOT NULL DEFAULT '',
	report_id     TEXT NOT NULL,
	begin         INTEGER NOT NULL,
	end           INTEGER NOT NULL,
	-- the policy domain (TLS reports get one row per policy domain)
	domain        TEXT NOT NULL,
	-- rfc7489, dmarc-2.0 or tlsrpt
	schema        TEXT NOT NULL DEFAULT '',
	-- set on a second copy of an already stored report; duplicates have no
	-- policy, records or TLS rows and are never counted
	duplicate_of  INTEGER REFERENCES reports (id) ON DELETE CASCADE,
	-- duplicate whose content differs from the first copy
	conflict      INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE UNIQUE INDEX reports_identity ON reports (kind, org, report_id, domain)
	WHERE duplicate_of IS NULL;
CREATE INDEX reports_window ON reports (kind, domain, end, begin);
CREATE INDEX reports_message ON reports (message);
CREATE INDEX reports_duplicate ON reports (duplicate_of);

CREATE TABLE policies (
	report    INTEGER PRIMARY KEY REFERENCES reports (id) ON DELETE CASCADE,
	p         TEXT NOT NULL DEFAULT '',
	sp        TEXT NOT NULL DEFAULT '',
	np        TEXT NOT NULL DEFAULT '',
	pct       INTEGER,
	adkim     TEXT NOT NULL DEFAULT '',
	aspf      TEXT NOT NULL DEFAULT '',
	fo        TEXT NOT NULL DEFAULT '',
	testing   TEXT NOT NULL DEFAULT '',
	discovery TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE records (
	id            INTEGER PRIMARY KEY,
	report        INTEGER NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
	source_ip     TEXT NOT NULL,
	count         INTEGER NOT NULL,
	disposition   TEXT NOT NULL DEFAULT '',
	dkim          TEXT NOT NULL DEFAULT '',
	spf           TEXT NOT NULL DEFAULT '',
	-- JSON array of {"type","comment"}
	reasons       TEXT NOT NULL DEFAULT '[]',
	header_from   TEXT NOT NULL DEFAULT '',
	envelope_from TEXT NOT NULL DEFAULT '',
	envelope_to   TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX records_report ON records (report);
CREATE INDEX records_source_ip ON records (source_ip);

CREATE TABLE auth_results (
	record       INTEGER NOT NULL REFERENCES records (id) ON DELETE CASCADE,
	-- dkim or spf
	method       TEXT NOT NULL,
	domain       TEXT NOT NULL DEFAULT '',
	selector     TEXT NOT NULL DEFAULT '',
	scope        TEXT NOT NULL DEFAULT '',
	result       TEXT NOT NULL DEFAULT '',
	human_result TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX auth_results_record ON auth_results (record);

CREATE TABLE tls_policies (
	id       INTEGER PRIMARY KEY,
	report   INTEGER NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
	type     TEXT NOT NULL,
	domain   TEXT NOT NULL,
	-- JSON arrays of text
	strings  TEXT NOT NULL DEFAULT '[]',
	mx_hosts TEXT NOT NULL DEFAULT '[]',
	success  INTEGER NOT NULL DEFAULT 0,
	failure  INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX tls_policies_report ON tls_policies (report);

CREATE TABLE tls_failures (
	policy            INTEGER NOT NULL REFERENCES tls_policies (id) ON DELETE CASCADE,
	result_type       TEXT NOT NULL,
	sending_ip        TEXT NOT NULL DEFAULT '',
	receiving_ip      TEXT NOT NULL DEFAULT '',
	receiving_mx      TEXT NOT NULL DEFAULT '',
	receiving_mx_helo TEXT NOT NULL DEFAULT '',
	failed_sessions   INTEGER NOT NULL DEFAULT 0,
	reason_code       TEXT NOT NULL DEFAULT '',
	additional_info   TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX tls_failures_policy ON tls_failures (policy);

-- Enrichment cache. Owned by internal/enrich.
CREATE TABLE ip_info (
	ip            TEXT PRIMARY KEY,
	ptr           TEXT NOT NULL DEFAULT '',
	ptr_confirmed INTEGER NOT NULL DEFAULT 0,
	asn           INTEGER NOT NULL DEFAULT 0,
	as_org        TEXT NOT NULL DEFAULT '',
	looked_up_at  INTEGER NOT NULL,
	error         TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE failure_reports (
	id            INTEGER PRIMARY KEY,
	message       INTEGER NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
	arrival       INTEGER NOT NULL DEFAULT 0,
	domain        TEXT NOT NULL DEFAULT '',
	source_ip     TEXT NOT NULL DEFAULT '',
	auth_failure  TEXT NOT NULL DEFAULT '',
	orig_from     TEXT NOT NULL DEFAULT '',
	orig_to       TEXT NOT NULL DEFAULT '',
	orig_subject  TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX failure_reports_message ON failure_reports (message);
`,
}
