"use strict";
const element = id => document.getElementById(id);
let token = "", cursor = "", currentAfterID = "", busy = false, timer = null, expiry = null, generation = 0;
function notice(text, error = false) { element("notice").textContent = text; element("notice").className = error ? "error" : ""; }
function clearData() { for (const id of ["overview", "runs"]) element(id).tBodies[0].replaceChildren(); element("detail").hidden = true; element("more").disabled = true; cursor = ""; currentAfterID = ""; }
function disconnect() { token = ""; generation++; clearInterval(timer); timer = null; clearTimeout(expiry); expiry = null; element("refresh").disabled = true; element("state").disabled = false; clearData(); notice("Disconnected."); }
async function api(path) {
  const expected = generation;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  try {
    const response = await fetch(path, {headers:{Authorization:"Bearer " + token}, cache:"no-store", credentials:"omit", signal:controller.signal});
    if (expected !== generation) throw new Error("Session changed");
    if (response.status === 401 || response.status === 403) { disconnect(); throw new Error("Authorization denied. Sign in again with diagnostics.read."); }
    if (!response.ok) throw new Error("Request failed: HTTP " + response.status + " · request " + (response.headers.get("X-Request-ID") || "unavailable"));
    const value = await response.json();
    if (expected !== generation) throw new Error("Session changed");
    return value;
  } finally { clearTimeout(timeout); }
}
function row(table, values) { const tr = document.createElement("tr"); for (const value of values) { const td = document.createElement("td"); if (value instanceof Node) td.append(value); else td.textContent = String(value ?? ""); tr.append(td); } table.append(tr); }
async function showDetail(id) {
  try { const detail = await api("/v1/admin/diagnostics/runs/" + encodeURIComponent(id)); element("detail-json").textContent = JSON.stringify(detail, null, 2); element("detail-notice").textContent = "Safe metadata only. Partial lists are marked in the response."; element("detail").hidden = false; }
  catch (error) { notice(error.message, true); }
}
async function loadRuns(after = "") {
  const query = new URLSearchParams({state:element("state").value, limit:"50"}); if (after) query.set("after_id", after);
  const page = await api("/v1/admin/diagnostics/runs?" + query);
  const tbody = element("runs").tBodies[0]; tbody.replaceChildren();
  for (const run of page.items) { const button = document.createElement("button"); button.textContent = run.id; button.addEventListener("click", () => showDetail(run.id)); row(tbody,[button,run.state,run.created_at,run.reserved_microcredits,run.spent_microcredits,[run.pause_requested ? "pause" : "", run.cancel_requested ? "cancel" : ""].filter(Boolean).join(", ")]); }
  cursor = page.next_cursor || ""; element("more").disabled = !cursor;
}
async function refresh(after = currentAfterID) {
  if (!token || busy) return;
  busy = true; element("refresh").disabled = true; element("state").disabled = true;
  try {
    const snapshot = await api("/v1/admin/diagnostics"); const tbody = element("overview").tBodies[0]; tbody.replaceChildren();
    for (const [key,value] of Object.entries(snapshot.overview)) { if (typeof value === "object" && value !== null) { for (const [name,count] of Object.entries(value)) row(tbody,[key + "." + name,count]); } else row(tbody,[key,value]); }
    row(tbody,["runtime.sampled_at",snapshot.runtime.sampled_at]);
    for (const [name,up] of Object.entries(snapshot.runtime.dependencies || {})) row(tbody,[name,up ? "available" : "unavailable"]);
    await loadRuns(after); currentAfterID = after; notice("Updated " + new Date().toLocaleTimeString() + ". Refreshes every 15 seconds; no prompt or credential contents.");
  } catch (error) { notice(error.message, true); }
  finally { busy = false; element("refresh").disabled = !token; element("state").disabled = false; }
}
element("login").addEventListener("click", () => { window.location.assign("/admin/login"); });
async function initializeLogin() {
  const expected = generation;
  const callback = new URLSearchParams(window.location.search);
  const code = callback.get("code"), state = callback.get("state"), denied = callback.has("error");
  if (code || state || denied) history.replaceState(null, "", "/admin");
  try {
    const configuration = await fetch("/admin/login/config", {cache:"no-store", credentials:"same-origin"});
    if (!configuration.ok) throw new Error("Login configuration unavailable.");
    const settings = await configuration.json();
    element("login").disabled = !settings.enabled;
    if (expected !== generation) return;
    if (!settings.enabled) { notice("Zitadel login is not configured."); return; }
    if (denied) throw new Error("Sign-in was denied. Try again.");
    if (!code && !state) { notice("Sign in to inspect diagnostics for your organization."); return; }
    if (!code || !state) throw new Error("Invalid sign-in callback. Try again.");
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 15000);
    let response;
    try { response = await fetch("/admin/login/exchange", {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify({code,state}), credentials:"same-origin", cache:"no-store", signal:controller.signal}); }
    finally { clearTimeout(timeout); }
    if (!response.ok) throw new Error(response.status === 403 ? "Diagnostics permission required. Sign in with an authorized account." : "Sign-in failed or expired. Try again.");
    const credential = await response.json();
    if (expected !== generation) return;
    disconnect(); token = credential.access_token;
    expiry = setTimeout(() => { disconnect(); notice("Sign-in expired. Sign in again."); }, Math.min(credential.expires_in * 1000, 2147483647));
    await refresh(); if (token) timer = setInterval(() => refresh(), 15000);
  } catch (error) { if (expected !== generation) return; notice(error.name === "AbortError" ? "Sign-in timed out. Try again." : error.message, true); }
}
initializeLogin();
element("disconnect").addEventListener("click", disconnect);
element("refresh").addEventListener("click", () => refresh());
element("state").addEventListener("change", () => { currentAfterID = ""; refresh(); });
element("more").addEventListener("click", () => refresh(cursor));
