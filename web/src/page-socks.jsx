import React from "react";
import { Ic } from "./icons.jsx";
import { Modal as SocksModal, useToast as useSocksToast } from "./ui.jsx";
import api from "./api.jsx";


function socksForm(account) {
  const e = account?.egress || {};
  let mode = "direct";
  if (e.gateway && e.region) mode = "gateway-region";
  else if (e.gateway) mode = "gateway-direct";
  else if (e.pool && e.node) mode = "node";
  else if (e.any || e.region === "*") mode = "any";
  else if (e.region) mode = "region";
  return {
    username: account?.username || "",
    password: "",
    mode,
    gateway: e.gateway || "",
    node: e.pool && e.node ? `${e.pool}/${e.node}` : "",
    region: e.region && e.region !== "*" ? e.region : "US",
    strategy: e.strategy || "random",
    residential: !!e.residential,
    premium: !!e.premium,
    affinity_ttl: account?.affinity_ttl || "0s",
    enabled: account?.enabled !== false,
  };
}

function socksEgress(form) {
  const base = { strategy: form.strategy, residential: form.residential, premium: form.premium };
  if (form.mode === "direct") return { direct: true };
  if (form.mode === "any") return { ...base, any: true };
  if (form.mode === "node") {
    const separator = form.node.indexOf("/");
    return { pool: form.node.slice(0, separator), node: form.node.slice(separator + 1) };
  }
  if (form.mode === "gateway-direct") return { gateway: form.gateway, direct: true };
  if (form.mode === "gateway-region") return { ...base, gateway: form.gateway, region: form.region.trim().toUpperCase() };
  return { ...base, region: form.region.trim().toUpperCase() };
}

function socksExitLabel(account) {
  const e = account.egress || {};
  if (e.gateway) return `${e.gateway} / ${e.region || "direct"}`;
  if (e.pool) return `${e.pool} / ${e.node}`;
  if (e.any) return `Any / ${e.strategy || "random"}`;
  if (e.region) return `${e.region} / ${e.strategy || "random"}`;
  return "Local direct";
}

function PageSOCKS({ state }) {
  const [data, setData] = React.useState({ accounts: [], advertise_address: "" });
  const [editing, setEditing] = React.useState(null);
  const [form, setForm] = React.useState(socksForm());
  const [busy, setBusy] = React.useState(false);
  const [created, setCreated] = React.useState(null);
  const [endpoint, setEndpoint] = React.useState("");
  const toast = useSocksToast();
  const gateways = state.pools.filter(p => p.source === "template" && p.template_type === "chijie" && p.enabled);
  const nodes = state.pools.flatMap(p => (p.nodes || []).filter(n => n.enabled).map(n => ({ value: `${p.name}/${n.name}`, label: `${p.name} / ${n.name}` })));

  const refresh = React.useCallback(async () => {
    try {
      const result = await api.listSOCKSAccounts();
      setData(result);
      if (result.advertise_address) setEndpoint(result.advertise_address);
    } catch (error) { toast(error.message); }
  }, [toast]);
  React.useEffect(() => { refresh(); }, [refresh]);

  const openCreate = () => { setForm(socksForm()); setEditing("new"); };
  const openEdit = (account) => { setForm(socksForm(account)); setEditing(account.username); };
  const update = (key, value) => setForm(current => ({ ...current, [key]: value }));

  const save = async () => {
    setBusy(true);
    try {
      const account = { username: form.username.trim(), egress: socksEgress(form), affinity_ttl: form.affinity_ttl.trim(), enabled: form.enabled };
      if ((form.mode === "gateway-direct" || form.mode === "gateway-region") && !form.gateway) throw new Error("Select a gateway");
      if (form.mode === "node" && !form.node) throw new Error("Select a node");
      const result = editing === "new"
        ? await api.createSOCKSAccount(account, form.password)
        : await api.updateSOCKSAccount(account, form.password);
      setEditing(null);
      await refresh();
      if (result.password) setCreated({ username: account.username, password: result.password, connection: result.connection || "" });
      toast(editing === "new" ? "SOCKS account created" : "SOCKS account saved");
    } catch (error) { toast(error.message); }
    finally { setBusy(false); }
  };

  const remove = async (account) => {
    if (!window.confirm(`Delete ${account.username}?`)) return;
    try { await api.deleteSOCKSAccount(account.username); await refresh(); toast("SOCKS account deleted"); }
    catch (error) { toast(error.message); }
  };

  const fallbackPort = String(state.stats?.runtime?.socks_listen || ":1080").split(":").pop() || "1080";
  const connection = created ? (endpoint ? `socks5h://${encodeURIComponent(created.username)}:${encodeURIComponent(created.password)}@${endpoint}` : created.connection || `socks5h://${encodeURIComponent(created.username)}:${encodeURIComponent(created.password)}@${window.location.hostname}:${fallbackPort}`) : "";

  return <div className="page">
    <div className="page-h">
      <div><h1>SOCKS accounts</h1><p>One listener, one account per exit policy. TCP and UDP use the same account.</p></div>
      <div className="page-h-actions"><button className="btn primary" onClick={openCreate}><Ic.plus/> New account</button></div>
    </div>
    <div className="card">
      <div className="card-h bordered"><h3>Accounts</h3><span className="sub">{data.accounts.length} configured</span></div>
      {data.accounts.length === 0 ? <div className="card-body muted-2">No accounts. All SOCKS connections are rejected.</div> :
        <div style={{overflowX:"auto"}}><table className="table"><thead><tr><th>Username</th><th>Exit</th><th>Sticky</th><th>Status</th><th></th></tr></thead><tbody>
          {data.accounts.map(account => <tr key={account.username}>
            <td className="mono">{account.username}</td>
            <td className="mono">{socksExitLabel(account)}</td>
            <td className="mono">{account.affinity_ttl || "off"}</td>
            <td><span className="pill">{account.enabled ? "enabled" : "disabled"}</span></td>
            <td><div className="row gap-12"><button className="btn sm ghost" onClick={() => openEdit(account)}>Edit</button><button className="btn sm ghost danger" onClick={() => remove(account)}>Delete</button></div></td>
          </tr>)}
        </tbody></table></div>}
    </div>
    <SocksModal open={!!editing} onClose={() => setEditing(null)} title={editing === "new" ? "New SOCKS account" : `Edit ${editing}`}
      footer={<><button className="btn" onClick={() => setEditing(null)}>Cancel</button><button className="btn primary" disabled={busy} onClick={save}>{busy ? "Saving..." : "Save"}</button></>}>
      <div className="field" style={{marginBottom:14}}><label className="field-label">Username</label><input className="input mono" value={form.username} disabled={editing !== "new"} onChange={e => update("username", e.target.value)} placeholder="bot-home" /></div>
      <div className="field" style={{marginBottom:14}}><label className="field-label">Password</label><input className="input mono" type="password" value={form.password} onChange={e => update("password", e.target.value)} placeholder={editing === "new" ? "Leave empty to generate" : "Leave empty to keep current"} /></div>
      <div className="field" style={{marginBottom:14}}><label className="field-label">Exit</label><select className="select" value={form.mode} onChange={e => update("mode", e.target.value)}>
        <option value="direct">Local direct</option><option value="region">Region</option><option value="any">Any available region</option><option value="node">Fixed node</option><option value="gateway-direct">Gateway direct</option><option value="gateway-region">Gateway region</option>
      </select></div>
      {(form.mode === "region" || form.mode === "gateway-region") && <div className="field" style={{marginBottom:14}}><label className="field-label">Region code</label><input className="input mono" maxLength="2" value={form.region} onChange={e => update("region", e.target.value)} /></div>}
      {(form.mode === "gateway-direct" || form.mode === "gateway-region") && <div className="field" style={{marginBottom:14}}><label className="field-label">Gateway</label><select className="select" value={form.gateway} onChange={e => update("gateway", e.target.value)}><option value="">Select gateway</option>{gateways.map(p => <option key={p.name} value={p.name}>{p.name}</option>)}</select></div>}
      {form.mode === "node" && <div className="field" style={{marginBottom:14}}><label className="field-label">Node</label><select className="select" value={form.node} onChange={e => update("node", e.target.value)}><option value="">Select node</option>{nodes.map(n => <option key={n.value} value={n.value}>{n.label}</option>)}</select></div>}
      {["region", "any", "gateway-region"].includes(form.mode) && <>
        <div className="field" style={{marginBottom:14}}><label className="field-label">Strategy</label><select className="select" value={form.strategy} onChange={e => update("strategy", e.target.value)}><option value="random">Random</option><option value="round-robin">Round robin</option><option value="least-latency">Lowest health-check latency</option></select></div>
        <div className="row gap-12" style={{marginBottom:14}}><label><input type="checkbox" checked={form.residential} onChange={e => update("residential", e.target.checked)} /> Residential</label><label><input type="checkbox" checked={form.premium} onChange={e => update("premium", e.target.checked)} /> Prefer premium</label></div>
      </>}
      <div className="field" style={{marginBottom:14}}><label className="field-label">Sticky duration</label><input className="input mono" value={form.affinity_ttl} onChange={e => update("affinity_ttl", e.target.value)} placeholder="0s or 1h" /><span className="field-hint">New connections keep the same selected node until this duration expires.</span></div>
      <label><input type="checkbox" checked={form.enabled} onChange={e => update("enabled", e.target.checked)} /> Enabled</label>
    </SocksModal>
    <SocksModal open={!!created} onClose={() => setCreated(null)} title="SOCKS connection" footer={<button className="btn primary" onClick={() => setCreated(null)}>Done</button>}>
      <p className="muted-2">The password is shown once. Set the client address if the published host or port differs.</p>
      <div className="field" style={{marginBottom:14}}><label className="field-label">Client address</label><input className="input mono" value={endpoint} onChange={e => setEndpoint(e.target.value)} placeholder={`${window.location.hostname}:${fallbackPort}`} /></div>
      <div className="field"><label className="field-label">Connection URL</label><input className="input mono" value={connection} readOnly onFocus={e => e.target.select()} /></div>
      <button className="btn" style={{marginTop:14}} onClick={async () => { try { await navigator.clipboard.writeText(connection); toast("Copied"); } catch { toast("Select and copy the URL"); } }}>Copy</button>
    </SocksModal>
  </div>;
}

export { PageSOCKS };
