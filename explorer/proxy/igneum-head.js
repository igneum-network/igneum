// The explorer's page head, written at the proxy (the founder's order through main, 15 October 2026): every public
// page of the explorer carries its own title in a card owner's voice, one plain description, the canonical link, the
// Open Graph and Twitter set with the explorer's own image, and the favicon set and manifest as the site serves them.
// The explorer is a stock Blockscout; its own head (the vendor's description, its stock image, its favicons) is
// replaced here on every HTML answer and nothing else in the answer is touched. The chain is named "the public test
// network" and never by an internal name.
//
// Loaded by nginx's njs module (ngx_http_js_module): js_import in the http context, js_header_filter and
// js_body_filter on the frontend location (explorer/proxy/default.conf.template).

const HOST = 'https://explorer.igneum.network';
const SITE_NAME = 'Igneum';
const IMAGE = 'https://igneum.network/og-rebuild/explorer-1200x630.png';   // 1200 by 630 PNG, rendered by the site from its template
const HOME_TITLE = 'The explorer of the test network';
const HOME_DESC = 'Every block, transaction and contract of the public test network, read from the chain and verified against your source.';

function short(h) { return h.length > 14 ? h.slice(0, 8) + '…' + h.slice(-4) : h; }

// the title (two to six words, never a summary with a colon) and the description (one plain sentence under 155
// characters) for a path; the query string is not part of the page
function page(uri) {
  let m;
  if (uri === '/' || uri === '') return [HOME_TITLE, HOME_DESC];
  if ((m = /^\/block\/(\d+)\/?$/.exec(uri))) return ['Block ' + m[1] + ' of the test network', 'Block ' + m[1] + ' of the public test network, with its sealer, its transactions and the shares it paid, read from the chain.'];
  if ((m = /^\/block\/(0x[0-9a-fA-F]{64})\/?$/.exec(uri))) return ['Block ' + short(m[1]) + ' of the test network', 'One block of the public test network, with its sealer, its transactions and the shares it paid, read from the chain.'];
  if ((m = /^\/tx\/(0x[0-9a-fA-F]{64})/.exec(uri))) return ['Transaction ' + short(m[1]) + ' on the test network', 'One transaction of the public test network, with its block, its receipt, its logs and its fee, read from the chain.'];
  if ((m = /^\/address\/(0x[0-9a-fA-F]{40})/.exec(uri))) return ['Address ' + short(m[1]) + ' on the test network', 'One address of the public test network, with its balance, its transactions and its code, read from the chain.'];
  if ((m = /^\/token\/(0x[0-9a-fA-F]{40})/.exec(uri))) return ['Token ' + short(m[1]) + ' on the test network', 'One token contract of the public test network, with its holders and its transfers, read from the chain.'];
  if ((m = /^\/op\/(0x[0-9a-fA-F]{64})/.exec(uri))) return ['User operation ' + short(m[1]) + ' on the test network', 'One user operation of the public test network, with its bundle, its sender and its outcome, read from the chain.'];
  if (/^\/blocks\/?$/.test(uri)) return ['The blocks of the test network', 'The blocks of the public test network as they seal, each with its sealer and its transactions.'];
  if (/^\/txs\/?$/.test(uri)) return ['The transactions of the test network', 'The transactions of the public test network as they are included, each with its block and its fee.'];
  if (/^\/verified-contracts\/?$/.test(uri)) return ['Verified contracts of the test network', 'Every contract of the public test network whose source has been verified against the bytecode the chain holds.'];
  if (/^\/contract-verification/.test(uri)) return ['Verify a contract of the test network', 'Verify the source of a contract on the public test network against the bytecode the chain holds.'];
  if (/^\/accounts\/?$/.test(uri)) return ['Accounts of the test network', 'The addresses of the public test network by balance, read from the chain.'];
  if (/^\/tokens\/?$/.test(uri)) return ['Tokens of the test network', 'The token contracts of the public test network, read from the chain.'];
  if (/^\/stats\/?$/.test(uri)) return ['The test network in numbers', 'The public test network in numbers, read from the chain as it runs.'];
  if (/^\/api-docs\/?$/.test(uri)) return ['The explorer’s API', 'The explorer’s API for the public test network, the same data its pages read.'];
  if (/^\/graphiql\/?$/.test(uri)) return ['The explorer’s GraphQL', 'The explorer’s GraphQL interface for the public test network, the same data its pages read.'];
  if (/^\/search-results/.test(uri)) return ['Search of the test network', 'Search the public test network by block, transaction, address or token.'];
  if (/^\/withdrawals\/?$/.test(uri)) return ['Withdrawals of the test network', 'The withdrawals page of the public test network, read from the chain.'];
  if (/^\/batches\/?$/.test(uri)) return ['Batches of the test network', 'The batches page of the public test network, read from the chain.'];
  if (/^\/name-domains/.test(uri)) return ['Names on the test network', 'The names page of the public test network, read from the chain.'];
  if (/^\/gas-tracker\/?$/.test(uri)) return ['Gas on the test network', 'The gas price of the public test network, read from the chain as it runs.'];
  if (/^\/apps/.test(uri)) return ['Applications on the test network', 'Applications that run on the public test network, as the explorer lists them.'];
  if (/^\/account\//.test(uri) || /^\/auth\//.test(uri)) return ['Your account on the explorer', 'Your own watch list, tags and keys on the explorer of the public test network.'];
  return [HOME_TITLE, HOME_DESC];
}

function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); }

function headFor(uri) {
  const p = page(uri);
  const title = esc(p[0]), desc = esc(p[1]), url = esc(HOST + (uri === '' ? '/' : uri));
  return '<title>' + title + '</title>' +
    '<meta name="description" content="' + desc + '"/>' +
    '<link rel="canonical" href="' + url + '"/>' +
    '<meta property="og:type" content="website"/>' +
    '<meta property="og:site_name" content="' + SITE_NAME + '"/>' +
    '<meta property="og:url" content="' + url + '"/>' +
    '<meta property="og:title" content="' + title + '"/>' +
    '<meta property="og:description" content="' + desc + '"/>' +
    '<meta property="og:image" content="' + IMAGE + '"/>' +
    '<meta property="og:image:type" content="image/png"/>' +
    '<meta property="og:image:width" content="1200"/>' +
    '<meta property="og:image:height" content="630"/>' +
    '<meta name="twitter:card" content="summary_large_image"/>' +
    '<meta name="twitter:title" content="' + title + '"/>' +
    '<meta name="twitter:description" content="' + desc + '"/>' +
    '<meta name="twitter:image" content="' + IMAGE + '"/>' +
    '<link rel="icon" href="/icons/favicon.ico" sizes="16x16 32x32 48x48"/>' +
    '<link rel="icon" href="/icons/favicon.svg" type="image/svg+xml"/>' +
    '<link rel="icon" href="/icons/favicon-16.png" sizes="16x16" type="image/png"/>' +
    '<link rel="icon" href="/icons/favicon-32.png" sizes="32x32" type="image/png"/>' +
    '<link rel="icon" href="/icons/favicon-48.png" sizes="48x48" type="image/png"/>' +
    '<link rel="apple-touch-icon" href="/icons/apple-touch-icon.png" sizes="180x180"/>' +
    '<link rel="manifest" href="/site.webmanifest"/>';
}

// the vendor's own head lines that the block above replaces; everything else in the head stays
const STRIP = [
  /<title[^>]*>[\s\S]*?<\/title>/g,
  /<meta\s+(?:name|property)="(?:description|og:[^"]*|twitter:[^"]*)"[^>]*>/g,
  /<link\s+rel="canonical"[^>]*>/g,
  /<link\s+rel="(?:icon|shortcut icon|apple-touch-icon|manifest|mask-icon)"[^>]*>/g,
];

function rewrite(html, uri) {
  const end = html.indexOf('</head>');
  if (end < 0) return html;
  let head = html.slice(0, end);
  const open = /<head[^>]*>/.exec(head);
  if (!open) return html;
  for (let i = 0; i < STRIP.length; i++) head = head.replace(STRIP[i], '');
  const at = open.index + open[0].length;
  head = head.slice(0, at) + headFor(uri) + head.slice(at);
  return head + html.slice(end);
}

function isHtml(r) { return /^text\/html/.test(r.headersOut['Content-Type'] || ''); }

function headers(r) {
  if (isHtml(r)) { delete r.headersOut['Content-Length']; delete r.headersOut['ETag']; }
}

// the whole body is held until its last chunk, then rewritten once (the head is not always inside the first chunk);
// it is held in the request's own variable $igneum_held (js_var in the template): the module's scope does not carry
// state from one chunk's call to the next
function body(r, data, flags) {
  if (!isHtml(r)) { r.sendBuffer(data, flags); return; }
  r.variables.igneum_held = (r.variables.igneum_held || '') + data;
  if (flags.last) {
    const html = r.variables.igneum_held;
    r.variables.igneum_held = '';
    let out;
    try { out = rewrite(html, r.uri); } catch (e) { r.error('igneum-head: rewrite failed, the page served as it came: ' + e); out = html; }
    r.sendBuffer(out, flags);
  }
}

export default { headers, body, page, rewrite };
