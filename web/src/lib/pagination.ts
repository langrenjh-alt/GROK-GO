export const DEFAULT_PAGE_SIZE = 25;

export function paginatedPath(
  path: string,
  page: number,
  pageSize: number,
  filters: Record<string, string | number | boolean | undefined> = {},
) {
  const query = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
  for (const [key, value] of Object.entries(filters)) {
    if (value !== undefined && value !== "") query.set(key, String(value));
  }
  return `${path}?${query.toString()}`;
}
