export type PrototypeTweakKind = 'color' | 'enum' | 'range' | 'boolean' | 'text'

export type PrototypeTweakValue = string | number | boolean

export type PrototypeTweakValues = Record<string, PrototypeTweakValue>

export interface PrototypeTweakOption {
  value: string
  label: string
}

export interface PrototypeTweakSpec {
  key: string
  label: string
  kind: PrototypeTweakKind
  default: PrototypeTweakValue
  section?: string
  min?: number
  max?: number
  step?: number
  unit?: string
  options?: PrototypeTweakOption[]
  swatches?: string[]
}

export interface PrototypeNodeData {
  [key: string]: unknown
  title?: string
  note?: string
  html?: string
  src?: string
  viewportWidth?: number
  viewportHeight?: number
  preview?: string
  tweaks?: PrototypeTweakSpec[]
  tweakValues?: PrototypeTweakValues
}

export const PROTOTYPE_HEADER_HEIGHT = 36
export const PROTOTYPE_DEFAULT_VIEWPORT_WIDTH = 1280
export const PROTOTYPE_DEFAULT_VIEWPORT_HEIGHT = 800

const TWEAK_KEY_RE = /^[a-zA-Z0-9_-]{1,64}$/
const HEX_COLOR_RE = /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/
const TWEAKS_SCRIPT_RE = /<script\b([^>]*)>([\s\S]*?)<\/script>/gi
const TWEAKS_ATTR_RE = /\bdata-ariadne-tweaks\b/i
const JSON_TYPE_ATTR_RE = /\btype\s*=\s*["']?application\/json["']?/i

const SANDBOX_CSP =
  "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none';"

export function normalizePrototypeTweaks(raw: unknown): PrototypeTweakSpec[] {
  if (!Array.isArray(raw)) return []
  const out: PrototypeTweakSpec[] = []
  const seen = new Set<string>()
  for (const item of raw) {
    if (!item || typeof item !== 'object') continue
    const rec = item as Record<string, unknown>
    const key = typeof rec.key === 'string' ? rec.key.trim() : ''
    if (!key || !TWEAK_KEY_RE.test(key) || seen.has(key)) continue
    seen.add(key)

    const label = typeof rec.label === 'string' && rec.label.trim() ? rec.label.trim() : key
    const rawKind = typeof rec.kind === 'string' ? rec.kind.trim().toLowerCase() : ''
    let kind: PrototypeTweakKind
    if (
      rawKind === 'color' ||
      rawKind === 'enum' ||
      rawKind === 'range' ||
      rawKind === 'boolean' ||
      rawKind === 'text'
    ) {
      kind = rawKind
    } else if (Array.isArray(rec.options)) {
      kind = 'enum'
    } else if (typeof rec.min === 'number' || typeof rec.max === 'number') {
      kind = 'range'
    } else if (typeof rec.default === 'boolean') {
      kind = 'boolean'
    } else if (typeof rec.default === 'number') {
      kind = 'range'
    } else if (typeof rec.default === 'string' && HEX_COLOR_RE.test(rec.default.trim())) {
      kind = 'color'
    } else {
      kind = 'text'
    }

    const section =
      typeof rec.section === 'string' && rec.section.trim() ? rec.section.trim() : undefined

    if (kind === 'boolean') {
      out.push({
        key,
        label,
        kind,
        default: typeof rec.default === 'boolean' ? rec.default : false,
        ...(section ? { section } : {}),
      })
    } else if (kind === 'color') {
      const def =
        typeof rec.default === 'string' && rec.default.trim() ? rec.default.trim() : '#2563eb'
      const swatches = Array.isArray(rec.swatches)
        ? rec.swatches
            .filter((s): s is string => typeof s === 'string' && s.trim().length > 0)
            .map((s) => s.trim())
            .slice(0, 16)
        : undefined
      out.push({
        key,
        label,
        kind,
        default: def,
        ...(section ? { section } : {}),
        ...(swatches?.length ? { swatches } : {}),
      })
    } else if (kind === 'range') {
      const min = typeof rec.min === 'number' && Number.isFinite(rec.min) ? rec.min : 0
      const max = typeof rec.max === 'number' && Number.isFinite(rec.max) ? rec.max : 100
      if (max <= min) continue
      const step =
        typeof rec.step === 'number' && Number.isFinite(rec.step) && rec.step > 0 ? rec.step : 1
      const rawDef =
        typeof rec.default === 'number' && Number.isFinite(rec.default) ? rec.default : min
      const def = Math.min(Math.max(rawDef, min), max)
      const unit = typeof rec.unit === 'string' && rec.unit.trim() ? rec.unit.trim() : undefined
      out.push({
        key,
        label,
        kind,
        min,
        max,
        step,
        default: def,
        ...(unit ? { unit } : {}),
        ...(section ? { section } : {}),
      })
    } else if (kind === 'enum') {
      if (!Array.isArray(rec.options)) continue
      const options: PrototypeTweakOption[] = []
      for (const rawOpt of rec.options) {
        if (typeof rawOpt === 'string' && rawOpt.trim()) {
          const val = rawOpt.trim()
          options.push({ value: val, label: val })
        } else if (rawOpt && typeof rawOpt === 'object') {
          const optRec = rawOpt as Record<string, unknown>
          const val = typeof optRec.value === 'string' ? optRec.value.trim() : ''
          if (!val) continue
          const lbl =
            typeof optRec.label === 'string' && optRec.label.trim() ? optRec.label.trim() : val
          options.push({ value: val, label: lbl })
        }
        if (options.length >= 20) break
      }
      if (!options.length) continue
      const rawDef = typeof rec.default === 'string' ? rec.default.trim() : ''
      const def = options.some((o) => o.value === rawDef) ? rawDef : options[0]!.value
      out.push({
        key,
        label,
        kind,
        options,
        default: def,
        ...(section ? { section } : {}),
      })
    } else if (kind === 'text') {
      const def = typeof rec.default === 'string' ? rec.default.trim().slice(0, 200) : ''
      out.push({
        key,
        label,
        kind,
        default: def,
        ...(section ? { section } : {}),
      })
    }
    if (out.length >= 32) break
  }
  return out
}

export function extractTweaksFromHtml(html: string): PrototypeTweakSpec[] {
  if (!html) return []
  TWEAKS_SCRIPT_RE.lastIndex = 0
  let match: RegExpExecArray | null
  while ((match = TWEAKS_SCRIPT_RE.exec(html)) !== null) {
    const attrs = match[1] ?? ''
    const body = (match[2] ?? '').trim()
    if (!TWEAKS_ATTR_RE.test(attrs) || !JSON_TYPE_ATTR_RE.test(attrs) || !body) continue
    try {
      const parsed = JSON.parse(body) as unknown
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        const maybe = (parsed as Record<string, unknown>).tweaks
        return normalizePrototypeTweaks(maybe)
      }
      return normalizePrototypeTweaks(parsed)
    } catch {
      continue
    }
  }
  return []
}

export function effectiveTweakValues(
  specs: readonly PrototypeTweakSpec[],
  saved?: PrototypeTweakValues,
): PrototypeTweakValues {
  const result: PrototypeTweakValues = {}
  for (const spec of specs) {
    const candidate = saved?.[spec.key]
    if (spec.kind === 'boolean' && typeof candidate === 'boolean') {
      result[spec.key] = candidate
    } else if (
      spec.kind === 'range' &&
      typeof candidate === 'number' &&
      Number.isFinite(candidate)
    ) {
      const min = spec.min ?? 0
      const max = spec.max ?? 100
      result[spec.key] = Math.min(Math.max(candidate, min), max)
    } else if (spec.kind === 'enum' && typeof candidate === 'string') {
      result[spec.key] = spec.options?.some((o) => o.value === candidate) ? candidate : spec.default
    } else if (spec.kind === 'color' && typeof candidate === 'string' && candidate.trim()) {
      result[spec.key] = candidate.trim()
    } else {
      result[spec.key] = spec.default
    }
  }
  return result
}

export function countModifiedTweaks(
  specs: readonly PrototypeTweakSpec[],
  saved?: PrototypeTweakValues,
): number {
  const effective = effectiveTweakValues(specs, saved)
  let count = 0
  for (const spec of specs) {
    if (effective[spec.key] !== spec.default) count++
  }
  return count
}

export interface PrototypeTweakGroup {
  section: string | null
  items: PrototypeTweakSpec[]
}

export function groupTweaksBySection(specs: readonly PrototypeTweakSpec[]): PrototypeTweakGroup[] {
  const groups: PrototypeTweakGroup[] = []
  const byName = new Map<string, PrototypeTweakGroup>()
  for (const spec of specs) {
    const key = spec.section ?? ''
    let group = byName.get(key)
    if (!group) {
      group = { section: spec.section ?? null, items: [] }
      byName.set(key, group)
      groups.push(group)
    }
    group.items.push(spec)
  }
  return groups
}

function escapeInlineJson(value: unknown): string {
  return JSON.stringify(value)
    .replace(/</g, '\\u003c')
    .replace(/>/g, '\\u003e')
    .replace(/&/g, '\\u0026')
    .replace(/\u2028/g, '\\u2028')
    .replace(/\u2029/g, '\\u2029')
}

function buildBridgeScript(
  frameId: string,
  specs: readonly PrototypeTweakSpec[],
  initialValues: PrototypeTweakValues,
): string {
  const units: Record<string, string> = {}
  for (const spec of specs) {
    if (spec.unit) units[spec.key] = spec.unit
  }
  const config = escapeInlineJson({ frameId, units, initialValues })
  return `<script data-ariadne-bridge="1">
(function(){
  var cfg = ${config};
  var current = Object.assign({}, cfg.initialValues || {});
  function cssValue(key, val) {
    if (typeof val === 'number' && cfg.units && cfg.units[key]) return String(val) + cfg.units[key];
    return String(val);
  }
  function applyTweaks(values, emitEvent) {
    var root = document.documentElement;
    if (!root || !values || typeof values !== 'object') return;
    var changed = {};
    var keys = Object.keys(values);
    for (var i = 0; i < keys.length; i++) {
      var k = keys[i];
      var v = values[k];
      if (v === undefined || v === null) continue;
      if (current[k] !== v) changed[k] = v;
      current[k] = v;
      root.style.setProperty('--tweak-' + k, cssValue(k, v));
      root.style.setProperty('--tweak-' + k + '-raw', String(v));
      root.setAttribute('data-tweak-' + k, String(v));
    }
    if (emitEvent) {
      var detail = { values: Object.assign({}, current), changed: changed };
      try {
        window.dispatchEvent(new CustomEvent('ariadne:tweaks', { detail: detail }));
        document.dispatchEvent(new CustomEvent('ariadne:tweaks', { detail: detail }));
      } catch (_) {}
    }
  }
  applyTweaks(current, false);
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function(){ applyTweaks(current, true); }, { once: true });
  } else {
    setTimeout(function(){ applyTweaks(current, true); }, 0);
  }
  var SEMANTIC_SELECTOR = 'button,a[href],input,textarea,select,label,[role="button"],[role="link"],[role="tab"],[role="switch"],[role="checkbox"],[role="radio"],[role="menuitem"],[data-testid],h1,h2,h3,h4,nav,header,footer,aside,article,section';
  function pickTarget(raw) {
    if (!raw || raw === document.documentElement || raw === document.body) return raw;
    if (raw.closest) {
      var semantic = raw.closest(SEMANTIC_SELECTOR);
      if (semantic && semantic !== document.body && semantic !== document.documentElement) return semantic;
    }
    return raw;
  }
  function inferRole(el) {
    var explicit = el.getAttribute && el.getAttribute('role');
    if (explicit) return explicit.trim().toLowerCase().slice(0, 40);
    var tag = (el.tagName || '').toLowerCase();
    if (tag === 'button') return 'button';
    if (tag === 'a') return 'link';
    if (tag === 'input' || tag === 'textarea' || tag === 'select') return 'input';
    if (/^h[1-6]$/.test(tag)) return 'heading';
    if (tag === 'nav') return 'navigation';
    if (tag === 'header') return 'banner';
    if (tag === 'footer') return 'contentinfo';
    if (tag === 'img' || tag === 'svg') return 'image';
    return '';
  }
  function inferLabel(el) {
    var attrs = ['aria-label', 'data-label', 'title', 'placeholder', 'alt', 'name'];
    for (var i = 0; i < attrs.length; i++) {
      var v = el.getAttribute && el.getAttribute(attrs[i]);
      if (v && v.trim()) return v.trim().replace(/\\s+/g, ' ').slice(0, 80);
    }
    var txt = (el.innerText || el.textContent || '').trim().replace(/\\s+/g, ' ');
    if (txt) return txt.slice(0, 80);
    return '';
  }
  function buildSelector(el) {
    var parts = [];
    var cur = el;
    var depth = 0;
    while (cur && cur.nodeType === 1 && cur !== document.documentElement && depth < 4) {
      var seg = (cur.tagName || '').toLowerCase();
      if (cur.id && /^[a-zA-Z0-9_-]+$/.test(cur.id)) {
        seg += '#' + cur.id;
        parts.unshift(seg);
        break;
      }
      var testId = cur.getAttribute && cur.getAttribute('data-testid');
      if (testId) {
        seg += '[data-testid="' + testId.slice(0, 40) + '"]';
        parts.unshift(seg);
        break;
      }
      if (typeof cur.className === 'string' && cur.className.trim()) {
        var classes = cur.className.trim().split(/\\s+/).filter(function(c){ return /^[a-zA-Z0-9_-]{1,32}$/.test(c); }).slice(0, 2);
        if (classes.length) seg += '.' + classes.join('.');
      }
      parts.unshift(seg);
      cur = cur.parentElement;
      depth++;
    }
    return parts.join(' > ').slice(0, 200);
  }
  function inspectPoint(x, y) {
    var raw = document.elementFromPoint(x, y);
    if (!raw || raw === document.documentElement || raw === document.body) return null;
    var el = pickTarget(raw);
    if (!el || el === document.documentElement || el === document.body) return null;
    var r = el.getBoundingClientRect();
    var vw = Math.max(window.innerWidth || 1, 1);
    var vh = Math.max(window.innerHeight || 1, 1);
    var out = { tag: (el.tagName || '').toLowerCase().slice(0, 40) };
    var role = inferRole(el);
    if (role) out.role = role;
    var label = inferLabel(el);
    if (label) out.label = label;
    var sel = buildSelector(el);
    if (sel) out.selector = sel;
    if (r && r.width >= 0 && r.height >= 0) {
      out.rect = {
        x: Math.max(0, Math.min(1, r.left / vw)),
        y: Math.max(0, Math.min(1, r.top / vh)),
        w: Math.max(0, Math.min(1, r.width / vw)),
        h: Math.max(0, Math.min(1, r.height / vh))
      };
    }
    return out;
  }
  window.addEventListener('message', function(ev) {
    var data = ev && ev.data;
    if (!data || typeof data !== 'object' || data.frameId !== cfg.frameId) return;
    if (data.type === 'ariadne:tweaks') {
      applyTweaks(data.values, true);
    } else if (data.type === 'ariadne:inspect-point') {
      var element = null;
      try { element = inspectPoint(Number(data.x) || 0, Number(data.y) || 0); } catch (_) {}
      try {
        window.parent.postMessage({
          type: 'ariadne:inspect-result',
          frameId: cfg.frameId,
          requestId: data.requestId,
          element: element
        }, '*');
      } catch (_) {}
    }
  });
  window.addEventListener('keydown', function(ev) {
    if (ev.key === 'Escape') {
      try {
        window.parent.postMessage({ type: 'ariadne:escape', frameId: cfg.frameId }, '*');
      } catch (_) {}
    }
  });
})();
</script>`
}

/**
 * Wraps raw prototype HTML with a strict sandbox CSP and the lightweight Ariadne bridge script
 * so host tweaks and semantic comment inspection work inside an opaque-origin sandbox iframe.
 */
export function buildSandboxedPrototypeDoc(
  rawHtml: string,
  frameId: string,
  specs: readonly PrototypeTweakSpec[],
  initialValues: PrototypeTweakValues,
): string {
  const cspTag = `<meta http-equiv="Content-Security-Policy" content="${SANDBOX_CSP}">`
  const bridge = buildBridgeScript(frameId, specs, initialValues)
  const injection = `${cspTag}\n${bridge}\n`

  const headOpen = /<head\b[^>]*>/i.exec(rawHtml)
  if (headOpen) {
    const insertAt = headOpen.index + headOpen[0].length
    return `${rawHtml.slice(0, insertAt)}\n${injection}${rawHtml.slice(insertAt)}`
  }
  const htmlOpen = /<html\b[^>]*>/i.exec(rawHtml)
  if (htmlOpen) {
    const insertAt = htmlOpen.index + htmlOpen[0].length
    return `${rawHtml.slice(0, insertAt)}\n<head>${injection}</head>${rawHtml.slice(insertAt)}`
  }
  return `<!doctype html><html><head><meta charset="utf-8">${injection}</head><body>${rawHtml}</body></html>`
}
