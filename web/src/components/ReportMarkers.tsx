import type { ReportSummary } from "../api/types.ts";

export function ReportMarkers({ report }: { report: ReportSummary }) {
  return (
    <span className="inline-flex flex-wrap gap-1">
      {report.duplicate && (
        <span className="badge">
          Duplicate{report.duplicateOf ? ` of #${report.duplicateOf}` : ""}
        </span>
      )}
      {report.conflict && <span className="badge badge-fail">Conflict</span>}
      {report.longWindow && <span className="badge">Long window</span>}
    </span>
  );
}
