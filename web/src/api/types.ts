// Mirrors internal/query/types.go exactly. Dates are YYYY-MM-DD in tz;
// timestamps are unix seconds; rates are fractions in [0,1], null when denom 0.

export interface Range {
  from: string;
  to: string;
  tz: string;
}
export interface Verdicts {
  messages: number;
  pass: number;
  forwarded: number;
  fail: number;
}
export interface Count {
  value: string;
  messages: number;
}
export interface Policy {
  p: string;
  sp: string;
  np: string;
  pct: number | null;
  adkim: string;
  aspf: string;
  fo: string;
  testing: string;
  discovery: string;
}

export interface DomainsResponse {
  domains: Domain[];
}
export interface Domain {
  domain: string;
  firstReportAt: number;
  lastReportAt: number;
  dmarcReports: number;
  tlsReports: number;
}

export interface Overview {
  range: Range;
  days: DayVerdicts[];
  policyChanges: PolicyChange[];
  topFailing: SourceVolume[];
  newUnknown: NewSource[];
  newExpected: NewSource[];
  quietReporters: QuietReporter[];
  totals: OverviewTotals;
  longWindows: number;
}
export interface OverviewTotals {
  passRate: number | null;
  messages: number;
  pass: number;
  forwarded: number;
  fail: number;
  quarantined: number;
  rejected: number;
  reports: number;
}
export interface DayVerdicts {
  date: string;
  messages: number;
  pass: number;
  forwarded: number;
  fail: number;
}
export interface PolicyChange {
  before: Policy | null;
  after: Policy;
  domain: string;
  date: string;
  fields: string[];
  reportId: number;
}
export interface SourceVolume {
  key: string;
  name: string;
  kind: string;
  expected: boolean;
  messages: number;
  pass: number;
  forwarded: number;
  fail: number;
}
export interface NewSource {
  key: string;
  name: string;
  kind: string;
  firstSeen: string;
  messages: number;
  pass: number;
  forwarded: number;
  fail: number;
  expected: boolean;
}
export interface QuietReporter {
  org: string;
  domains: string[];
  lastReportAt: number;
}

export interface SourcesResponse {
  range: Range;
  sources: SourceRow[];
}
export interface SourceRow {
  spfAligned: number | null;
  dkimAligned: number | null;
  name: string;
  kind: string;
  lastSeen: string;
  firstSeen: string;
  key: string;
  rule: string;
  dkimDomains: string[];
  forwarded: number;
  fail: number;
  pass: number;
  ips: number;
  messages: number;
  expected: boolean;
}
export interface SourceDetail {
  range: Range;
  ips: IPRow[];
  days: DayVerdicts[];
  combinations: Combination[];
  envelopeFrom: Count[];
  headerFrom: Count[];
  dkimSignatures: Count[];
  reporters: Count[];
  source: SourceRow;
}
export interface IPRow {
  ip: string;
  ptr: string;
  asOrg: string;
  messages: number;
  pass: number;
  forwarded: number;
  fail: number;
  asn: number;
  ptrConfirmed: boolean;
}
export interface Combination {
  spf: string;
  dkim: string;
  disposition: string;
  verdict: string;
  messages: number;
  spfAligned: boolean;
  dkimAligned: boolean;
}

export interface ReportsResponse {
  nextCursor: string;
  reports: ReportSummary[];
}
export interface ReportSummary {
  passRate: number | null;
  duplicateOf: number | null;
  domain: string;
  reportId: string;
  date: string;
  org: string;
  kind: string;
  id: number;
  begin: number;
  end: number;
  messages: number;
  tlsFailed: number;
  duplicate: boolean;
  conflict: boolean;
  longWindow: boolean;
}
export interface ReportDetail {
  policy: Policy | null;
  email: string;
  extraContact: string;
  schema: string;
  records: RecordDetail[];
  tlsPolicies: TLSPolicy[];
  copies: ReportSummary[];
  report: ReportSummary;
  messageId: number;
}
export interface RecordDetail {
  headerFrom: string;
  sourceKey: string;
  sourceName: string;
  disposition: string;
  dkim: string;
  spf: string;
  verdict: string;
  sourceIp: string;
  envelopeFrom: string;
  envelopeTo: string;
  reasons: Reason[];
  auth: AuthResult[];
  count: number;
}
export interface Reason {
  type: string;
  comment: string;
}
export interface AuthResult {
  method: string;
  domain: string;
  selector: string;
  scope: string;
  result: string;
}

export interface TLSResponse {
  range: Range;
  domains: TLSDomain[];
}
export interface TLSDomain {
  domain: string;
  success: number;
  failure: number;
  reports: number;
  lastReportAt: number;
}
export interface TLSDomainDetail {
  range: Range;
  domain: string;
  days: TLSDay[];
  failures: TLSFailure[];
  policies: TLSPolicy[];
  reporters: Count[];
  success: number;
  failure: number;
}
export interface TLSDay {
  date: string;
  success: number;
  failure: number;
}
export interface TLSFailure {
  resultType: string;
  mxHosts: string[];
  receivingIps: string[];
  reasonCodes: string[];
  sessions: number;
}
export interface TLSPolicy {
  type: string;
  domain: string;
  strings: string[];
  mxHosts: string[];
  failures: TLSFailureDetail[];
  success: number;
  failure: number;
  reports: number;
  lastSeenAt: number;
}
export interface TLSFailureDetail {
  resultType: string;
  sendingIp: string;
  receivingIp: string;
  receivingMx: string;
  reasonCode: string;
  failedSessions: number;
}

export interface Status {
  fetch: FetchState;
  lastSuccessAt: number | null;
  problems: MessageRow[];
  mailboxes: MailboxStatus[];
  classes: ClassCount[];
  other: MessageRow[];
  conflicts: ReportSummary[];
  events: Event[];
  failureReports: FailureReport[];
  staleAfterSeconds: number;
  duplicates: number;
  parserVersion: number;
  enrichQueue: number;
  stale: boolean;
  reparsing: boolean;
}
export interface FetchState {
  nextFetchAt: number | null;
  cooldownUntilAt: number | null;
  enabled: boolean;
  running: boolean;
}
export interface MailboxStatus {
  pending: number | null;
  configured: boolean;
  lastFetchStartAt: number | null;
  lastFetchEndAt: number | null;
  lastSuccessAt: number | null;
  name: string;
  lastResult: string;
  lastError: string;
  uidValidity: number;
  nextUid: number;
}
export interface ClassCount {
  class: string;
  status: string;
  messages: number;
}
export interface MessageRow {
  mailbox: string;
  from: string;
  subject: string;
  class: string;
  status: string;
  error: string;
  id: number;
  internalDateAt: number;
  size: number;
  uid: number;
  hasRaw: boolean;
}
export interface Event {
  kind: string;
  mailbox: string;
  detail: string;
  at: number;
}
export interface FailureReport {
  domain: string;
  sourceIp: string;
  authFailure: string;
  from: string;
  to: string;
  subject: string;
  messageId: number;
  arrivalAt: number;
}
export interface FetchResponse {
  status: string;
}
export interface ErrorBody {
  error: ErrorDetail;
}
export interface ErrorDetail {
  code: string;
  message: string;
}

export interface ReportersResponse {
  range: Range;
  reporters: Reporter[];
}

export interface Reporter {
  org: string;
  domains: string[];
  reports: number;
  messages: number;
  firstReportAt: number;
  lastReportAt: number;
  quiet: boolean;
}
