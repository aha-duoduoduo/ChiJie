import React from "react";
import { Ic } from "./icons.jsx";

const RETRY_STATUSES = [408, 425, 429, 500, 502, 503, 504];

export default function DomainRetryRules({ rules, onChange, disabled, single = false }) {
  const update = (index, patch) => onChange(rules.map((rule, i) => i === index ? { ...rule, ...patch } : rule));
  const add = () => onChange([...rules, {
    domain: "", enabled: true, max_retries: 3, retry_other_nodes: true,
    status_codes: [429, 500, 502, 503, 504],
  }]);

  return <section className="domain-retries" aria-label="Domain retry rules">
    <div className="row gap-12" style={{ flexWrap: "wrap" }}>
      <h3>Domain retries</h3>
      {!single && <button className="btn sm ml-auto" disabled={disabled || rules.length >= 100} onClick={add}><Ic.plus/> Add domain rule</button>}
    </div>
    <p className="field-hint">Exact hostname · GET / HEAD only · 404 is never retried.</p>
    {rules.length === 0 && <div className="domain-retry-empty">No domain rules. HTTP error responses are returned without retrying.</div>}
    {rules.map((rule, index) => <fieldset className="domain-retry-rule" key={index} disabled={disabled}>
      <legend className="sr-only">Domain retry rule {index + 1}</legend>
      <div className="row gap-12" style={{ marginBottom: 14 }}>
        <label className="domain-retry-check"><input type="checkbox" checked={rule.enabled} onChange={e => update(index, { enabled: e.target.checked })}/> Enabled</label>
        {!single && <button className="btn sm ghost danger ml-auto" aria-label={`Remove domain rule ${index + 1}`} onClick={() => onChange(rules.filter((_, i) => i !== index))}>Remove</button>}
      </div>
      <div className="field-row">
        <label className="field"><span className="field-label">Domain</span>
          <input className="input mono" value={rule.domain} readOnly={single} placeholder="api.example.com" maxLength={253} onChange={e => update(index, { domain: e.target.value })}/>
        </label>
        <label className="field"><span className="field-label">Max retries</span>
          <input className="input mono" type="number" min="0" max="20" value={rule.max_retries} onChange={e => update(index, { max_retries: e.target.value })}/>
          <span className="field-hint">Additional attempts after the first request (0–20).</span>
        </label>
        <label className="field"><span className="field-label">Retry route</span>
          <select className="select" value={rule.retry_other_nodes ? "other" : "same"} onChange={e => update(index, { retry_other_nodes: e.target.value === "other" })}>
            <option value="other">Use another node</option><option value="same">Use the same node</option>
          </select>
        </label>
        <div className="field"><span className="field-label">Retry HTTP statuses</span>
          <div className="domain-retry-statuses">{RETRY_STATUSES.map(code => <label className="domain-retry-check mono" key={code}>
            <input type="checkbox" checked={rule.status_codes.includes(code)} onChange={e => update(index, { status_codes: e.target.checked ? [...rule.status_codes, code] : rule.status_codes.filter(value => value !== code) })}/>{code}
          </label>)}</div>
        </div>
      </div>
    </fieldset>)}
    {rules.length > 0 && <p className="field-hint">Switches nodes immediately. Same-node retries back off briefly. Ignores Retry-After; keeps region and filters. Stops at the retry limit, total timeout or when no other nodes remain.</p>}
  </section>;
}
