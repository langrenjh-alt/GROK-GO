export function formatNumber(value: number | undefined): string {
  return new Intl.NumberFormat().format(value ?? 0);
}

export function formatLimit(value: number, locale = "zh"): string {
  if (value === 0) return locale === "zh" ? "无限" : "Unlimited";
  return formatNumber(value);
}

export function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let size = value / 1024;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  return `${size.toFixed(size >= 10 ? 0 : 1)} ${units[index]}`;
}

export function formatRelative(value?: string, locale = "zh"): string {
  if (!value) return "-";
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return "-";
  const delta = timestamp - Date.now();
  const abs = Math.abs(delta);
  const formatter = new Intl.RelativeTimeFormat(locale === "zh" ? "zh-CN" : "en", { numeric: "auto" });
  if (abs < 60_000) return formatter.format(Math.round(delta / 1000), "second");
  if (abs < 3_600_000) return formatter.format(Math.round(delta / 60_000), "minute");
  if (abs < 86_400_000) return formatter.format(Math.round(delta / 3_600_000), "hour");
  if (abs < 604_800_000) return formatter.format(Math.round(delta / 86_400_000), "day");
  return new Intl.DateTimeFormat(locale === "zh" ? "zh-CN" : "en", { dateStyle: "medium" }).format(timestamp);
}
