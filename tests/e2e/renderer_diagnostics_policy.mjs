// WebView2 serves registered plugin origins locally and denies unknown ones.
// Only the exact HTTPS package-host shape is exempt from external HTTP diagnostics.
const HOST_LOCAL_PLUGIN_ORIGIN_PATTERN =
  /^https:\/\/[a-f0-9]{32}\.[a-f0-9]{32}\.plugins\.vibetable\.local$/;

function isHostLocalPluginRequest(url) {
  return url.username === "" && url.password === "" && url.port === ""
    && HOST_LOCAL_PLUGIN_ORIGIN_PATTERN.test(url.origin);
}

export function collectExternalRendererRequests(networkEntries) {
  return networkEntries.filter((entry) => {
    let url;
    try {
      url = new URL(entry.url);
    } catch {
      return false;
    }
    if (!["http:", "https:"].includes(url.protocol)) return false;
    if (url.protocol === "https:" && url.hostname === "app.vibetable.local"
      && url.port === "" && url.username === "" && url.password === "") return false;
    if (isHostLocalPluginRequest(url)) return false;
    return !["127.0.0.1", "::1", "localhost"].includes(url.hostname);
  });
}
