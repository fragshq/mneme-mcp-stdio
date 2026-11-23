// Mneme workbench dashboard. Plain ES modules, no build step: the Go
// binary embeds this directory and serves it next to the JSON API (/api).

import { h, json, put, when } from "./dom.js";
import { buildForm, FormError } from "./form.js";
import { openDefinitionEditor, openDeleteType } from "./typeeditor.js";

const $ = (id) => document.getElementById(id);
const PAGE_SIZES = [10, 25, 50, 100, 250];

// savedPageSize is the page size picked last time, if it is still offered.
function savedPageSize() {
  try {
    const n = Number(localStorage.getItem("mneme.pageSize"));
    return PAGE_SIZES.includes(n) ? n : 50;
  } catch {
    return 50;
  }
}

const state = {
  tenants: [],
  tenant: "",
  types: [],
  type: "", // selected record type name
  tab: "records",
  latest: null, // RecordTypeDetails of the latest version
  shown: null, // RecordTypeDetails of the version on the Definition tab
  items: [],
  cursor: "",
  limit: savedPageSize(), // records per page
  sort: "", // e.g. "-/total" or "$id"
  labelFilter: new Set(),
  filter: "", // advanced filter JSON as typed
  labels: [], // [{label, count}]
  loading: false,
};

// ---- API -----------------------------------------------------------------

async function api(path, { method = "GET", params = {}, body } = {}) {
  const url = new URL(`api${path}`, document.baseURI);
  for (const [k, v] of Object.entries({ tenant: state.tenant, ...params })) {
    if (v !== undefined && v !== null && v !== "") url.searchParams.set(k, v);
  }
  const res = await fetch(url, {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const err = new Error(data?.error || `${res.status} ${res.statusText}`);
    err.status = res.status;
    err.issues = data?.issues || [];
    throw err;
  }
  return data;
}

const typePath = (name) => `/record-types/${encodeURIComponent(name)}`;

// ---- navigation (state lives in the URL hash) -----------------------------

function writeHash() {
  const p = new URLSearchParams();
  if (state.tenant) p.set("tenant", state.tenant);
  if (state.type) p.set("type", state.type);
  if (state.tab !== "records") p.set("tab", state.tab);
  const next = `#${p}`;
  if (location.hash !== next) history.replaceState(null, "", next);
}

function readHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  return { tenant: p.get("tenant") ?? undefined, type: p.get("type") || "", tab: p.get("tab") || "records" };
}

// ---- feedback ---------------------------------------------------------------

let toastTimer;
function toast(message, isError = false) {
  const t = $("toast");
  t.textContent = message;
  t.className = isError ? "toast error" : "toast";
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, isError ? 6000 : 2500);
}

const fail = (err) => toast(err.message || String(err), true);

// ---- tenants ----------------------------------------------------------------

async function loadTenants(wanted) {
  const { tenants } = await api("/tenants");
  state.tenants = tenants;
  const current = tenants.find((t) => t.current)?.name ?? "";
  state.tenant = wanted !== undefined && tenants.some((t) => t.name === wanted) ? wanted : current;
  const select = $("tenant");
  put(select, ...tenants.map((t) =>
    h("option", { value: t.name, selected: t.name === state.tenant },
      `${t.name || "(default space)"}${t.current ? "  •  MCP server" : ""}`)));
}

async function selectTenant(name) {
  state.tenant = name;
  state.type = "";
  closeDrawer();
  await loadTypes();
  renderWelcome();
  writeHash();
}

// ---- record types -------------------------------------------------------------

async function loadTypes() {
  state.types = await api("/record-types");
  renderTypes();
}

function renderTypes() {
  const q = $("type-search").value.trim().toLowerCase();
  const nav = $("types");
  const shown = state.types.filter((t) => !q || t.name.includes(q) || t.title.toLowerCase().includes(q));
  put(nav, ...shown.map((t) =>
    h("a", { href: "#", class: t.name === state.type ? "active" : "", onclick: (e) => { e.preventDefault(); selectType(t.name).catch(fail); } },
      h("div", { class: "name" }, t.name, h("span", { class: "badge" }, `v${t.latestVersion}`)),
      t.title ? h("div", { class: "title" }, t.title) : null)));
  if (!shown.length) nav.append(h("p", { class: "muted" }, state.types.length ? "No match." : "No record types in this tenant."));
}

async function selectType(name, tab = "records") {
  state.type = name;
  state.tab = tab;
  state.latest = await api(typePath(name));
  state.shown = state.latest;
  state.items = [];
  state.cursor = "";
  state.sort = "";
  state.labelFilter = new Set();
  state.filter = "";
  closeDrawer();
  renderTypes();
  renderType();
  writeHash();
  if (state.tab === "records") await Promise.all([loadRecords(), loadLabels()]);
}

function renderWelcome() {
  const tenant = state.tenants.find((t) => t.name === state.tenant);
  put($("main"), h("div", { class: "empty" },
    h("h1", {}, state.tenant || "Default space"),
    h("p", {}, tenant?.current ? "This is the tenant the MCP server works on." : "Browsing another tenant than the MCP server's."),
    h("p", {}, state.types.length ? "Pick a record type on the left, or define a new one." : "This tenant has no record types yet."),
    h("button", { class: "btn primary", onclick: newRecordType }, "+ New record type")));
}

// ---- record type design -------------------------------------------------------------

function definitionEditor(latest) {
  openDefinitionEditor($("editor"), {
    latest,
    tenant: state.tenant,
    save: (body) => api("/record-types", { method: "POST", body }),
    validate: (body) => api("/record-types/validate", { method: "POST", body }),
    onSaved: async (d) => {
      toast(`Defined ${d.name} v${d.version}`);
      await loadTypes();
      await selectType(d.name, "definition");
    },
  });
}

const newRecordType = () => definitionEditor(null);
const newVersion = () => definitionEditor(state.latest.definition);

function deleteRecordType() {
  const name = state.type;
  openDeleteType($("editor"), {
    name,
    versions: state.latest.versions.length,
    tenant: state.tenant,
    remove: () => api(typePath(name), { method: "DELETE" }),
    onDeleted: async () => {
      toast(`Deleted record type ${name}`);
      state.type = "";
      closeDrawer();
      await loadTypes();
      renderWelcome();
      writeHash();
    },
  });
}

function renderType() {
  const d = state.latest.definition;
  const tabs = h("div", { class: "tabs" },
    ...[["records", "Records"], ["definition", "Definition"]].map(([key, label]) =>
      h("button", { class: state.tab === key ? "active" : "", onclick: () => switchTab(key).catch(fail) }, label)));
  const body = h("div", { id: "tab-body" });
  put($("main"), 
    h("div", { class: "page-head" },
      h("div", {},
        h("h1", {}, d.title || d.name, " ", h("span", { class: "badge" }, `v${d.version}`)),
        h("p", {}, d.title ? h("code", {}, d.name) : null, d.title && d.description ? " — " : null, d.description || null)),
      h("div", { class: "head-actions" },
        h("button", { class: "btn", onclick: newVersion, title: "Define the next version of this record type" }, "New version"),
        h("button", { class: "btn primary", onclick: () => openEditor() }, "+ New record"))),
    tabs, body);
  if (state.tab === "records") renderRecords();
  else renderDefinition();
}

async function switchTab(tab) {
  state.tab = tab;
  writeHash();
  renderType();
  if (tab === "records" && !state.items.length) await Promise.all([loadRecords(), loadLabels()]);
}

// ---- records -------------------------------------------------------------------

// columns picks up to 6 top-level fields: name-like ones first, then those
// the latest version requires, then scalars, then fields of the latest
// version; fields only older versions declare come last.
function columns() {
  const latest = state.latest.definition.version;
  const required = new Set(state.latest.definition.schema.required || []);
  const scalar = (f) => f.types.some((t) => ["string", "number", "integer", "boolean"].includes(t));
  const score = (f) => {
    const name = unptr(f.path.slice(1));
    return (/^(title|name|label|subject|summary)$/i.test(name) ? 0 : 8)
      + (required.has(name) ? 0 : 4) + (scalar(f) ? 0 : 2) + (f.versions.includes(latest) ? 0 : 16);
  };
  return state.latest.fields
    .filter((f) => /^\/[^/]+$/.test(f.path))
    .map((f) => ({ f, s: score(f) }))
    .sort((a, b) => a.s - b.s || a.f.path.localeCompare(b.f.path))
    .slice(0, 6)
    .map(({ f }) => ({ key: f.path, label: unptr(f.path.slice(1)) }));
}

const unptr = (token) => token.replaceAll("~1", "/").replaceAll("~0", "~");

function filterParam() {
  const parts = [...state.labelFilter].map((l) => ({ op: "contains", field: "$labels", value: l }));
  if (state.filter.trim()) parts.push(JSON.parse(state.filter));
  if (!parts.length) return "";
  return JSON.stringify(parts.length === 1 ? parts[0] : { op: "and", filters: parts });
}

function advancedFilter() {
  return state.filter.trim() ? JSON.stringify(JSON.parse(state.filter)) : "";
}

async function loadRecords(append = false) {
  state.loading = true;
  try {
    const page = await api(`${typePath(state.type)}/records`, {
      params: { filter: filterParam(), sort: state.sort, limit: state.limit, cursor: append ? state.cursor : "" },
    });
    state.items = append ? [...state.items, ...page.items] : page.items;
    state.cursor = page.nextCursor;
    state.total = page.total;
  } finally {
    state.loading = false;
  }
  if (state.tab === "records") renderRecords();
}

async function loadLabels() {
  state.labels = await api(`${typePath(state.type)}/labels`, { params: { filter: advancedFilter() } });
  if (state.tab === "records") renderRecords();
}

function renderRecords() {
  const body = $("tab-body");
  if (!body) return;
  const cols = columns();

  const labelbar = h("div", { class: "labelbar" },
    state.labels.length ? h("span", { class: "muted" }, "Labels:") : null,
    ...state.labels.map(({ label, count }) => h("button", {
      class: `chip${state.labelFilter.has(label) ? " on" : ""}`,
      title: state.labelFilter.has(label) ? "Remove from the filter" : "Show only records with this label",
      onclick: () => {
        if (state.labelFilter.has(label)) state.labelFilter.delete(label); else state.labelFilter.add(label);
        loadRecords().catch(fail);
      },
    }, label, h("span", { class: "count" }, String(count)))));

  const filterArea = h("textarea", { rows: 3, spellcheck: false, placeholder: '{"op":"eq","field":"status","value":"open"}' }, state.filter);
  const filterBox = h("details", { class: "filter-box", open: !!state.filter },
    h("summary", {}, "Filter", state.filter ? " (active)" : ""),
    filterArea,
    h("div", { class: "row" },
      h("button", { class: "btn small", onclick: () => applyFilter(filterArea.value) }, "Apply"),
      h("button", { class: "btn small", onclick: () => applyFilter("") }, "Clear"),
      h("span", { class: "muted" }, "Ops: eq ne gt gte lt lte in contains prefix exists and or not. Fields: name, owner.id, /ptr, $id $version $createdAt $updatedAt $labels.")));

  const sortIcon = (key) => state.sort === key ? " ▲" : state.sort === `-${key}` ? " ▼" : "";
  const th = (key, label) => h("th", { class: "sortable", title: "Sort", onclick: () => cycleSort(key) }, label, sortIcon(key));
  const latest = state.latest.definition.version;
  const rows = state.items.map((r) => h("tr", { onclick: () => openDrawer(r) },
    h("td", {}, h("div", { class: "cell mono" }, r.id)),
    ...cols.map((c) => {
      const key = c.label;
      const has = r.data && Object.hasOwn(r.data, key);
      return h("td", { class: has ? "" : "missing" }, h("div", { class: "cell" }, has ? preview(r.data[key]) : "—"));
    }),
    h("td", {}, ...r.labels.map((l) => h("span", { class: "chip" }, l))),
    h("td", {}, h("span", { class: `badge${r.schemaVersion < latest ? " old" : ""}`, title: r.schemaVersion < latest ? "Written with an older schema version" : "" }, `v${r.schemaVersion}`)),
    h("td", { class: "muted" }, when(r.updatedAt))));

  put(body, 
    labelbar,
    filterBox,
    state.items.length || state.loading
      ? h("div", { class: "table-wrap" }, h("table", {},
        h("thead", {}, h("tr", {}, th("$id", "ID"), ...cols.map((c) => th(c.key, c.label)), h("th", {}, "Labels"), th("$version", "Ver."), th("$updatedAt", "Updated"))),
        h("tbody", {}, ...rows)))
      : h("div", { class: "empty" }, state.labelFilter.size || state.filter ? "No records match." : "No records yet.",
        h("div", { style: "margin-top:10px" }, h("button", { class: "btn primary", onclick: () => openEditor() }, "+ New record"))),
    state.cursor ? h("div", { class: "more" }, h("button", { class: "btn", onclick: () => loadRecords(true).catch(fail) }, "Load more")) : null,
    h("div", { class: "page-footer muted" },
      h("span", {}, `${state.items.length} of ${state.total ?? state.items.length} record${state.total === 1 ? "" : "s"} shown.`),
      h("label", {}, "Per page ",
        h("select", { onchange: (e) => setPageSize(Number(e.target.value)) },
          ...PAGE_SIZES.map((n) => h("option", { value: String(n), selected: n === state.limit }, String(n)))))));
}

function preview(v) {
  if (v === null) return "null";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

function applyFilter(text) {
  if (text.trim()) {
    try {
      JSON.parse(text);
    } catch (e) {
      return fail(new Error(`Filter is not valid JSON: ${e.message}`));
    }
  }
  state.filter = text;
  Promise.all([loadRecords(), loadLabels()]).catch(fail);
}

function setPageSize(n) {
  state.limit = n;
  try {
    localStorage.setItem("mneme.pageSize", String(n));
  } catch {
    // not remembered; it still applies now
  }
  loadRecords().catch(fail);
}

function cycleSort(key) {
  state.sort = state.sort === key ? `-${key}` : state.sort === `-${key}` ? "" : key;
  loadRecords().catch(fail);
}

// ---- record drawer -------------------------------------------------------------

function openDrawer(r) {
  const latest = state.latest.definition.version;
  const d = $("drawer");
  d.hidden = false;
  put(d, 
    h("div", { class: "head" },
      h("div", {}, h("div", { class: "muted" }, state.type), h("h2", { class: "mono" }, r.id)),
      h("button", { class: "btn small", onclick: closeDrawer, title: "Close (Esc)" }, "✕")),
    h("dl", { class: "meta" },
      h("dt", {}, "Schema version"), h("dd", {}, `v${r.schemaVersion}`, r.schemaVersion < latest ? h("span", { class: "muted" }, ` (latest is v${latest})`) : null),
      h("dt", {}, "Created"), h("dd", {}, when(r.createdAt)),
      h("dt", {}, "Updated"), h("dd", {}, when(r.updatedAt)),
      h("dt", {}, "Labels"), h("dd", {}, r.labels.length ? r.labels.map((l) => h("span", { class: "chip" }, l)) : h("span", { class: "muted" }, "none"))),
    h("div", { class: "actions" },
      h("button", { class: "btn primary", onclick: () => openEditor(r) }, "Edit"),
      h("button", { class: "btn danger", onclick: () => removeRecord(r).catch(fail) }, "Delete"),
      h("button", { class: "btn", onclick: () => navigator.clipboard?.writeText(JSON.stringify(r.data, null, 2)).then(() => toast("Data copied")) }, "Copy JSON")),
    json(r.data));
}

function closeDrawer() {
  $("drawer").hidden = true;
}

async function removeRecord(r) {
  if (!confirm(`Delete ${state.type} ${r.id} permanently?`)) return;
  await api(`${typePath(state.type)}/records/${encodeURIComponent(r.id)}`, { method: "DELETE" });
  closeDrawer();
  toast(`Deleted ${r.id}`);
  await Promise.all([loadRecords(), loadLabels()]);
}

// ---- definition tab ---------------------------------------------------------------

function renderDefinition() {
  const body = $("tab-body");
  const d = state.shown.definition;
  const versions = state.latest.versions;
  const picker = h("select", {
    onchange: async (e) => {
      const v = Number(e.target.value);
      state.shown = v === state.latest.definition.version ? state.latest : await api(typePath(state.type), { params: { version: v } });
      renderDefinition();
    },
  }, ...[...versions].reverse().map((v) => h("option", { value: String(v), selected: v === d.version }, `v${v}${v === state.latest.definition.version ? " (latest)" : ""}`)));

  put(body, 
    h("div", { class: "toolbar" }, h("span", {}, "Version"), picker,
      h("div", { class: "spacer" }),
      h("button", { class: "btn", onclick: newVersion }, "New version…"),
      h("button", { class: "btn danger", onclick: deleteRecordType }, "Delete record type…")),
    h("dl", { class: "meta" },
      h("dt", {}, "Name"), h("dd", { class: "mono" }, d.name),
      h("dt", {}, "Title"), h("dd", {}, d.title || h("span", { class: "muted" }, "—")),
      h("dt", {}, "Description"), h("dd", {}, d.description || h("span", { class: "muted" }, "—")),
      h("dt", {}, "Defined"), h("dd", {}, when(d.createdAt)),
      h("dt", {}, "ID strategy"), h("dd", {}, d.idStrategy ? h("code", {}, JSON.stringify(d.idStrategy)) : h("span", { class: "muted" }, "default (UUIDv7)")),
      h("dt", {}, "Indexes"), h("dd", {}, d.indexes.length ? d.indexes.map((i) => h("code", { class: "chip" }, i)) : h("span", { class: "muted" }, "none"))),
    h("h2", {}, "JSON Schema"),
    json(d.schema),
    h("h2", {}, "Fields across all versions"),
    h("p", { class: "muted" }, "A field is required only if every version requires it, since older records may lack it."),
    h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, ...["Field", "Types", "Required", "Versions", "Description"].map((c) => h("th", {}, c)))),
      h("tbody", {}, ...state.latest.fields.map((f) => h("tr", { style: "cursor:default" },
        h("td", { class: "mono" }, f.path),
        h("td", {}, f.types.join(" | ")),
        h("td", {}, f.required ? "yes" : ""),
        h("td", {}, f.versions.map((v) => `v${v}`).join(", ")),
        h("td", { class: "muted" }, f.description)))))));
}

// ---- editor -------------------------------------------------------------------------

function openEditor(record) {
  const dlg = $("editor");
  const def = state.latest.definition;
  const editing = !!record;
  let mode = "form";
  let form = buildForm(def.schema, record?.data);
  const jsonArea = h("textarea", { class: "json-input", rows: 18, spellcheck: false });
  const errors = h("div", { class: "errors", hidden: true });
  const formHost = h("div", {}, form.el);
  const jsonHost = h("div", { hidden: true }, jsonArea);

  // labels editor: chips plus an input with suggestions from existing labels
  const labels = new Set(record?.labels || []);
  const chips = h("span");
  const labelInput = h("input", { list: "label-suggestions", placeholder: "add label, press Enter" });
  const suggestions = h("datalist", { id: "label-suggestions" }, ...state.labels.map((l) => h("option", { value: l.label })));
  const renderChips = () => put(chips, ...[...labels].sort().map((l) =>
    h("span", { class: "chip" }, l, h("button", { type: "button", class: "x", title: "Remove", onclick: () => { labels.delete(l); renderChips(); } }, "×"))));
  const addLabel = () => {
    for (const l of labelInput.value.split(",").map((s) => s.trim()).filter(Boolean)) labels.add(l);
    labelInput.value = "";
    renderChips();
  };
  labelInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === ",") { e.preventDefault(); addLabel(); }
    if (e.key === "Backspace" && !labelInput.value && labels.size) { labels.delete([...labels].sort().pop()); renderChips(); }
  });
  labelInput.addEventListener("change", addLabel);
  renderChips();

  const setMode = (next) => {
    if (next === mode) return;
    if (next === "json") {
      try {
        jsonArea.value = JSON.stringify(form.getValue() ?? {}, null, 2);
      } catch (e) {
        return showErrors([], e.message);
      }
    } else {
      let parsed;
      try {
        parsed = JSON.parse(jsonArea.value || "{}");
      } catch (e) {
        return showErrors([], `Fix the JSON first: ${e.message}`);
      }
      form = buildForm(def.schema, parsed);
      put(formHost, form.el);
    }
    mode = next;
    errors.hidden = true;
    formHost.hidden = mode !== "form";
    jsonHost.hidden = mode !== "json";
    modeButtons.forEach((b) => b.classList.toggle("active", b.dataset.mode === mode));
  };
  const modeButtons = [["form", "Form"], ["json", "JSON"]].map(([m, label]) =>
    h("button", { type: "button", class: m === mode ? "active" : "", dataset: { mode: m }, onclick: () => setMode(m) }, label));

  function showErrors(issues, message) {
    const loose = mode === "form" ? form.showErrors(issues) : issues;
    errors.hidden = false;
    put(errors, h("strong", {}, message || "The record was not saved."),
      loose.length ? h("ul", {}, ...loose.map((i) => h("li", {}, h("code", {}, i.path || "/"), " ", i.message))) : null);
    errors.scrollIntoView({ block: "nearest" });
  }

  const save = h("button", { type: "submit", class: "btn primary" }, editing ? "Save changes" : "Insert record");
  const submit = async (e) => {
    e.preventDefault();
    addLabel();
    let data;
    try {
      if (mode === "form") {
        form.clearErrors();
        data = form.getValue() ?? {};
      } else {
        data = JSON.parse(jsonArea.value || "{}");
      }
    } catch (err) {
      if (err instanceof FormError) return showErrors([{ path: err.path, message: err.message }], "Some fields are not valid.");
      return showErrors([], `Invalid JSON: ${err.message}`);
    }
    if (!data || typeof data !== "object" || Array.isArray(data)) return showErrors([], "A record must be a JSON object.");
    save.disabled = true;
    try {
      const body = { data, labels: [...labels] };
      const saved = editing
        ? await api(`${typePath(state.type)}/records/${encodeURIComponent(record.id)}`, { method: "PUT", body })
        : await api(`${typePath(state.type)}/records`, { method: "POST", body });
      dlg.close();
      toast(editing ? `Saved ${saved.id}` : `Inserted ${saved.id}`);
      await Promise.all([loadRecords(), loadLabels()]);
      openDrawer(saved);
    } catch (err) {
      if (err.status === 400 && err.issues.length) showErrors(err.issues, `Not valid against ${def.name} v${def.version}: see the highlighted fields.`);
      else showErrors([], err.message);
    } finally {
      save.disabled = false;
    }
  };

  const older = editing && record.schemaVersion < def.version;
  put(dlg, h("form", { method: "dialog", onsubmit: submit, novalidate: true },
    h("div", { class: "head" },
      h("h2", {}, editing ? `Edit ${record.id}` : `New ${def.title || def.name} record`),
      h("p", { class: "muted" }, `Validated against ${def.name} v${def.version} (latest)${state.tenant ? ` · tenant ${state.tenant}` : ""}`)),
    h("div", { class: "body" },
      older ? h("div", { class: "note" }, `This record was written with v${record.schemaVersion}. Saving replaces it entirely and moves it to v${def.version}.`) : null,
      errors,
      formHost,
      jsonHost,
      h("div", { class: "field" },
        h("label", {}, "Labels", h("span", { class: "type" }, "outside the schema")),
        h("div", { class: "labels-editor", onclick: () => labelInput.focus() }, chips, labelInput),
        suggestions)),
    h("div", { class: "foot" },
      h("div", { class: "mode" }, ...modeButtons),
      h("div", { class: "spacer" }),
      h("button", { type: "button", class: "btn", onclick: () => dlg.close() }, "Cancel"),
      save)));
  dlg.showModal();
  dlg.querySelector(".body input, .body select, .body textarea")?.focus();
}

// ---- boot ------------------------------------------------------------------------

async function boot() {
  const wanted = readHash();
  await loadTenants(wanted.tenant);
  await loadTypes();
  if (wanted.type && state.types.some((t) => t.name === wanted.type)) await selectType(wanted.type, wanted.tab);
  else renderWelcome();
  writeHash();
}

$("tenant").addEventListener("change", (e) => selectTenant(e.target.value).catch(fail));
$("type-search").addEventListener("input", renderTypes);
$("new-type").addEventListener("click", newRecordType);
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !$("editor").open) closeDrawer();
});
boot().catch(fail);
