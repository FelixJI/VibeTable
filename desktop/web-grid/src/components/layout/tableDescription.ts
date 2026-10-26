// data.changed 的本机观察时间不等于权威元数据；未知时不显示说明。
export function localModifiedLabel(
  timestamp: number | undefined | null,
): string | null {
  if (!timestamp || timestamp <= 0) return null;
  const minutes = Math.max(0, Math.floor((Date.now() - timestamp) / 60_000));
  const relative = minutes < 1
    ? "刚刚"
    : minutes < 60
      ? `${minutes} 分钟前`
      : minutes < 1440
        ? `${Math.floor(minutes / 60)} 小时前`
        : `${Math.floor(minutes / 1440)} 天前`;
  return `本机修改 · ${relative}`;
}
