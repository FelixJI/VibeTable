import type { GlobalThemeOverrides } from "naive-ui";

// Naive UI overrides so the component library eats the same Feishu tokens.
// Light + dark variants. Primary color differs (dark lifts for contrast).

const fontFamily =
  '"PingFang SC", "Microsoft YaHei", system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

const base: GlobalThemeOverrides = {
  common: {
    borderRadius: "6px",
    borderRadiusSmall: "4px",
    fontFamily,
    fontWeightStrong: "500",
  },
  Button: { fontWeight: "500" },
  Card: { borderRadius: "8px" },
};

export const lightThemeOverrides: GlobalThemeOverrides = {
  ...base,
  common: {
    ...base.common,
    primaryColor: "#3370ff",
    primaryColorHover: "#2b5fe0",
    primaryColorPressed: "#1f47b3",
    primaryColorSuppl: "#3370ff",
    successColor: "#00b88a",
    successColorHover: "#00a67e",
    successColorPressed: "#008f6c",
    warningColor: "#ffa600",
    warningColorHover: "#e59500",
    warningColorPressed: "#cc8400",
    errorColor: "#f54a45",
    errorColorHover: "#db3f3a",
    errorColorPressed: "#c23530",
    infoColor: "#3370ff",
  },
};

export const darkThemeOverrides: GlobalThemeOverrides = {
  ...base,
  common: {
    ...base.common,
    primaryColor: "#5b8bff",
    primaryColorHover: "#4a7dff",
    primaryColorPressed: "#3a6bff",
    primaryColorSuppl: "#5b8bff",
    successColor: "#1ecda0",
    warningColor: "#ffb840",
    errorColor: "#f56a66",
    infoColor: "#5b8bff",
  },
};
