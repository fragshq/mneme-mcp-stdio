// Schema-driven record form: builds inputs from a JSON Schema (draft
// 2020-12 subset), reads them back as a JSON value, and places server
// validation issues (JSON pointers) next to the fields they concern.
// Anything it cannot render as plain inputs (oneOf, anyOf, tuples, free-form
// objects) falls back to a JSON text area for that part of the value.

import { h } from "./dom.js";

/** A user input that cannot be turned into a value (bad number, bad JSON). */
export class FormError extends Error {
  constructor(path, message) {
    super(message);
    this.path = path;
  }
}

const esc = (key) => String(key).replaceAll("~", "~0").replaceAll("/", "~1");

/**
 * buildForm renders a form for schema, prefilled with value (may be
 * undefined). It returns {el, getValue, showErrors, clearErrors}.
 */
export function buildForm(schema, value) {
  const fields = new Map(); // JSON pointer -> {wrap, err}
  const ctx = { root: schema, fields };
  const root = build(ctx, schema, "", value, { required: true, root: true });

  function clearErrors() {
    for (const f of fields.values()) {
      f.wrap.classList.remove("invalid");
      f.err.textContent = "";
      f.err.hidden = true;
    }
  }

  // showErrors marks the fields named by issues and returns the issues it
  // could not attach to a field (shown by the caller in a summary).
  function showErrors(issues) {
    clearErrors();
    const loose = [];
    for (const issue of issues) {
      for (const { path, message } of expand(issue)) {
        const f = nearest(path);
        if (!f) {
          loose.push({ path, message });
          continue;
        }
        f.wrap.classList.add("invalid");
        f.err.hidden = false;
        f.err.textContent = f.err.textContent ? `${f.err.textContent}; ${message}` : message;
      }
    }
    const first = document.querySelector(".field.invalid input, .field.invalid select, .field.invalid textarea");
    if (first) first.focus();
    return loose;
  }

  function nearest(path) {
    for (let p = path; ; p = p.slice(0, p.lastIndexOf("/"))) {
      if (fields.has(p)) return fields.get(p);
      if (p === "") return null;
    }
  }

  return { el: root.el, getValue: () => root.get(), showErrors, clearErrors };
}

// expand turns "missing property 'x'" issues, reported on the parent
// object, into issues on the missing children.
function expand(issue) {
  const m = /missing propert(?:y|ies) (.*)/.exec(issue.message);
  if (!m) return [issue];
  const names = [...m[1].matchAll(/'([^']*)'/g)].map((x) => x[1]);
  if (!names.length) return [issue];
  return names.map((n) => ({ path: `${issue.path}/${esc(n)}`, message: "required" }));
}

// resolve follows local $refs and folds allOf into one schema.
function resolve(ctx, node, depth = 0) {
  if (node === true || node === undefined || node === null) return {};
  if (node === false || depth > 32) return node === false ? { not: {} } : {};
  if (typeof node.$ref === "string" && node.$ref.startsWith("#")) {
    let target = ctx.root;
    for (const part of node.$ref.slice(1).split("/").filter(Boolean)) {
      target = target?.[decodeURIComponent(part).replaceAll("~1", "/").replaceAll("~0", "~")];
    }
    const { $ref, ...rest } = node;
    return resolve(ctx, { ...(target || {}), ...rest }, depth + 1);
  }
  if (Array.isArray(node.allOf)) {
    const { allOf, ...rest } = node;
    let out = { ...rest };
    for (const part of allOf) {
      const r = resolve(ctx, part, depth + 1);
      out = {
        ...r,
        ...out,
        properties: { ...(r.properties || {}), ...(out.properties || {}) },
        required: [...(r.required || []), ...(out.required || [])],
      };
    }
    return out;
  }
  return node;
}

// kindOf picks the widget for a resolved schema.
function kindOf(node) {
  if (node.const !== undefined) return "const";
  if (node.oneOf || node.anyOf || node.not) return "json";
  if (Array.isArray(node.enum)) return "enum";
  let type = node.type;
  if (Array.isArray(type)) {
    const real = type.filter((t) => t !== "null");
    if (real.length !== 1) return "json";
    type = real[0];
  }
  if (!type) {
    if (node.properties) return "object";
    if (node.items) return "array";
    return "json";
  }
  if (type === "object") return node.properties ? "object" : "json";
  if (type === "array") {
    return node.items && !Array.isArray(node.items) && node.items !== true ? "array" : "json";
  }
  return ["string", "number", "integer", "boolean", "null"].includes(type) ? type : "json";
}

function typeText(node, kind) {
  if (kind === "array") {
    const it = node.items && typeof node.items === "object" ? node.items : {};
    return `array of ${it.type || (it.$ref ? it.$ref.split("/").pop() : "any")}`;
  }
  if (kind === "enum") return "choice";
  if (kind === "json") return "JSON";
  const t = Array.isArray(node.type) ? node.type.join(" | ") : node.type || kind;
  return node.format ? `${t} · ${node.format}` : t;
}

function hints(node) {
  const out = [];
  if (node.minimum !== undefined) out.push(`min ${node.minimum}`);
  if (node.maximum !== undefined) out.push(`max ${node.maximum}`);
  if (node.exclusiveMinimum !== undefined) out.push(`> ${node.exclusiveMinimum}`);
  if (node.exclusiveMaximum !== undefined) out.push(`< ${node.exclusiveMaximum}`);
  if (node.minLength !== undefined) out.push(`at least ${node.minLength} chars`);
  if (node.maxLength !== undefined) out.push(`at most ${node.maxLength} chars`);
  if (node.pattern) out.push(`pattern ${node.pattern}`);
  if (node.minItems !== undefined) out.push(`at least ${node.minItems} items`);
  if (node.maxItems !== undefined) out.push(`at most ${node.maxItems} items`);
  return out.join(", ");
}

// field wraps a control with its label, help and error line, and registers
// it under its JSON pointer.
function field(ctx, path, opts, node, kind, control) {
  const err = h("div", { class: "err", hidden: true });
  const help = [node.description, hints(node)].filter(Boolean).join(" — ");
  const label = opts.label === undefined ? opts.name : opts.label;
  const head = label === undefined || label === null ? null
    : h(kind === "object" || kind === "array" ? "div" : "label", { class: kind === "object" || kind === "array" ? "label" : "" },
      node.title || label,
      opts.required ? h("span", { class: "req", title: "required" }, "*") : null,
      h("span", { class: "type" }, typeText(node, kind)));
  const wrap = h("div", { class: "field", dataset: { path } }, head, control, help ? h("div", { class: "help" }, help) : null, err);
  if (head && head.tagName === "LABEL" && control.id) head.htmlFor = control.id;
  ctx.fields.set(path, { wrap, err });
  return wrap;
}

let seq = 0;
const nextID = () => `f${++seq}`;

function build(ctx, rawNode, path, value, opts) {
  const node = resolve(ctx, rawNode);
  const kind = kindOf(node);
  switch (kind) {
    case "object": return objectField(ctx, node, path, value, opts);
    case "array": return arrayField(ctx, node, path, value, opts);
    case "string": return stringField(ctx, node, path, value, opts);
    case "number":
    case "integer": return numberField(ctx, node, path, value, opts, kind);
    case "boolean": return booleanField(ctx, node, path, value, opts);
    case "enum": return enumField(ctx, node, path, value, opts);
    case "const": {
      const el = field(ctx, path, opts, node, kind, h("code", {}, JSON.stringify(node.const)));
      return { el, get: () => node.const };
    }
    case "null": {
      const el = field(ctx, path, opts, node, kind, h("code", {}, "null"));
      return { el, get: () => (opts.required || value !== undefined ? null : undefined) };
    }
    default: return jsonField(ctx, node, path, value, opts);
  }
}

function objectField(ctx, node, path, value, opts) {
  const obj = value && typeof value === "object" && !Array.isArray(value) ? value : undefined;
  const props = node.properties || {};
  const required = new Set(node.required || []);
  // Required fields first; the rest keep the schema's order.
  const order = Object.keys(props).sort((a, b) => Number(required.has(b)) - Number(required.has(a)));
  const children = order.map((key) => [key, props[key]]).map(([key, sub]) => [key, build(ctx, sub, `${path}/${esc(key)}`, obj?.[key], { name: key, required: required.has(key) })]);
  const extras = obj ? Object.keys(obj).filter((k) => !(k in props)) : [];
  const keepExtras = node.additionalProperties !== false;
  const body = h("div", {},
    extras.length ? h("p", { class: "extras" }, keepExtras
      ? `Kept as they are (not in the form): ${extras.join(", ")}. Use JSON mode to edit them.`
      : `Not allowed by the latest schema, so they will be removed: ${extras.join(", ")}.`) : null,
    ...children.map(([, c]) => c.el));
  const el = opts.root ? body : field(ctx, path, opts, node, "object", h("fieldset", { class: "object" }, body));
  if (opts.root) ctx.fields.set(path, { wrap: body, err: h("div") });
  return {
    el,
    get() {
      const out = {};
      if (keepExtras) for (const k of extras) out[k] = obj[k];
      for (const [key, c] of children) {
        const v = c.get();
        if (v !== undefined) out[key] = v;
      }
      if (!opts.required && obj === undefined && Object.keys(out).length === 0) return undefined;
      return out;
    },
  };
}

function arrayField(ctx, node, path, value, opts) {
  const itemSchema = resolve(ctx, node.items);
  const initial = Array.isArray(value) ? value : undefined;
  // A set of choices renders as checkboxes.
  if (Array.isArray(itemSchema.enum) && node.uniqueItems) {
    const boxes = itemSchema.enum.map((choice) => {
      const box = h("input", { type: "checkbox", checked: initial?.some((v) => JSON.stringify(v) === JSON.stringify(choice)) });
      return [choice, h("label", { class: "inline" }, box, String(choice)), box];
    });
    const el = field(ctx, path, opts, node, "array", h("div", { class: "checks" }, ...boxes.map((b) => b[1])));
    return {
      el,
      get() {
        const out = boxes.filter((b) => b[2].checked).map((b) => b[0]);
        return out.length || opts.required || initial !== undefined ? out : undefined;
      },
    };
  }
  let items = []; // [{get}] of the rendered rows
  const list = h("div", { class: "array-items" });
  const render = (values) => {
    list.replaceChildren();
    items = values.map((v, i) => {
      const row = build(ctx, node.items, `${path}/${i}`, v, { label: null, required: true });
      const remove = h("button", { type: "button", class: "btn small danger", title: "Remove", onclick: () => render(collect().filter((_, j) => j !== i)) }, "✕");
      list.append(h("div", { class: "array-item" }, h("div", { class: "content" }, row.el), remove));
      return row;
    });
  };
  // collect reads the rows, keeping unreadable ones as they were typed.
  const collect = () => items.map((it) => { try { return it.get(); } catch { return undefined; } });
  render(initial || []);
  const add = h("button", { type: "button", class: "btn small", onclick: () => render([...collect(), undefined]) }, "+ Add item");
  const el = field(ctx, path, opts, node, "array", h("div", {}, list, h("div", { style: "margin-top:6px" }, add)));
  return {
    el,
    get() {
      const out = items.map((it) => it.get()).filter((v) => v !== undefined);
      return out.length || opts.required || initial !== undefined ? out : undefined;
    },
  };
}

function stringField(ctx, node, path, value, opts) {
  const id = nextID();
  const v = value === undefined || value === null ? "" : typeof value === "string" ? value : JSON.stringify(value);
  let input;
  if (node.maxLength > 200 || node.format === "textarea") {
    input = h("textarea", { id, rows: 4 }, v);
  } else {
    const type = { date: "date", email: "email", uri: "url" }[node.format] || "text";
    input = h("input", { id, type, value: v, placeholder: node.format === "date-time" ? "2026-01-31T09:30:00Z" : node.examples?.[0] ?? "" });
  }
  let control = input;
  if (node.format === "date-time") {
    control = h("div", { class: "inline" }, input,
      h("button", { type: "button", class: "btn small", onclick: () => { input.value = new Date().toISOString().replace(/\.\d+Z$/, "Z"); } }, "now"));
    control.id = id;
  }
  const el = field(ctx, path, opts, node, "string", control);
  return { el, get: () => (input.value === "" ? undefined : input.value) };
}

function numberField(ctx, node, path, value, opts, kind) {
  const input = h("input", {
    id: nextID(), type: "number", step: kind === "integer" ? "1" : "any",
    value: typeof value === "number" ? String(value) : "",
    min: node.minimum ?? "", max: node.maximum ?? "",
  });
  const el = field(ctx, path, opts, node, kind, input);
  return {
    el,
    get() {
      if (input.value.trim() === "") return undefined;
      const n = Number(input.value);
      if (!Number.isFinite(n)) throw new FormError(path, "not a number");
      if (kind === "integer" && !Number.isInteger(n)) throw new FormError(path, "must be a whole number");
      return n;
    },
  };
}

function booleanField(ctx, node, path, value, opts) {
  if (opts.required) {
    const box = h("input", { id: nextID(), type: "checkbox", checked: value === true });
    const el = field(ctx, path, opts, node, "boolean", h("div", { class: "inline" }, box, h("span", { class: "muted" }, "yes")));
    return { el, get: () => box.checked };
  }
  const select = h("select", { id: nextID() },
    h("option", { value: "" }, "—"),
    h("option", { value: "true", selected: value === true }, "true"),
    h("option", { value: "false", selected: value === false }, "false"));
  const el = field(ctx, path, opts, node, "boolean", select);
  return { el, get: () => (select.value === "" ? undefined : select.value === "true") };
}

function enumField(ctx, node, path, value, opts) {
  const current = node.enum.findIndex((c) => JSON.stringify(c) === JSON.stringify(value));
  const select = h("select", { id: nextID() },
    opts.required && current >= 0 ? null : h("option", { value: "" }, "—"),
    ...node.enum.map((c, i) => h("option", { value: String(i), selected: i === current }, typeof c === "string" ? c : JSON.stringify(c))));
  const el = field(ctx, path, opts, node, "enum", select);
  return { el, get: () => (select.value === "" ? undefined : node.enum[Number(select.value)]) };
}

function jsonField(ctx, node, path, value, opts) {
  const area = h("textarea", { id: nextID(), class: "json-input", rows: 4, spellcheck: false, placeholder: "JSON value" },
    value === undefined ? "" : JSON.stringify(value, null, 2));
  const el = field(ctx, path, opts, node, "json", area);
  return {
    el,
    get() {
      if (area.value.trim() === "") return undefined;
      try {
        return JSON.parse(area.value);
      } catch (e) {
        throw new FormError(path, `invalid JSON: ${e.message}`);
      }
    },
  };
}
