"use strict";

const ui = {};
let state = null;
let selectedRunID = 0;
let activeView = location.hash.slice(1) || "updates";

document.addEventListener("DOMContentLoaded", () => {
  for (const id of ["loading-state","freshness","catalog-count","open-count","issue-count","metric-products","metric-active","metric-withdrawn","metric-run","catalog-finding","catalog-search","status-filter","catalog-result-count","catalog-body","catalog-empty","update-finding","abort-button","resume-button","run-source","run-status","batch-progress","checkpoint-strip","last-error","scenario-select","scenario-description","outage-select","start-form","start-button","counter-grid","run-select","run-history-list","report-counter-grid","changes-count","changes-list","issues-count","issues-list","menu-toggle","sidebar","mobile-title","reset-open","reset-dialog","reset-confirm","reset-button","toast"]) ui[id] = document.getElementById(id);
  document.querySelectorAll("[data-view]").forEach((button) => button.addEventListener("click", () => showView(button.dataset.view)));
  document.querySelectorAll("[data-go]").forEach((button) => button.addEventListener("click", () => showView(button.dataset.go)));
  ui["catalog-search"].addEventListener("input", renderCatalog);
  ui["status-filter"].addEventListener("change", renderCatalog);
  ui["scenario-select"].addEventListener("change", renderScenario);
  ui["start-form"].addEventListener("submit", startUpdate);
  ui["resume-button"].addEventListener("click", () => mutate("/api/resume", {}, "Update resumed and completed."));
  ui["abort-button"].addEventListener("click", () => mutate("/api/abort", {}, "Open update aborted."));
  ui["run-select"].addEventListener("change", () => { selectedRunID = Number(ui["run-select"].value); renderReport(); });
  ui["menu-toggle"].addEventListener("click", toggleMenu);
  ui["reset-open"].addEventListener("click", () => { ui["reset-confirm"].value = ""; ui["reset-dialog"].showModal(); });
  ui["reset-button"].addEventListener("click", resetSession);
  window.addEventListener("hashchange", () => showView(location.hash.slice(1) || "updates", false));
  showView(activeView, false);
  loadState();
});

async function loadState() {
  setBusy(true);
  try {
    const response = await fetch("/api/state", {headers: {Accept: "application/json"}});
    if (!response.ok) throw new Error("Could not load the persisted catalog.");
    state = await response.json();
    selectedRunID = state.latest_run ? state.latest_run.id : 0;
    renderAll();
  } catch (error) {
    showToast(error.message, true);
    ui.freshness.textContent = "Catalog unavailable";
  } finally {
    setBusy(false);
  }
}

async function startUpdate(event) {
  event.preventDefault();
  await mutate("/api/start", {scenario: ui["scenario-select"].value, fail_after: Number(ui["outage-select"].value)}, "Update completed.");
}

async function mutate(path, body, successMessage) {
  if (!state) return;
  setBusy(true);
  try {
    let nextPath = path;
    let nextBody = body;
    while (nextPath) {
      const response = await fetch(nextPath, {method: "POST", headers: {"Content-Type": "application/json", "X-CSRF-Token": state.csrf_token, Accept: "application/json"}, body: JSON.stringify(nextBody)});
      const result = await response.json();
      if (result.state) {
        state = result.state;
        selectedRunID = result.run && result.run.id ? result.run.id : (state.latest_run ? state.latest_run.id : 0);
        renderAll();
      }
      if (!response.ok) {
        const message = result.incomplete ? "Update stopped incomplete. Committed batches are safe; use Resume to continue." : result.error;
        showToast(message || "The operation could not be completed.", true);
        return;
      }
      nextPath = result.more ? "/api/continue" : "";
      nextBody = {};
    }
    showToast(successMessage, false);
  } catch (error) {
    showToast("The server could not be reached. Persisted data was not changed.", true);
  } finally {
    setBusy(false);
  }
}

async function resetSession() {
  const confirm = ui["reset-confirm"].value;
  await mutate("/api/reset", {confirm}, "This demo session was reset.");
  if (confirm === "RESET") ui["reset-dialog"].close();
}

function renderAll() {
  renderNavigation();
  renderScenarios();
  renderCatalog();
  renderUpdate();
  renderReportControls();
  renderReport();
  const refreshedAt = new Intl.DateTimeFormat("en", {hour: "2-digit", minute: "2-digit"}).format(new Date());
  ui.freshness.textContent = window.matchMedia("(max-width: 640px)").matches
    ? `Saved · ${refreshedAt}`
    : `Persisted · refreshed ${refreshedAt}`;
}

function renderNavigation() {
  const issueCount = state.latest_run ? state.latest_run.counters.issue_count : 0;
  ui["catalog-count"].textContent = String(state.products.length);
  ui["open-count"].hidden = !state.open_run;
  ui["issue-count"].hidden = issueCount === 0;
  ui["issue-count"].textContent = `${issueCount} issues`;
}

function renderCatalog() {
  if (!state) return;
  const query = ui["catalog-search"].value.trim().toLowerCase();
  const status = ui["status-filter"].value;
  const products = state.products.filter((product) => (!status || product.status === status) && (!query || product.sku.toLowerCase().includes(query) || product.name.toLowerCase().includes(query)));
  const active = state.products.filter((product) => product.status === "active").length;
  ui["metric-products"].textContent = String(state.products.length);
  ui["metric-active"].textContent = String(active);
  ui["metric-withdrawn"].textContent = String(state.products.length - active);
  ui["metric-run"].textContent = state.latest_run ? `#${state.latest_run.id}` : "—";
  ui["catalog-finding"].textContent = state.products.length ? `${active} active products and ${state.products.length - active} retained withdrawal tombstones.` : "Products persisted from committed supplier batches.";
  ui["catalog-result-count"].textContent = `${products.length} ${products.length === 1 ? "product" : "products"}`;
  ui["catalog-body"].replaceChildren(...products.map(productRow));
  ui["catalog-empty"].hidden = products.length > 0;
  document.querySelector(".catalog-table").hidden = products.length === 0;
}

function productRow(product) {
  const tr = document.createElement("tr");
  const name = product.name || "Name unavailable";
  tr.append(cell([el("strong", product.sku), el("span", name)], "product-cell"));
  tr.append(cell([badge(product.status, product.status)]));
  tr.append(cell(product.status === "withdrawn" && !product.name ? "—" : money(product.price_cents), "num"));
  tr.append(cell(String(product.revision), "num"));
  tr.append(cell(`#${product.updated_in_run}`, "num"));
  return tr;
}

function renderScenarios() {
  if (!state || ui["scenario-select"].options.length) return;
  state.scenarios.forEach((scenario) => ui["scenario-select"].append(new Option(scenario.name, scenario.id)));
  renderScenario();
}

function renderScenario() {
  if (!state) return;
  const scenario = state.scenarios.find((item) => item.id === ui["scenario-select"].value) || state.scenarios[0];
  ui["scenario-description"].textContent = scenario.description;
  ui["outage-select"].replaceChildren(new Option("No simulated outage", "-1"));
  for (let batch = 0; batch < scenario.batches; batch += 1) ui["outage-select"].append(new Option(batch === 0 ? "Before batch 1" : `After batch ${batch}`, String(batch)));
}

function renderUpdate() {
  const run = state.open_run || state.latest_run;
  const open = Boolean(state.open_run);
  ui["resume-button"].disabled = !open;
  ui["abort-button"].disabled = !open;
  ui["start-button"].disabled = open;
  ui["scenario-select"].disabled = open;
  ui["outage-select"].disabled = open;
  if (!run) {
    ui["run-source"].textContent = "No update has run in this session.";
    setStatus(ui["run-status"], "Not started", "neutral");
    ui["batch-progress"].textContent = "0 / 0";
    ui["checkpoint-strip"].replaceChildren();
    ui["last-error"].hidden = true;
    ui["counter-grid"].replaceChildren(...counterCells(emptyCounters()));
    return;
  }
  ui["run-source"].textContent = scenarioName(run.source);
  setStatus(ui["run-status"], statusText(run.status), run.status);
  ui["batch-progress"].textContent = `${Math.max(0, run.next_seq - 1)} / ${run.total_batches}`;
  ui["checkpoint-strip"].replaceChildren(...checkpoints(run));
  ui["last-error"].hidden = !run.last_error;
  ui["last-error"].querySelector("p").textContent = run.last_error || "";
  ui["update-finding"].textContent = open ? `Run #${run.id} is incomplete; ${Math.max(0, run.next_seq - 1)} committed batches are safe.` : `Run #${run.id} is ${statusText(run.status).toLowerCase()}.`;
  ui["counter-grid"].replaceChildren(...counterCells(run.counters));
}

function checkpoints(run) {
  const items = [];
  for (let batch = 1; batch <= run.total_batches; batch += 1) {
    const item = document.createElement("span");
    item.className = "checkpoint";
    item.dataset.label = String(batch);
    if (batch < run.next_seq) item.classList.add("is-done");
    else if (batch === run.next_seq && run.status !== "completed" && run.status !== "completed_with_issues") item.classList.add("is-current");
    items.push(item);
  }
  return items;
}

function renderReportControls() {
  const options = state.runs.map((run) => new Option(`Run #${run.id} · ${statusText(run.status)}`, String(run.id)));
  if (!options.length) options.push(new Option("No runs yet", "0"));
  ui["run-select"].replaceChildren(...options);
  if (!selectedRunID && state.latest_run) selectedRunID = state.latest_run.id;
  ui["run-select"].value = String(selectedRunID);
  ui["run-select"].disabled = !state.runs.length;
  ui["run-history-list"].replaceChildren(...state.runs.map(historyButton));
}

function historyButton(run) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = `history-button${run.id === selectedRunID ? " is-selected" : ""}`;
  button.append(el("strong", `Run #${run.id}`), el("span", `${scenarioName(run.source)} · ${Math.max(0, run.next_seq - 1)}/${run.total_batches}`), badge(statusText(run.status), run.status));
  button.addEventListener("click", () => { selectedRunID = run.id; ui["run-select"].value = String(run.id); renderReportControls(); renderReport(); });
  return button;
}

function renderReport() {
  if (!state) return;
  const run = state.runs.find((item) => item.id === selectedRunID);
  if (!run) {
    ui["report-counter-grid"].replaceChildren(...counterCells(emptyCounters()));
    renderEmpty(ui["changes-list"], "No run selected.");
    renderEmpty(ui["issues-list"], "No run selected.");
    return;
  }
  ui["report-counter-grid"].replaceChildren(...counterCells(run.counters));
  const changes = state.changes.filter((item) => item.run_id === run.id);
  const issues = state.issues.filter((item) => item.run_id === run.id);
  ui["changes-count"].textContent = `${changes.length} ${changes.length === 1 ? "change" : "changes"}`;
  ui["issues-count"].textContent = `${issues.length} ${issues.length === 1 ? "issue" : "issues"}`;
  ui["changes-list"].replaceChildren(...(changes.length ? changes.map(changeRow) : [emptyAudit("No recorded changes for this run.")]));
  ui["issues-list"].replaceChildren(...(issues.length ? issues.map(issueRow) : [emptyAudit("No records need attention for this run.")]));
}

function counterCells(counters) {
  return [["Created",counters.created],["Updated",counters.updated],["Withdrawn",counters.withdrawn],["Relisted",counters.relisted],["Unchanged",counters.unchanged],["Stale ignored",counters.stale_ignored],["Issues",counters.issue_count]].map(([label,value]) => { const div=document.createElement("div"); div.append(el("span",label),el("strong",String(value),"num")); return div; });
}

function changeRow(change) {
  const row = document.createElement("div"); row.className = "audit-row";
  const detail = document.createElement("div"); detail.className = "audit-content";
  detail.append(el("strong", titleCase(change.kind)), el("p", change.before ? `${productSummary(change.before)} → ${productSummary(change.after)}` : productSummary(change.after)));
  row.append(el("div", change.sku, "audit-key"), detail); return row;
}

function issueRow(issue) {
  const row = document.createElement("div"); row.className = "audit-row";
  const detail = document.createElement("div"); detail.className = "audit-content";
  detail.append(el("strong", `${titleCase(issue.kind.replaceAll("_", " "))} · batch ${issue.batch_seq}`), el("p", issue.detail), el("pre", JSON.stringify(issue.raw, null, 2), "raw-record"));
  row.append(el("div", issue.sku || "No SKU", "audit-key"), detail); return row;
}

function showView(view, updateHash = true) {
  if (!document.querySelector(`[data-view-panel="${view}"]`)) view = "catalog";
  activeView = view;
  document.querySelectorAll("[data-view-panel]").forEach((panel) => { panel.hidden = panel.dataset.viewPanel !== view; });
  document.querySelectorAll("[data-view]").forEach((button) => button.classList.toggle("is-active", button.dataset.view === view));
  const labels = {catalog:"Catalog",updates:"Updates",report:"Report",guide:"Demo guide"};
  if (ui["mobile-title"]) ui["mobile-title"].textContent = labels[view];
  if (updateHash) history.replaceState(null, "", `#${view}`);
  if (ui.sidebar) { ui.sidebar.classList.remove("is-open"); ui["menu-toggle"].setAttribute("aria-expanded", "false"); }
}

function toggleMenu() { const open=ui.sidebar.classList.toggle("is-open"); ui["menu-toggle"].setAttribute("aria-expanded",String(open)); }
function setBusy(busy) { if (ui["loading-state"]) ui["loading-state"].hidden=!busy; document.body.classList.toggle("is-busy",busy); }
function showToast(message,error) { ui.toast.textContent=message; ui.toast.classList.toggle("error",error); ui.toast.hidden=false; clearTimeout(showToast.timer); showToast.timer=setTimeout(()=>{ui.toast.hidden=true;},5000); }
function setStatus(node,text,kind) { node.textContent=text; node.className=`status-badge ${kind}`; }
function scenarioName(id) { const item=state.scenarios.find((scenario)=>scenario.id===id); return item?item.name:id; }
function statusText(status) { return ({running:"Running",interrupted:"INCOMPLETE",completed:"Completed",completed_with_issues:"Completed with issues",aborted:"Aborted"})[status]||titleCase(status); }
function titleCase(value) { return value ? value.charAt(0).toUpperCase()+value.slice(1) : ""; }
function money(cents) { return `${(cents/100).toFixed(2)}`; }
function productSummary(product) { const name=product.name||"Name unavailable"; const price=product.status==="withdrawn"&&!product.name?"price unavailable":money(product.price_cents); return `${name}, ${price}, ${product.status}, r${product.revision}`; }
function emptyCounters() { return {created:0,updated:0,withdrawn:0,relisted:0,unchanged:0,stale_ignored:0,issue_count:0}; }
function badge(text,kind) { return el("span",text,`status-badge ${kind}`); }
function cell(content,className="") { const td=document.createElement("td"); if(className)td.className=className; if(Array.isArray(content))td.append(...content); else td.textContent=content; return td; }
function el(tag,text,className="") { const node=document.createElement(tag); if(className)node.className=className; node.textContent=text; return node; }
function emptyAudit(message) { return el("div",message,"audit-empty"); }
function renderEmpty(node,message) { node.replaceChildren(emptyAudit(message)); }
