// Lint for the shipped stylesheet (@fluxdocs/core/styles.css). It guards the
// rules in docs/design/viewer-chrome.md §2 and §9.3: fd- selectors only, every
// token used is declared, no network-capable constructs (privacy), the
// reduced-motion guard exists, WCAG AA contrast holds in light and dark, and
// the :root tokens match spec §8.2. Run: `node --test test/styles.test.mjs`.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const pkg = join(here, "..");
const repo = join(pkg, "..", "..");

const raw = readFileSync(join(pkg, "styles.css"), "utf8");
const css = stripComments(raw);
const rules = parseRules(css);
const rootTokens = tokensOf(rules.filter((r) => r.selector === ":root" && r.at.length === 0));

// ---- tiny CSS reader (enough for a flat, hand-written sheet) --------------

function stripComments(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, "");
}

/**
 * parseRules returns every style rule as { selector, decls: [[prop, value]],
 * at: [enclosing at-rule preludes] }. Nested at-rule blocks are followed;
 * style rules are assumed not to nest (true for this file).
 */
function parseRules(text) {
  const out = [];
  const stack = [];
  let buf = "";
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === "{") {
      const prelude = buf.trim();
      buf = "";
      if (prelude.startsWith("@")) {
        stack.push(prelude);
        continue;
      }
      const end = text.indexOf("}", i + 1);
      assert.ok(end > i, `unterminated rule: ${prelude}`);
      out.push({ selector: prelude, decls: parseDecls(text.slice(i + 1, end)), at: [...stack] });
      i = end;
    } else if (c === "}") {
      stack.pop();
      buf = "";
    } else if (c === ";" && buf.trim().startsWith("@")) {
      out.push({ selector: buf.trim(), decls: [], at: [...stack], statement: true });
      buf = "";
    } else {
      buf += c;
    }
  }
  return out;
}

function parseDecls(body) {
  return splitTopLevel(body, ";")
    .map((d) => d.trim())
    .filter(Boolean)
    .map((d) => {
      const k = d.indexOf(":");
      return [d.slice(0, k).trim(), d.slice(k + 1).trim().replace(/\s+/g, " ")];
    });
}

/** splitTopLevel splits on `sep` outside (), [] and quotes. */
function splitTopLevel(s, sep) {
  const parts = [];
  let depth = 0;
  let quote = "";
  let cur = "";
  for (const c of s) {
    if (quote) {
      if (c === quote) quote = "";
    } else if (c === '"' || c === "'") {
      quote = c;
    } else if (c === "(" || c === "[") {
      depth++;
    } else if (c === ")" || c === "]") {
      depth--;
    } else if (c === sep && depth === 0) {
      parts.push(cur);
      cur = "";
      continue;
    }
    cur += c;
  }
  parts.push(cur);
  return parts;
}

function tokensOf(ruleList) {
  const map = new Map();
  for (const r of ruleList) {
    for (const [k, v] of r.decls) {
      if (k.startsWith("--")) map.set(k, v);
    }
  }
  return map;
}

// ---- color math (WCAG 2.x relative luminance) -----------------------------

function resolve(tokens, name, seen = new Set()) {
  assert.ok(!seen.has(name), `token cycle at ${name}`);
  seen.add(name);
  const v = tokens.get(name);
  assert.ok(v !== undefined, `token ${name} is not declared`);
  const m = /^var\(\s*(--[\w-]+)\s*\)$/.exec(v);
  return m ? resolve(tokens, m[1], seen) : v;
}

function hexToRgb(hex) {
  let h = hex.trim().replace(/^#/, "");
  if (h.length === 3) h = [...h].map((c) => c + c).join("");
  assert.match(h, /^[0-9a-fA-F]{6}$/, `expected a hex color, got ${hex}`);
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
}

function luminance(hex) {
  const [r, g, b] = hexToRgb(hex).map((v) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** themeTokens returns the token map as seen by `.fd-viewer` in a theme. */
function themeTokens(theme) {
  const t = new Map(rootTokens);
  if (theme === "dark") {
    for (const name of rootTokens.keys()) {
      if (rootTokens.has(`${name}-dark`)) t.set(name, `var(${name}-dark)`);
    }
  }
  return t;
}

// ---- tests -----------------------------------------------------------------

test("privacy: no network-capable constructs", () => {
  assert.doesNotMatch(css, /@import/i);
  assert.doesNotMatch(css, /@font-face/i);
  assert.doesNotMatch(css, /url\s*\(/i);
  assert.doesNotMatch(css, /image-set\s*\(/i);
});

test("no !important and no @layer", () => {
  assert.doesNotMatch(css, /!\s*important/i);
  assert.doesNotMatch(css, /@layer/i);
});

test("every selector is scoped to an fd- class (or :root)", () => {
  for (const r of rules.filter((x) => !x.statement)) {
    for (const sel of splitTopLevel(r.selector, ",").map((s) => s.trim())) {
      if (sel === ":root") continue;
      // Drop attribute selectors and functional-pseudo arguments before
      // looking at classes/IDs/type selectors.
      const bare = sel.replace(/\[[^\]]*\]/g, "[]").replace(/\([^)]*\)/g, "()");
      assert.match(bare, /\.fd-/, `selector without an fd- class: ${sel}`);
      for (const m of bare.matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)) {
        assert.ok(m[1].startsWith("fd-"), `non-fd class .${m[1]} in ${sel}`);
      }
      assert.doesNotMatch(bare, /#/, `ID selector in ${sel}`);
      for (const compound of bare.split(/\s*[\s>+~]\s*/).filter(Boolean)) {
        assert.doesNotMatch(compound, /^[a-zA-Z]/, `type selector "${compound}" in ${sel}`);
      }
    }
  }
});

test("every var(--*) used is an --fd- token declared in :root", () => {
  for (const r of rules) {
    for (const [, v] of r.decls) {
      for (const m of v.matchAll(/var\(\s*(--[\w-]+)/g)) {
        assert.ok(m[1].startsWith("--fd-"), `non-fd custom property ${m[1]}`);
        assert.ok(rootTokens.has(m[1]), `${m[1]} used but not declared in :root`);
      }
    }
  }
});

test("dark theme re-points every -dark pair, in both activation rules", () => {
  const pairs = [...rootTokens.keys()]
    .filter((k) => k.endsWith("-dark"))
    .map((dark) => [dark.slice(0, -"-dark".length), dark]);
  assert.ok(pairs.length >= 7, "expected the seven light/dark pairs");
  const remapRules = rules.filter((r) =>
    r.decls.some(([k, v]) => k === "color-scheme" && v === "dark"),
  );
  assert.equal(remapRules.length, 2, "media-query rule + data-fd-theme rule");
  assert.ok(remapRules.some((r) => r.at.some((a) => /prefers-color-scheme:\s*dark/.test(a))));
  assert.ok(remapRules.some((r) => r.selector.includes('[data-fd-theme="dark"]')));
  for (const r of remapRules) {
    for (const [base, dark] of pairs) {
      assert.ok(rootTokens.has(base), `${dark} has no base token ${base}`);
      assert.ok(
        r.decls.some(([k, v]) => k === base && v === `var(${dark})`),
        `${r.selector} does not remap ${base} -> ${dark}`,
      );
    }
  }
});

test("reduced-motion guard zeroes transitions", () => {
  const guarded = rules.filter((r) =>
    r.at.some((a) => /prefers-reduced-motion:\s*reduce/.test(a)),
  );
  assert.ok(guarded.length > 0, "missing @media (prefers-reduced-motion: reduce)");
  assert.ok(
    guarded.some((r) =>
      r.selector.includes(".fd-viewer *") &&
      r.decls.some(([k, v]) => k === "transition-duration" && v === "0s"),
    ),
  );
});

test("coarse pointers get 44px controls", () => {
  const coarse = rules.filter(
    (r) => r.selector === ":root" && r.at.some((a) => /pointer:\s*coarse/.test(a)),
  );
  assert.equal(tokensOf(coarse).get("--fd-control-size"), "44px");
  assert.equal(rootTokens.get("--fd-control-size"), "32px");
});

// decl returns the value of `prop` in the unconditional rule for `selector`.
function decl(selector, prop) {
  const r = rules.find((x) => x.selector === selector && x.at.length === 0);
  assert.ok(r, `missing rule ${selector}`);
  const d = r.decls.find(([k]) => k === prop);
  return d ? d[1] : undefined;
}

test("root fills its container's width (inline-size containment)", () => {
  // container-type: inline-size sizes the box as if empty, so in a
  // shrink-to-fit host (flex item, inline-block, float) the viewer would be
  // 0 px wide without an explicit width (design note §6).
  assert.match(decl(".fd-viewer", "container"), /inline-size/);
  assert.equal(decl(".fd-viewer", "width"), "100%");
  // width: 100% cannot resolve in a shrink-to-fit host; the intrinsic size
  // keeps the viewer at 320 px there instead of 0 px.
  assert.equal(decl(".fd-viewer", "contain-intrinsic-inline-size"), "auto 320px");
});

test('root-level data-fd-theme="light" restores the light tokens under a dark ancestor', () => {
  const pairs = [...rootTokens.keys()]
    .filter((k) => k.endsWith("-dark"))
    .map((dark) => dark.slice(0, -"-dark".length));
  const darkIdx = rules.findIndex(
    (r) => r.at.length === 0 && r.selector.includes('[data-fd-theme="dark"] .fd-viewer'),
  );
  const lightIdx = rules.findIndex(
    (r) => r.at.length === 0 && r.selector === '.fd-viewer[data-fd-theme="light"]',
  );
  assert.ok(darkIdx >= 0 && lightIdx >= 0, "missing dark ancestor rule or light root rule");
  // Equal specificity (0,2,0), so source order decides: light must come later.
  assert.ok(lightIdx > darkIdx, "light root rule must follow the dark rule");
  const light = rules[lightIdx];
  assert.ok(light.decls.some(([k, v]) => k === "color-scheme" && v === "light"));
  for (const base of pairs) {
    assert.ok(
      light.decls.some(([k, v]) => k === base && v === "inherit"),
      `light root rule does not restore ${base}`,
    );
  }
});

test("buttons transition only background-color", () => {
  assert.equal(decl(".fd-viewer__btn", "transition"), "background-color var(--fd-transition-fast)");
});

test("page edge does not shrink the bitmap; viewport reserves the scrollbar gutter", () => {
  // A border under border-box would make the img (W-2)x(H-2).
  for (const k of ["border", "border-width", "padding"]) {
    assert.equal(decl(".fd-viewer__page", k), undefined, `.fd-viewer__page sets ${k}`);
  }
  assert.equal(decl(".fd-viewer__page", "outline"), "1px solid var(--fd-page-border)");
  assert.equal(decl(".fd-viewer__viewport", "scrollbar-gutter"), "stable");
});

test("contrast formula matches the design note", () => {
  // docs/design/viewer-chrome.md §7.3: #18181b on #fafafa = 16.97:1.
  assert.equal(contrast("#18181b", "#fafafa").toFixed(2), "16.97");
  assert.equal(contrast("#71717a", "#e5e7eb").toFixed(2), "3.90");
});

for (const theme of ["light", "dark"]) {
  test(`WCAG AA contrast holds in the ${theme} theme`, () => {
    const t = themeTokens(theme);
    const required = [
      ["--fd-fg", "--fd-bg", 4.5],
      ["--fd-fg-muted", "--fd-bg", 4.5],
      ["--fd-fg", "--fd-bg-canvas", 4.5],
      ["--fd-danger", "--fd-bg", 4.5],
      ["--fd-focus-ring", "--fd-bg", 3],
      ["--fd-focus-ring", "--fd-bg-canvas", 3],
    ];
    for (const [fg, bg, min] of required) {
      const ratio = contrast(resolve(t, fg), resolve(t, bg));
      assert.ok(ratio >= min, `${theme}: ${fg} on ${bg} is ${ratio.toFixed(2)}:1 < ${min}:1`);
    }
  });
}

test(":root tokens match docs/spec.md §8.2", () => {
  const spec = readFileSync(join(repo, "docs", "spec.md"), "utf8");
  const section = spec.slice(spec.indexOf("### 8.2 Design Tokens"));
  const block = /```css\n([\s\S]*?)```/.exec(section);
  assert.ok(block, "spec §8.2 css block not found");
  const specTokens = tokensOf(parseRules(stripComments(block[1])));
  assert.deepEqual(
    Object.fromEntries([...rootTokens].sort()),
    Object.fromEntries([...specTokens].sort()),
  );
});
