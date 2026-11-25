
// Region presentation helpers.

const REGION_NAMES = {
  HK: "Hong Kong", TW: "Taiwan", MO: "Macau", CN: "Mainland China",
  JP: "Japan", KR: "South Korea",
  SG: "Singapore", TH: "Thailand", VN: "Vietnam", ID: "Indonesia", MY: "Malaysia",
  PH: "Philippines", KH: "Cambodia", LA: "Laos", MM: "Myanmar", BN: "Brunei",
  IN: "India", PK: "Pakistan", BD: "Bangladesh", LK: "Sri Lanka",
  US: "United States", CA: "Canada", MX: "Mexico",
  BR: "Brazil", AR: "Argentina", CL: "Chile", CO: "Colombia", PE: "Peru",
  GB: "United Kingdom", UK: "United Kingdom", IE: "Ireland", FR: "France", DE: "Germany",
  NL: "Netherlands", BE: "Belgium", LU: "Luxembourg",
  SE: "Sweden", NO: "Norway", FI: "Finland", DK: "Denmark", IS: "Iceland",
  ES: "Spain", PT: "Portugal", IT: "Italy", GR: "Greece",
  AT: "Austria", CH: "Switzerland", PL: "Poland", CZ: "Czechia", HU: "Hungary",
  SK: "Slovakia", SI: "Slovenia", HR: "Croatia", RO: "Romania", BG: "Bulgaria",
  RS: "Serbia", UA: "Ukraine", BY: "Belarus", LT: "Lithuania", LV: "Latvia",
  EE: "Estonia", MD: "Moldova", RU: "Russia", TR: "Turkey",
  AE: "United Arab Emirates", SA: "Saudi Arabia", IL: "Israel", QA: "Qatar",
  KW: "Kuwait", OM: "Oman", BH: "Bahrain", JO: "Jordan", LB: "Lebanon",
  IQ: "Iraq", IR: "Iran",
  AU: "Australia", NZ: "New Zealand",
  ZA: "South Africa", EG: "Egypt", NG: "Nigeria", KE: "Kenya", MA: "Morocco",
  DZ: "Algeria", TN: "Tunisia",
  UN: "Unassigned",
};
const REGION_FLAGS = {
  HK: "🇭🇰", TW: "🇹🇼", MO: "🇲🇴", CN: "🇨🇳",
  JP: "🇯🇵", KR: "🇰🇷",
  SG: "🇸🇬", TH: "🇹🇭", VN: "🇻🇳", ID: "🇮🇩", MY: "🇲🇾",
  PH: "🇵🇭", KH: "🇰🇭", LA: "🇱🇦", MM: "🇲🇲", BN: "🇧🇳",
  IN: "🇮🇳", PK: "🇵🇰", BD: "🇧🇩", LK: "🇱🇰",
  US: "🇺🇸", CA: "🇨🇦", MX: "🇲🇽",
  BR: "🇧🇷", AR: "🇦🇷", CL: "🇨🇱", CO: "🇨🇴", PE: "🇵🇪",
  GB: "🇬🇧", UK: "🇬🇧", IE: "🇮🇪", FR: "🇫🇷", DE: "🇩🇪",
  NL: "🇳🇱", BE: "🇧🇪", LU: "🇱🇺",
  SE: "🇸🇪", NO: "🇳🇴", FI: "🇫🇮", DK: "🇩🇰", IS: "🇮🇸",
  ES: "🇪🇸", PT: "🇵🇹", IT: "🇮🇹", GR: "🇬🇷",
  AT: "🇦🇹", CH: "🇨🇭", PL: "🇵🇱", CZ: "🇨🇿", HU: "🇭🇺",
  SK: "🇸🇰", SI: "🇸🇮", HR: "🇭🇷", RO: "🇷🇴", BG: "🇧🇬",
  RS: "🇷🇸", UA: "🇺🇦", BY: "🇧🇾", LT: "🇱🇹", LV: "🇱🇻",
  EE: "🇪🇪", MD: "🇲🇩", RU: "🇷🇺", TR: "🇹🇷",
  AE: "🇦🇪", SA: "🇸🇦", IL: "🇮🇱", QA: "🇶🇦",
  KW: "🇰🇼", OM: "🇴🇲", BH: "🇧🇭", JO: "🇯🇴", LB: "🇱🇧",
  IQ: "🇮🇶", IR: "🇮🇷",
  AU: "🇦🇺", NZ: "🇳🇿",
  ZA: "🇿🇦", EG: "🇪🇬", NG: "🇳🇬", KE: "🇰🇪", MA: "🇲🇦",
  DZ: "🇩🇿", TN: "🇹🇳",
  UN: "🏳️",
};

function normalizeRegionCodeForFlag(code) {
  const base = baseRegionCode(code);
  if (base === "UK") return "GB";
  return base;
}

function baseRegionCode(code) {
  return String(code || "").trim().toUpperCase().replace(/-RES/g, "").replace(/-PREM/g, "");
}

function egressGroupCode(region, residential) {
  if (!region) return "ANY";
  let code = String(region || "").trim().toUpperCase();
  if (residential) code += "-RES";
  return code;
}

function regionFlag(code) {
  const normalized = normalizeRegionCodeForFlag(code);
  if (REGION_FLAGS[normalized]) return REGION_FLAGS[normalized];
  if (!/^[A-Z]{2}$/.test(normalized) || normalized === "UN") return null;
  return normalized
    .split("")
    .map(ch => String.fromCodePoint(0x1F1E6 + ch.charCodeAt(0) - 65))
    .join("");
}

// Build region groups from pools
function buildRegionGroups(pools) {
  const groups = {};
  for (const pool of pools) {
    if (pool.source !== "static" && pool.source !== "subscription") continue;
    if (!pool.enabled) continue;
    for (const n of pool.nodes || []) {
      if (!n.region || n.region === "UN") continue;
      const code = egressGroupCode(n.region, n.residential);
      if (!groups[code]) {
        groups[code] = {
          code, region: n.region, residential: !!n.residential,
          name: REGION_NAMES[n.region] || n.region,
          flag: regionFlag(n.region) || "🏳️",
          count: 0, online: 0, sources: new Set(), latencies: [],
        };
      }
      groups[code].count += 1;
      groups[code].sources.add(pool.name);
      if (n.enabled && n.alive) {
        groups[code].online += 1;
        if (n.latency > 0) groups[code].latencies.push(n.latency);
      }
    }
  }
  // Template fallback availability
  const hasTemplateFor = (group) => pools.some(p =>
    p.source === "template" && p.enabled &&
    (p.coverage === "both" || !!p.residential === !!group.residential)
  );
  return Object.values(groups).map(g => ({
    ...g,
    sources: Array.from(g.sources),
    minLatency: g.latencies.length ? Math.min(...g.latencies) : null,
    templateBackup: hasTemplateFor(g),
  })).sort((a, b) => {
    if (a.residential !== b.residential) return a.residential ? 1 : -1;
    return a.region.localeCompare(b.region);
  });
}


export { REGION_NAMES, REGION_FLAGS, regionFlag, baseRegionCode, egressGroupCode, buildRegionGroups };
