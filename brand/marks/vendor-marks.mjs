// Igneum vendor and OS marks (gpu-logos, 7 October 2026): the strings the miner app ships in app/igneum-app/ui/app.js
// (View.MARKS and View.VENDORS), exported verbatim for the site and anything else that names the hardware.
// view.test.mjs fails when this file and app.js drift apart, so edit app.js first and regenerate this file with
// `node brand/marks/regen.mjs` (or copy the strings by hand; the test says which one moved).
//
// Each glyph: a hand-drawn simplified monochrome mark of the vendor's public geometry (never a copied logo file,
// never a raster), 24 x 24 viewBox at 22 px, under 460 bytes, fill or stroke through currentColor so the element's
// colour tints it. Nominative use that names the hardware; the ember accent is for state and never tints a brand.
//
// The treatment in the app (app.css, the block at the end): a 44 x 44 well, radius 12, background the vendor colour
// at .14 alpha (dark) or .10 (light), a 1 px ring in the vendor colour at .45 alpha, the glyph in the full colour;
// hover and focus-within add a 3 px halo of the well colour; nothing animates. The light hex of every vendor reads at
// 3:1 or better on its well over white (nvidia 3.87, amd 5.01, intel 4.29, apple 7.52, gpu 4.65).
//
// Class names in the app: .badge.<vendor> (nvidia | amd | intel | apple | gpu), .badge.mini for a 26 px inline mark,
// .gen for the series line under the name. Tokens: --mark-<vendor>, --mark-<vendor>-well, --mark-<vendor>-ring.

export const VENDORS = {
  "nvidia": {
    "label": "NVIDIA",
    "dark": "#8BE37A",
    "light": "#2F8A22"
  },
  "amd": {
    "label": "AMD Radeon",
    "dark": "#FF5A5A",
    "light": "#C41E2A"
  },
  "intel": {
    "label": "Intel",
    "dark": "#7CC4FF",
    "light": "#1C6FD6"
  },
  "apple": {
    "label": "Apple",
    "dark": "#E6E3DD",
    "light": "#4A4A50"
  },
  "gpu": {
    "label": "GPU",
    "dark": "#9A9A9E",
    "light": "#6B6B70"
  }
};
export const WELL_ALPHA = { dark: 0.14, light: 0.1 };
export const MARKS = {
  "nvidia": "<svg viewBox=\"0 0 24 24\" width=\"22\" height=\"22\" aria-hidden=\"true\" focusable=\"false\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.8\" stroke-linecap=\"round\" stroke-linejoin=\"round\"><path d=\"M2.6 12C5 7.9 8.3 5.8 12 5.8s7 2.1 9.4 6.2c-2.4 4.1-5.7 6.2-9.4 6.2S5 16.1 2.6 12z\"/><path d=\"M15.6 12A3.6 3.6 0 1 0 12 15.6\"/><circle cx=\"12\" cy=\"12\" r=\"1.2\" fill=\"currentColor\" stroke=\"none\"/></svg>",
  "amd": "<svg viewBox=\"0 0 24 24\" width=\"22\" height=\"22\" aria-hidden=\"true\" focusable=\"false\" fill=\"currentColor\"><path d=\"M8 3h13v13l-4-4V7h-5z\"/><path d=\"M3 8l4 4v5h5l4 4H3z\"/></svg>",
  "intel": "<svg viewBox=\"0 0 24 24\" width=\"22\" height=\"22\" aria-hidden=\"true\" focusable=\"false\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.8\" stroke-linecap=\"round\"><path d=\"M7.4 7.6C4.9 8.8 3.4 10.5 3.4 12.3c0 3.4 4.3 5.9 9.8 5.9 4.5 0 7.6-1.6 7.6-3.9 0-1.4-1.3-2.6-3.5-3.3\"/><path d=\"M11.2 9.6v6.2\"/><circle cx=\"11.2\" cy=\"6.6\" r=\"1.1\" fill=\"currentColor\" stroke=\"none\"/></svg>",
  "apple": "<svg viewBox=\"0 0 24 24\" width=\"22\" height=\"22\" aria-hidden=\"true\" focusable=\"false\" fill=\"currentColor\"><path d=\"M16.4 12.6c0-2.5 2-3.6 2.1-3.7-1.2-1.7-3-1.9-3.6-2-1.5-.2-3 .9-3.8.9-.8 0-2-.9-3.3-.8-1.7 0-3.2 1-4.1 2.5-1.8 3-.5 7.6 1.3 10.1.9 1.2 1.9 2.6 3.2 2.5 1.3 0 1.8-.8 3.3-.8 1.6 0 2 .8 3.3.8 1.4 0 2.3-1.2 3.1-2.5 1-1.4 1.4-2.8 1.4-2.9 0 0-2.7-1-2.9-4.1zM13.9 5.3c.7-.8 1.2-2 1-3.2-1 0-2.2.7-2.9 1.5-.6.7-1.2 1.9-1 3 1.1.1 2.2-.5 2.9-1.3z\"/></svg>",
  "gpu": "<svg viewBox=\"0 0 24 24\" width=\"22\" height=\"22\" aria-hidden=\"true\" focusable=\"false\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.7\" stroke-linecap=\"round\"><rect x=\"5.5\" y=\"5.5\" width=\"13\" height=\"13\" rx=\"2.2\"/><rect x=\"9.5\" y=\"9.5\" width=\"5\" height=\"5\" rx=\"1\"/><path d=\"M9 2.5v3M12 2.5v3M15 2.5v3M9 18.5v3M12 18.5v3M15 18.5v3M2.5 9h3M2.5 12h3M2.5 15h3M18.5 9h3M18.5 12h3M18.5 15h3\"/></svg>"
};
// OS marks in the same treatment: Apple is the vendor glyph; Windows is the four slanted panes
export const OS_MARKS = {
  macos: MARKS.apple,
  windows: "<svg viewBox=\"0 0 24 24\" width=\"22\" height=\"22\" aria-hidden=\"true\" focusable=\"false\" fill=\"currentColor\"><path d=\"M3 5.6l7.3-1v7.1H3zM11.4 4.4L21 3v8.7h-9.6zM3 12.3h7.3v7.1L3 18.4zM11.4 12.3H21V21l-9.6-1.4z\"/></svg>"
};
export const OS_COLOURS = { macos: VENDORS.apple, windows: { label: 'Windows', dark: '#7CC4FF', light: '#1C6FD6' } };
// the CSS tokens for both themes, as the app declares them
export function tokensCss() {
  const line = (theme) => Object.entries(VENDORS).map(([v, c]) => { const h = c[theme], r = parseInt(h.slice(1, 3), 16), g = parseInt(h.slice(3, 5), 16), b = parseInt(h.slice(5, 7), 16), a = String(WELL_ALPHA[theme]).replace(/^0/, ''); return `--mark-${v}:${h};--mark-${v}-well:rgba(${r},${g},${b},${a});--mark-${v}-ring:rgba(${r},${g},${b},.45)`; }).join(';');
  return `:root{${line('dark')}}\n@media (prefers-color-scheme:light){:root:not([data-theme="dark"]){${line('light')}}}\n:root[data-theme="light"]{${line('light')}}`;
}
// the well: <div class="badge nvidia"><svg…></svg></div>
export function markHtml(vendor, size) { const v = MARKS[vendor] ? vendor : 'gpu'; return '<div class="badge ' + (size ? size + ' ' : '') + v + '" data-mark="' + v + '" title="' + VENDORS[v].label + '">' + MARKS[v] + '</div>'; }
