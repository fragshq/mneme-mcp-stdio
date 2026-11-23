// Tiny DOM helpers. Text always goes through text nodes, never innerHTML,
// so record data can't inject markup.

const PROPS = new Set(["value", "checked", "selected", "hidden", "disabled", "id", "htmlFor", "type", "rows", "spellcheck"]);

/** h(tag, props, ...children) creates an element. */
export function h(tag, props = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (k === "value" ? v === undefined || v === null : v === undefined || v === null || v === false || v === "") continue;
    if (k === "class") el.className = v;
    else if (k === "dataset") Object.assign(el.dataset, v);
    else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
    else if (PROPS.has(k)) el[k] = v;
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  append(el, children);
  return el;
}

/** put replaces el's children, skipping null/undefined/false like h(). */
export function put(el, ...children) {
  el.replaceChildren();
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (Array.isArray(c)) append(el, c);
    else el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

/** json renders a value as highlighted, pretty-printed JSON. */
export function json(value) {
  const pre = h("pre", { class: "json" });
  const text = JSON.stringify(value, null, 2) ?? "undefined";
  const re = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false)\b|\b(null)\b|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g;
  let last = 0;
  for (const m of text.matchAll(re)) {
    pre.append(text.slice(last, m.index));
    const cls = m[1] ? (m[2] ? "k" : "s") : m[3] ? "b" : m[4] ? "z" : "n";
    pre.append(h("span", { class: cls }, m[1] || m[0]));
    if (m[2]) pre.append(m[2]);
    last = m.index + m[0].length;
  }
  pre.append(text.slice(last));
  return pre;
}

/** when formats an RFC 3339 timestamp in the viewer's locale. */
export function when(ts) {
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? String(ts) : d.toLocaleString();
}
