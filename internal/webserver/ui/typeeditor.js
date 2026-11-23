// Record type editor: define a new record type or a new version of one,
// with a visual schema builder (for the common subset of JSON Schema) and a
// JSON mode for everything else; and the confirmation dialog that deletes a
// record type with all its data.

import { h, put } from "./dom.js";

const TYPES = ["string", "number", "integer", "boolean", "object", "array"];
const FORMATS = ["", "date", "date-time", "email", "uri"];
const ID_TEMPLATE = { segments: [{ fixed: "ID-" }, { sequence: { name: "seq", pad: 4 } }] };

// ---- schema <-> builder model ------------------------------------------------
//
// A model field is {name, type, required, description, format, enumText,
// minimum, maximum, items (a field for array items), fields and closed (for
// objects: closed means additionalProperties false, so only the listed
// fields are allowed)}.

const FIELD_KEYS = new Set(["type", "description", "enum", "format", "minimum", "maximum", "items", "properties", "required", "additionalProperties"]);

/** fromSchema returns the builder model for schema ({fields, closed}), or
 * fields null with a reason when the schema uses something the builder
 * can't show. */
export function fromSchema(schema) {
  const why = [];
  const rootKeys = Object.keys(schema || {}).filter((k) => !["type", "properties", "required", "additionalProperties"].includes(k));
  if (!schema || schema.type !== "object") why.push("the root is not a plain object schema");
  if (rootKeys.length) why.push(`it uses ${rootKeys.join(", ")} at the root`);
  const closed = isClosed(schema, "the root", why);
  const fields = why.length ? [] : objectFields(schema, "", why);
  return why.length ? { fields: null, closed: false, why: why.slice(0, 3).join("; ") } : { fields, closed, why: "" };
}

// isClosed reads a boolean additionalProperties; a schema for the extra
// properties is beyond the builder.
function isClosed(node, where, why) {
  const ap = node?.additionalProperties;
  if (ap === undefined || ap === true) return false;
  if (ap === false) return true;
  why.push(`${where} has an additionalProperties schema`);
  return false;
}

function objectFields(node, path, why) {
  const required = new Set(node.required || []);
  return Object.entries(node.properties || {}).map(([name, sub]) => ({
    ...toField(sub, `${path}/${name}`, why),
    name,
    required: required.has(name),
  }));
}

function toField(node, path, why) {
  const f = newField("");
  if (!node || typeof node !== "object") {
    why.push(`${path || "/"} is not an object schema`);
    return f;
  }
  const extra = Object.keys(node).filter((k) => !FIELD_KEYS.has(k));
  if (extra.length) why.push(`${path} uses ${extra.join(", ")}`);
  if (!TYPES.includes(node.type)) why.push(`${path} has type ${JSON.stringify(node.type ?? "(none)")}`);
  f.type = TYPES.includes(node.type) ? node.type : "string";
  f.description = node.description || "";
  if (node.format !== undefined) {
    if (f.type === "string" && FORMATS.includes(node.format)) f.format = node.format;
    else why.push(`${path} has format ${node.format}`);
  }
  if (node.enum !== undefined) {
    if (f.type === "string" && Array.isArray(node.enum) && node.enum.every((v) => typeof v === "string" && !v.includes(","))) f.enumText = node.enum.join(", ");
    else why.push(`${path} has non-text allowed values`);
  }
  for (const k of ["minimum", "maximum"]) {
    if (node[k] === undefined) continue;
    if (f.type === "number" || f.type === "integer") f[k] = String(node[k]);
    else why.push(`${path} has ${k} on a ${f.type}`);
  }
  if (f.type === "array") {
    if (node.items && typeof node.items === "object" && !Array.isArray(node.items)) f.items = toField(node.items, `${path}/items`, why);
    else why.push(`${path} has no single items schema`);
  } else if (node.items !== undefined) why.push(`${path} has items on a ${f.type}`);
  if (f.type === "object") {
    f.fields = objectFields(node, path, why);
    f.closed = isClosed(node, path, why);
  } else if (node.properties || node.required || node.additionalProperties !== undefined) why.push(`${path} has object keywords on a ${f.type}`);
  return f;
}

/** toSchema turns builder fields into a root object schema; closed allows
 * no fields beyond them. */
export function toSchema(fields, closed = false) {
  return objectSchema(fields, closed);
}

function objectSchema(fields, closed) {
  const out = { type: "object", properties: {} };
  const required = [];
  for (const f of fields) {
    out.properties[f.name] = fieldSchema(f);
    if (f.required) required.push(f.name);
  }
  if (required.length) out.required = required;
  if (closed) out.additionalProperties = false;
  return out;
}

function fieldSchema(f) {
  const out = f.type === "object" ? objectSchema(f.fields, f.closed) : { type: f.type };
  if (f.description.trim()) out.description = f.description.trim();
  if (f.type === "string") {
    if (f.format) out.format = f.format;
    const values = f.enumText.split(",").map((v) => v.trim()).filter(Boolean);
    if (values.length) out.enum = values;
  }
  if (f.type === "number" || f.type === "integer") {
    for (const k of ["minimum", "maximum"]) if (f[k] !== "" && Number.isFinite(Number(f[k]))) out[k] = Number(f[k]);
  }
  if (f.type === "array") out.items = fieldSchema(f.items || newField(""));
  return out;
}

const newField = (name) => ({ name, type: "string", required: false, description: "", format: "", enumText: "", minimum: "", maximum: "", items: null, fields: [], closed: false });

// closedToggle edits an object's closed flag through get/set.
const closedToggle = (get, set, text) => h("label", { class: "inline fb-closed" },
  h("input", { type: "checkbox", checked: get(), onchange: (e) => set(e.target.checked) }), text);

// problems lists builder mistakes the server would report less clearly.
function problems(fields, path = "") {
  const out = [];
  const seen = new Set();
  for (const f of fields) {
    const p = `${path}/${f.name || "?"}`;
    if (!f.name.trim()) out.push(`${path || "/"}: a field has no name`);
    else if (seen.has(f.name)) out.push(`${path || "/"}: two fields are named "${f.name}"`);
    seen.add(f.name);
    for (const k of ["minimum", "maximum"]) {
      if ((f.type === "number" || f.type === "integer") && f[k] !== "" && !Number.isFinite(Number(f[k]))) out.push(`${p}: ${k} is not a number`);
    }
    if (f.type === "object") out.push(...problems(f.fields, p));
    if (f.type === "array" && f.items?.type === "object") out.push(...problems(f.items.fields, `${p}/items`));
  }
  return out;
}

// ---- builder UI ------------------------------------------------------------------

// builder renders editable rows for fields (mutated in place); rerender is
// called when the structure changes.
function builder(fields, rerender, depth = 0) {
  const rows = fields.map((f, i) => fieldRow(f, rerender, () => { fields.splice(i, 1); rerender(); }, depth));
  return h("div", { class: depth ? "fb nested" : "fb" },
    rows.length ? rows : h("p", { class: "muted fb-empty" }, depth ? "No fields." : "No fields yet: records may hold anything."),
    h("button", { type: "button", class: "btn small", onclick: () => { fields.push({ ...newField(""), fresh: true }); rerender(); } }, "+ Add field"));
}

function fieldRow(f, rerender, remove, depth) {
  const bind = (key) => (e) => { f[key] = e.target.type === "checkbox" ? e.target.checked : e.target.value; };
  const fresh = f.fresh; // just added: its name input takes the focus once
  delete f.fresh;
  return h("div", { class: "fb-row" },
    h("div", { class: "fb-main" },
      h("input", { class: "fb-name", value: f.name, placeholder: "field name", "aria-label": "Field name", oninput: bind("name"), dataset: fresh ? { fresh: "1" } : {} }),
      typeSelect(f, rerender),
      h("label", { class: "inline fb-req" }, h("input", { type: "checkbox", checked: f.required, onchange: bind("required") }), "required"),
      h("button", { type: "button", class: "btn small danger", title: "Remove field", onclick: remove }, "✕")),
    h("input", { class: "fb-desc", value: f.description, placeholder: "description (shown in forms and to the LLM)", oninput: bind("description") }),
    extras(f, rerender, depth));
}

function typeSelect(f, rerender) {
  return h("select", {
    "aria-label": "Field type",
    onchange: (e) => {
      f.type = e.target.value;
      if (f.type === "array" && !f.items) f.items = newField("");
      rerender();
    },
  }, ...TYPES.map((t) => h("option", { value: t, selected: f.type === t }, t)));
}

// extras renders the settings specific to f's type.
function extras(f, rerender, depth) {
  const bind = (key) => (e) => { f[key] = e.target.value; };
  switch (f.type) {
    case "string":
      return h("div", { class: "fb-extra" },
        h("label", { class: "inline" }, "format",
          h("select", { onchange: bind("format") }, ...FORMATS.map((v) => h("option", { value: v, selected: f.format === v }, v || "text")))),
        h("input", { value: f.enumText, placeholder: "allowed values, comma-separated (optional)", oninput: bind("enumText") }));
    case "number":
    case "integer":
      return h("div", { class: "fb-extra" },
        h("label", { class: "inline fb-bound" }, "min", h("input", { type: "number", step: "any", value: f.minimum, placeholder: "none", oninput: bind("minimum") })),
        h("label", { class: "inline fb-bound" }, "max", h("input", { type: "number", step: "any", value: f.maximum, placeholder: "none", oninput: bind("maximum") })));
    case "object":
      return h("div", { class: "fb-object" },
        closedToggle(() => f.closed, (v) => { f.closed = v; }, "only these fields (reject any other)"),
        builder(f.fields, rerender, depth + 1));
    case "array": {
      const it = f.items || (f.items = newField(""));
      return h("div", { class: "fb-items" },
        h("div", { class: "fb-extra" }, h("span", { class: "muted" }, "each item is a"), typeSelect(it, rerender)),
        extras(it, rerender, depth));
    }
    default:
      return null;
  }
}

// ---- definition editor ---------------------------------------------------------------

/**
 * openDefinitionEditor shows the editor in dlg. With latest (a Definition)
 * it creates the next version of that record type, prefilled; without, a
 * new record type. save(body) stores it; validate(body) checks it.
 */
export function openDefinitionEditor(dlg, { latest, tenant, save, validate, onSaved }) {
  const next = latest ? latest.version + 1 : 1;
  const parsed = fromSchema(latest?.schema ?? { type: "object", properties: {} });
  let mode = parsed.fields ? "visual" : "json";
  let fields = parsed.fields || [];
  let closed = parsed.closed;

  const name = h("input", { value: latest?.name ?? "", placeholder: "e.g. invoice", disabled: !!latest, pattern: "[a-z][a-z0-9_-]{0,62}" });
  const title = h("input", { value: latest?.title ?? "", placeholder: "Human-readable title" });
  const description = h("input", { value: latest?.description ?? "", placeholder: "What the records represent" });
  const indexes = h("input", { value: (latest?.indexes || []).join(", "), placeholder: "fields worth indexing, comma-separated (e.g. status, owner.id)" });

  const custom = !!latest?.idStrategy;
  const idArea = h("textarea", { class: "json-input", rows: 4, spellcheck: false, hidden: !custom },
    JSON.stringify(latest?.idStrategy || ID_TEMPLATE, null, 2));
  const idHelp = h("div", { class: "help", hidden: !custom },
    'Segments: {"fixed":"INV-"} {"date":"2006"} {"sequence":{"name":"inv","pad":4,"scope":"prefix"}} {"uuid":"v7"} {"random":{"length":8}} {"field":"/code"}');
  const idKind = h("select", {
    onchange: () => { idArea.hidden = idKind.value !== "custom"; idHelp.hidden = idArea.hidden; },
  }, h("option", { value: "default", selected: !custom }, "Default: UUIDv7"), h("option", { value: "custom", selected: custom }, "Custom format"));

  const schemaArea = h("textarea", { class: "json-input", rows: 16, spellcheck: false }, JSON.stringify(latest?.schema ?? toSchema(fields, closed), null, 2));
  const visualHost = h("div");
  const jsonHost = h("div", {}, schemaArea);
  const errors = h("div", { class: "errors", hidden: true });
  const notice = h("div", { class: "note", hidden: !!parsed.fields }, parsed.fields ? "" : `Visual editing is off: ${parsed.why}. Edit the schema as JSON.`);

  const renderVisual = () => {
    put(visualHost,
      closedToggle(() => closed, (v) => { closed = v; }, "Records may only hold the fields below (additionalProperties: false)"),
      builder(fields, renderVisual));
    visualHost.querySelector("[data-fresh]")?.focus();
  };

  const showError = (message, list = []) => {
    errors.hidden = false;
    put(errors, h("strong", {}, message), list.length ? h("ul", {}, ...list.map((m) => h("li", {}, m))) : null);
    errors.scrollIntoView({ block: "nearest" });
  };

  const setMode = (m) => {
    if (m === mode) return;
    if (m === "json") {
      schemaArea.value = JSON.stringify(toSchema(fields, closed), null, 2);
    } else {
      let schema;
      try {
        schema = JSON.parse(schemaArea.value);
      } catch (e) {
        return showError(`Fix the JSON first: ${e.message}`);
      }
      const res = fromSchema(schema);
      if (!res.fields) return showError(`The visual editor can't show this schema: ${res.why}.`);
      fields = res.fields;
      closed = res.closed;
      renderVisual();
    }
    mode = m;
    errors.hidden = true;
    notice.hidden = true;
    visualHost.hidden = mode !== "visual";
    jsonHost.hidden = mode !== "json";
    modeButtons.forEach((b) => b.classList.toggle("active", b.dataset.mode === mode));
  };
  const modeButtons = [["visual", "Visual"], ["json", "JSON"]].map(([m, label]) =>
    h("button", { type: "button", class: m === mode ? "active" : "", dataset: { mode: m }, onclick: () => setMode(m) }, label));

  // body assembles the request, or throws Error with a list of problems.
  const body = () => {
    const list = [];
    let schema;
    if (mode === "visual") {
      list.push(...problems(fields));
      schema = toSchema(fields, closed);
    } else {
      try {
        schema = JSON.parse(schemaArea.value);
      } catch (e) {
        list.push(`schema: invalid JSON: ${e.message}`);
      }
    }
    let idStrategy = null;
    if (idKind.value === "custom") {
      try {
        idStrategy = JSON.parse(idArea.value);
      } catch (e) {
        list.push(`ID strategy: invalid JSON: ${e.message}`);
      }
    }
    if (!name.value.trim()) list.push("name: required");
    if (list.length) throw Object.assign(new Error("Fix these first:"), { list });
    return {
      name: name.value.trim(),
      title: title.value.trim(),
      description: description.value.trim(),
      schema,
      idStrategy,
      indexes: indexes.value.split(",").map((s) => s.trim()).filter(Boolean),
    };
  };

  const run = async (fn, button) => {
    let b;
    try {
      b = body();
    } catch (e) {
      return showError(e.message, e.list || []);
    }
    button.disabled = true;
    try {
      await fn(b);
    } catch (e) {
      showError(e.message);
    } finally {
      button.disabled = false;
    }
  };

  const check = h("button", { type: "button", class: "btn" }, "Validate");
  check.addEventListener("click", () => run(async (b) => {
    await validate(b);
    errors.hidden = true;
    put(errors);
    check.textContent = "Valid ✓";
    setTimeout(() => { check.textContent = "Validate"; }, 2000);
  }, check));
  const submit = h("button", { type: "submit", class: "btn primary" }, latest ? `Save as v${next}` : "Create record type");

  renderVisual();
  visualHost.hidden = mode !== "visual";
  jsonHost.hidden = mode !== "json";

  put(dlg, h("form", {
    method: "dialog", novalidate: true,
    onsubmit: (e) => {
      e.preventDefault();
      run(async (b) => {
        const saved = await save(b);
        dlg.close();
        onSaved(saved);
      }, submit);
    },
  },
    h("div", { class: "head" },
      h("h2", {}, latest ? `New version of ${latest.name}` : "New record type"),
      h("p", { class: "muted" }, latest
        ? `Saving creates v${next}. Existing records keep their version; new and updated records must match it.`
        : `Records are validated against its JSON Schema${tenant ? ` · tenant ${tenant}` : ""}.`)),
    h("div", { class: "body" },
      errors,
      h("div", { class: "grid2" },
        h("div", { class: "field" }, h("label", {}, "Name", h("span", { class: "req" }, "*")), name,
          h("div", { class: "help" }, latest ? "A record type's name can't change." : "Lowercase letters, digits, _ or -, starting with a letter.")),
        h("div", { class: "field" }, h("label", {}, "Title"), title)),
      h("div", { class: "field" }, h("label", {}, "Description"), description),
      h("div", { class: "grid2" },
        h("div", { class: "field" }, h("label", {}, "ID strategy"), idKind,
          h("div", { class: "help" }, latest?.idStrategy ? "Keep it unless you mean to change future IDs." : "Records get UUIDv7 IDs unless you need a specific format.")),
        h("div", { class: "field" }, h("label", {}, "Indexes"), indexes)),
      idArea, idHelp,
      h("div", { class: "field" },
        h("div", { class: "schema-head" }, h("span", { class: "label" }, "Schema"), h("div", { class: "mode" }, ...modeButtons)),
        notice, visualHost, jsonHost)),
    h("div", { class: "foot" },
      check,
      h("div", { class: "spacer" }),
      h("button", { type: "button", class: "btn", onclick: () => dlg.close() }, "Cancel"),
      submit)));
  dlg.showModal();
  (latest ? title : name).focus();
}

// ---- delete ------------------------------------------------------------------------

/**
 * openDeleteType asks for the record type's name before remove() deletes
 * it with all its versions, records and ID counters.
 */
export function openDeleteType(dlg, { name, versions, tenant, remove, onDeleted }) {
  const confirmInput = h("input", { placeholder: name, autocomplete: "off", spellcheck: false });
  const go = h("button", { type: "submit", class: "btn danger-solid", disabled: true }, "Delete record type");
  const errors = h("div", { class: "errors", hidden: true });
  confirmInput.addEventListener("input", () => { go.disabled = confirmInput.value !== name; });
  put(dlg, h("form", {
    method: "dialog", novalidate: true,
    onsubmit: async (e) => {
      e.preventDefault();
      if (confirmInput.value !== name) return;
      go.disabled = true;
      try {
        await remove();
        dlg.close();
        onDeleted();
      } catch (err) {
        errors.hidden = false;
        put(errors, err.message);
        go.disabled = false;
      }
    },
  },
    h("div", { class: "head" }, h("h2", {}, `Delete record type ${name}?`)),
    h("div", { class: "body" },
      errors,
      h("p", {}, "This permanently deletes ",
        h("strong", {}, `${versions} version${versions === 1 ? "" : "s"}`), " of its definition, ",
        h("strong", {}, "every record"), " of this type and its ",
        h("strong", {}, "ID counters"), tenant ? ` in tenant ${tenant}` : " in the default space",
        ". It can't be undone. A record type defined later with the same name starts again from v1, with IDs from 1."),
      h("div", { class: "field" }, h("label", {}, "Type ", h("code", {}, name), " to confirm"), confirmInput)),
    h("div", { class: "foot" },
      h("div", { class: "spacer" }),
      h("button", { type: "button", class: "btn", onclick: () => dlg.close() }, "Cancel"),
      go)));
  dlg.showModal();
  confirmInput.focus();
}
