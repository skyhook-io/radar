// Builds the package the design-sync converter consumes. @skyhook-io/k8s-ui
// ships source only, so this stands in for its missing `build`: a minified ESM
// dist, emitted .d.ts, and compiled Tailwind CSS under a sync-only package dir.
// Run from the repo root (cfg.buildCmd). Output is gitignored.
//
// - Entry = the main barrel plus charts/ and resources/renderers/, which are
//   reachable only through package.json subpath exports.
// - Minified because monaco alone is ~9MB unminified and the upload caps a
//   file at 12MB; the converter re-bundles without minifying.
// - shiki is stubbed: it loads every grammar through dynamic import, which the
//   converter's single IIFE can't split. CodeViewer falls back to a plain <pre>.
// - React stays the host page's (window.React), never a bundled copy.
// - monaco is deep-imported without extensions (only Vite resolves that).
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { createRequire } from 'node:module';

const ROOT = resolve('.');
const SRC_PKG = join(ROOT, 'packages/k8s-ui');
const OUT = join(SRC_PKG, '.ds-sync-pkg');
const require = createRequire(join(ROOT, '.ds-sync/package.json'));
const { build } = require('esbuild');
// Route react/react-dom/react-is/scheduler to the page's globals the same way
// the converter does, so CJS deps bundled here (their `require("react")`)
// don't become dynamic requires the browser can't satisfy.
const { reactShim } = await import(join(ROOT, '.ds-sync/lib/bundle.mjs'));

const srcPkg = JSON.parse(readFileSync(join(SRC_PKG, 'package.json'), 'utf8'));
rmSync(OUT, { recursive: true, force: true });
mkdirSync(join(OUT, 'stubs'), { recursive: true });

writeFileSync(join(OUT, 'entry.ts'), [
  "export * from '../src/index'",
  "export * from '../src/components/charts'",
  "export * from '../src/components/resources/renderers'",
  '',
].join('\n'));

writeFileSync(join(OUT, 'stubs/shiki.ts'), `
const esc = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
export async function codeToHtml(code: string, _opts?: unknown): Promise<string> {
  return \`<pre class="shiki"><code>\${esc(code)}</code></pre>\`
}
`);

writeFileSync(join(OUT, 'package.json'), JSON.stringify({
  name: srcPkg.name,
  version: srcPkg.version,
  type: 'module',
  module: 'dist/index.js',
  types: 'types/.ds-sync-pkg/entry.d.ts',
}, null, 2) + '\n');

const monacoDeep = {
  name: 'monaco-deep-imports',
  setup(b) {
    b.onResolve({ filter: /^monaco-editor\/esm\// }, (args) => {
      if (args.path.endsWith('.js')) return undefined;
      const p = join(ROOT, 'node_modules', args.path + '.js');
      return existsSync(p) ? { path: p } : undefined;
    });
    b.onResolve({ filter: /^shiki$/ }, () => ({ path: join(OUT, 'stubs/shiki.ts') }));
  },
};

await build({
  entryPoints: [join(OUT, 'entry.ts')],
  outfile: join(OUT, 'dist/index.js'),
  bundle: true,
  format: 'esm',
  platform: 'browser',
  target: 'es2020',
  minify: true,
  jsx: 'automatic',
  loader: { '.ttf': 'dataurl', '.svg': 'dataurl', '.png': 'dataurl', '.woff': 'dataurl', '.woff2': 'dataurl' },
  // Resolved here so dev-only UI (e.g. ReachabilityView's state switcher)
  // never reaches the converter, which would otherwise define DEV as true.
  define: {
    'process.env.NODE_ENV': '"production"',
    'import.meta.env': '{"DEV":false,"PROD":true,"MODE":"production"}',
  },
  plugins: [monacoDeep, reactShim],
  logLevel: 'warning',
});

// Declarations: tsc emits despite type errors, so failures here are real.
writeFileSync(join(OUT, 'tsconfig.json'), JSON.stringify({
  extends: '../tsconfig.json',
  compilerOptions: {
    noEmit: false,
    emitDeclarationOnly: true,
    declaration: true,
    allowImportingTsExtensions: true,
    rootDir: '..',
    outDir: './types',
    noUnusedLocals: false,
    noUnusedParameters: false,
  },
  include: ['entry.ts', 'stubs', '../src'],
  exclude: ['../src/**/*.test.ts', '../src/**/*.test.tsx'],
}, null, 2));
try {
  execFileSync('node', [join(ROOT, 'node_modules/typescript-7/bin/tsc'), '-p', join(OUT, 'tsconfig.json')], { stdio: 'inherit' });
} catch { /* type errors still emit; the .d.ts check below is the gate */ }
if (!existsSync(join(OUT, 'types/.ds-sync-pkg/entry.d.ts'))) {
  console.error('build-pkg: declaration emit produced no entry.d.ts');
  process.exit(1);
}

// Tailwind v4: compile utilities used by the package sources and any authored
// previews, over the package's own theme layers.
const twVersion = JSON.parse(readFileSync(join(ROOT, 'node_modules/tailwindcss/package.json'), 'utf8')).version;
const twBin = join(ROOT, '.ds-sync/node_modules/.bin/tailwindcss');
if (!existsSync(twBin)) {
  execFileSync('npm', ['i', `@tailwindcss/cli@${twVersion}`], { cwd: join(ROOT, '.ds-sync'), stdio: 'inherit' });
}
// The app's @theme (DM Sans/Mono font stacks, radii, shadows, breakpoints) and
// base rules live in web/src/index.css, not in k8s-ui's theme files. Without
// them every card falls back to the system font and Tailwind's default radii.
const appCss = readFileSync(join(ROOT, 'web/src/index.css'), 'utf8');
function topLevelBlocks(css, selectorRx) {
  const out = [];
  const re = new RegExp(`^(${selectorRx})\\s*\\{`, 'gm');
  let m;
  while ((m = re.exec(css))) {
    let depth = 0, i = css.indexOf('{', m.index);
    for (let j = i; j < css.length; j++) {
      if (css[j] === '{') depth++;
      else if (css[j] === '}' && --depth === 0) { out.push(css.slice(m.index, j + 1)); break; }
    }
  }
  return out;
}
const appTheme = topLevelBlocks(appCss, '@theme|:root|body').join('\n\n');
if (!appTheme.includes('--font-sans')) {
  console.error('build-pkg: no @theme --font-sans found in web/src/index.css');
  process.exit(1);
}
writeFileSync(join(OUT, 'tailwind-input.css'), `@import "tailwindcss";
@source "../src/**/*.{ts,tsx}";
@source "../../../.design-sync/previews/**/*.tsx";
@variant dark (&:where(.dark, .dark *));
@import "../src/theme/variables.css";
@import "../src/theme/tailwind-theme.css";
@import "../src/theme/components.css";
@import "../src/topology.css";

${appTheme}
`);
execFileSync(twBin, ['-i', 'tailwind-input.css', '-o', 'tailwind.css'], { cwd: OUT, stdio: 'inherit' });

// One stylesheet: Tailwind output + the CSS the dist imported (xyflow, xterm,
// monaco). The converter takes a single cssEntry.
const distCss = join(OUT, 'dist/index.css');
writeFileSync(join(OUT, 'styles.css'),
  readFileSync(join(OUT, 'tailwind.css'), 'utf8') + '\n' +
  (existsSync(distCss) ? readFileSync(distCss, 'utf8') : ''));
rmSync(distCss, { force: true });
console.log(`build-pkg: ${srcPkg.name}@${srcPkg.version} -> ${OUT}`);
