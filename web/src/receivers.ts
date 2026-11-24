// Pure helpers for the Receivers section, unit-tested.
export const RECEIVERS_COLLAPSED = 5;

export function collapseRows<T>(
  rows: T[],
  expanded: boolean,
  limit = RECEIVERS_COLLAPSED,
): { shown: T[]; hidden: number } {
  if (expanded || rows.length <= limit) return { shown: rows, hidden: 0 };
  return { shown: rows.slice(0, limit), hidden: rows.length - limit };
}
