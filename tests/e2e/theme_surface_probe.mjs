/**
 * Collect browser-computed surface evidence with transparency resolved through
 * the ancestor stack. Its source is embedded in the renderer sampler below,
 * because Playwright page functions cannot capture imported bindings.
 */
export function collectBrowserSurfaceEvidence(element) {
  const parseColor = (value) => {
    const match = value.match(
      /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+))?\s*\)$/i,
    );
    if (!match) return null;
    return [
      Number(match[1]),
      Number(match[2]),
      Number(match[3]),
      match[4] === undefined ? 1 : Number(match[4]),
    ];
  };
  const compositeBehind = (front, back) => {
    const alpha = front[3] + back[3] * (1 - front[3]);
    if (alpha <= 0) return [0, 0, 0, 0];
    return [
      (front[0] * front[3] + back[0] * back[3] * (1 - front[3])) / alpha,
      (front[1] * front[3] + back[1] * back[3] * (1 - front[3])) / alpha,
      (front[2] * front[3] + back[2] * back[3] * (1 - front[3])) / alpha,
      alpha,
    ];
  };
  const luminance = (color) => {
    const channels = color.slice(0, 3).map((value) => {
      const normalized = value / 255;
      return normalized <= 0.04045
        ? normalized / 12.92
        : ((normalized + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
  };

  const style = getComputedStyle(element);
  let effective = [0, 0, 0, 0];
  let current = element;
  while (current) {
    const background = parseColor(getComputedStyle(current).backgroundColor);
    if (background) effective = compositeBehind(effective, background);
    if (effective[3] >= 0.999) break;
    current = current.parentElement;
  }
  if (effective[3] < 0.999) {
    effective = compositeBehind(effective, [255, 255, 255, 1]);
  }
  const foreground = parseColor(style.color);
  const backgroundLuminance = luminance(effective);
  const parsedOpacity = Number.parseFloat(style.opacity);
  const opacity = Number.isFinite(parsedOpacity)
    ? Math.max(0, Math.min(1, parsedOpacity))
    : 1;
  const effectiveForeground = foreground
    ? compositeBehind(
      [foreground[0], foreground[1], foreground[2], foreground[3] * opacity],
      effective,
    )
    : null;
  const foregroundLuminance = effectiveForeground
    ? luminance(effectiveForeground)
    : null;
  const contrast = foregroundLuminance === null
    ? null
    : (Math.max(backgroundLuminance, foregroundLuminance) + 0.05)
      / (Math.min(backgroundLuminance, foregroundLuminance) + 0.05);
  const rect = element.getBoundingClientRect();
  return {
    rawBackground: style.backgroundColor,
    effectiveBackground: effective,
    backgroundLuminance,
    foreground: style.color,
    effectiveForeground,
    opacity,
    contrast,
    visible: rect.width > 0 && rect.height > 0,
  };
}

const CONNECTED_THEME_SURFACE_SAMPLER = Function(`return (selectors) => {
  const collect = ${collectBrowserSurfaceEvidence.toString()};
  const isConnectedAndVisible = (element) => {
    if (!element?.isConnected) return false;
    const rect = element.getBoundingClientRect();
    const visibility = getComputedStyle(element).visibility;
    return rect.width > 0 && rect.height > 0 && visibility !== "hidden" && visibility !== "collapse";
  };
  const root = document.querySelector(selectors.root);
  const table = document.querySelector(selectors.table);
  const cell = document.querySelector(selectors.cell);
  if (![root, table, cell].every(isConnectedAndVisible)) return null;
  return {
    root: {
      rootDark: root.classList.contains("dark"),
      colorScheme: getComputedStyle(root).colorScheme,
    },
    table: collect(table),
    cell: collect(cell),
  };
}`)();

/**
 * Wait only for connected visible nodes, then return one renderer-turn sample.
 * Theme colours are deliberately not part of readiness: bad palettes must fail
 * the caller's existing contrast assertions immediately.
 */
export async function sampleConnectedThemeSurfaces(page, {
  root = "html",
  table = ".tabulator-tableholder",
  cell = ".tabulator-row .tabulator-cell",
  timeout = 10_000,
} = {}) {
  const sample = await page.waitForFunction(
    CONNECTED_THEME_SURFACE_SAMPLER,
    { root, table, cell },
    { timeout },
  );
  try {
    return await sample.jsonValue();
  } finally {
    await sample.dispose();
  }
}
