/** Parsed Prometheus metric family. */
export interface MetricFamily {
  name: string;
  help: string;
  type: string;
  metrics: Metric[];
}

export interface Metric {
  labels: Record<string, string>;
  value?: number;
  // For histograms
  buckets?: { le: string; count: number }[];
  count?: number;
  sum?: number;
}

/**
 * Parse Prometheus text exposition format into structured JSON.
 * Only parses loophole_* metrics to keep the response small.
 */
export function parsePrometheusText(text: string): Record<string, MetricFamily> {
  const families: Record<string, MetricFamily> = {};
  let currentName = "";

  for (const line of text.split("\n")) {
    if (line.startsWith("# HELP ")) {
      const rest = line.slice(7);
      const spaceIdx = rest.indexOf(" ");
      const name = rest.slice(0, spaceIdx);
      if (!name.startsWith("loophole_")) continue;
      currentName = name;
      if (!families[name]) {
        families[name] = { name, help: rest.slice(spaceIdx + 1), type: "", metrics: [] };
      } else {
        families[name].help = rest.slice(spaceIdx + 1);
      }
    } else if (line.startsWith("# TYPE ")) {
      const rest = line.slice(7);
      const spaceIdx = rest.indexOf(" ");
      const name = rest.slice(0, spaceIdx);
      if (!name.startsWith("loophole_")) continue;
      currentName = name;
      if (!families[name]) {
        families[name] = { name, help: "", type: rest.slice(spaceIdx + 1), metrics: [] };
      } else {
        families[name].type = rest.slice(spaceIdx + 1);
      }
    } else if (line && !line.startsWith("#")) {
      const parsed = parseMetricLine(line);
      if (!parsed || !parsed.name.startsWith("loophole_")) continue;

      // Map histogram _bucket/_sum/_count back to the family
      const baseName = parsed.name
        .replace(/_bucket$/, "")
        .replace(/_sum$/, "")
        .replace(/_count$/, "")
        .replace(/_total$/, "");

      // Find the right family
      const familyName = families[parsed.name]
        ? parsed.name
        : families[baseName]
          ? baseName
          : families[parsed.name.replace(/_total$/, "")]
            ? parsed.name.replace(/_total$/, "")
            : null;

      if (!familyName) continue;
      const family = families[familyName];

      if (parsed.name.endsWith("_bucket")) {
        const le = parsed.labels.le || "";
        delete parsed.labels.le;
        const labelKey = JSON.stringify(parsed.labels);
        let metric = family.metrics.find(
          (m) => JSON.stringify(m.labels) === labelKey && m.buckets
        );
        if (!metric) {
          metric = { labels: parsed.labels, buckets: [], count: 0, sum: 0 };
          family.metrics.push(metric);
        }
        metric.buckets!.push({ le, count: parsed.value });
      } else if (parsed.name.endsWith("_sum")) {
        const labelKey = JSON.stringify(parsed.labels);
        const metric = family.metrics.find(
          (m) => JSON.stringify(m.labels) === labelKey && m.buckets
        );
        if (metric) metric.sum = parsed.value;
      } else if (parsed.name.endsWith("_count") && family.type === "histogram") {
        const labelKey = JSON.stringify(parsed.labels);
        const metric = family.metrics.find(
          (m) => JSON.stringify(m.labels) === labelKey && m.buckets
        );
        if (metric) metric.count = parsed.value;
      } else {
        family.metrics.push({ labels: parsed.labels, value: parsed.value });
      }
    }
  }

  return families;
}

function parseMetricLine(
  line: string
): { name: string; labels: Record<string, string>; value: number } | null {
  let name: string;
  let rest: string;
  const labels: Record<string, string> = {};

  const braceIdx = line.indexOf("{");
  if (braceIdx !== -1) {
    name = line.slice(0, braceIdx);
    const closeBrace = line.indexOf("}");
    if (closeBrace === -1) return null;
    const labelStr = line.slice(braceIdx + 1, closeBrace);
    for (const pair of splitLabels(labelStr)) {
      const eq = pair.indexOf("=");
      if (eq === -1) continue;
      const k = pair.slice(0, eq);
      let v = pair.slice(eq + 1);
      if (v.startsWith('"') && v.endsWith('"')) v = v.slice(1, -1);
      labels[k] = v;
    }
    rest = line.slice(closeBrace + 1).trim();
  } else {
    const spaceIdx = line.indexOf(" ");
    if (spaceIdx === -1) return null;
    name = line.slice(0, spaceIdx);
    rest = line.slice(spaceIdx + 1).trim();
  }

  const value = parseFloat(rest);
  if (isNaN(value)) return null;
  return { name, labels, value };
}

/** Split label pairs, respecting quoted values that may contain commas. */
function splitLabels(s: string): string[] {
  const parts: string[] = [];
  let current = "";
  let inQuote = false;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (ch === '"' && s[i - 1] !== "\\") {
      inQuote = !inQuote;
    }
    if (ch === "," && !inQuote) {
      parts.push(current);
      current = "";
    } else {
      current += ch;
    }
  }
  if (current) parts.push(current);
  return parts;
}
