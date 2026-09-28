// Builds the package the design-sync converter consumes. @skyhook-io/k8s-ui
// ships source only, so this stands in for its missing `build`: a minified ESM
// dist, emitted .d.ts, and compiled Tailwind CSS under a sync-only package dir.
// Run from the repo root (cfg.buildCmd). Output is gitignored.
//
// - Entry = the main barrel plus charts/ and resources/renderers/, which are
//   reachable only through package.json subpath exports, plus WEB_COMPONENTS:
//   the prop-driven feature components in web/ (radar-app). Most of web/ fetches
//   its own data and can't render without a backend, so it is opt-in per export.
// - Minified because monaco alone is ~9MB unminified and the upload caps a
//   file at 12MB; the converter re-bundles without minifying.
// - shiki is stubbed: it loads every grammar through dynamic import, which the
//   converter's single IIFE can't split. CodeViewer falls back to a plain <pre>.
// - React stays the host page's (window.React), never a bundled copy.
// - Monaco is stubbed: it is ~6MB of the bundle and the upload caps a file at
//   12MB. Editor/DiffEditor render the same YAML as a read-only code view and a
//   line diff, which is what a design needs; editing behavior never matters there.
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
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

// web/src/components/<file> -> exports. Only components that render from props
// alone (no React Query, router, or app-context reads) belong here.
const WEB_COMPONENTS = {
  'diagnose/AnalysisStory.tsx': ['AnalysisStory'],
  'diagnose/StoryExcerpt.tsx': ['StoryExcerpt'],
  'diagnose/AssessmentCard.tsx': ['ResultCard', 'AssessmentSources', 'WorkingNotes', 'AllClearCard', 'InconclusiveCard'],
  'diagnose/EvidenceCard.tsx': ['EvidenceCard'],
  'diagnose/InvestigationEvidencePane.tsx': ['InvestigationEvidencePane'],
  'diagnose/InvestigationEvidenceSections.tsx': ['RuledOutBlock', 'AssessmentEvidenceQualification', 'EmptyCollection', 'CollapsedEvidenceCollection', 'CoverageStrip'],
  'diagnose/InvestigationResourceEvidence.tsx': ['InvestigationResourceEvidence'],
  'diagnose/AgentCase.tsx': ['AgentRoleChip', 'AgentClaimNote'],
  'diagnose/AgentControls.tsx': ['Segmented', 'AgentControls', 'ConsentCard'],
  'diagnose/AgentSetupNotice.tsx': ['AgentSetupNotice'],
  'diagnose/ActivityTurn.tsx': ['TurnView', 'FollowupAnswer'],
  'diagnose/ApplyDialog.tsx': ['ApplyDialog', 'ApplyOutcomeCard'],
  'diagnose/AIMarkdown.tsx': ['AIMarkdown'],
  'diagnose/Home.tsx': ['InvestigationHome', 'RecentList'],
  'capacity/ClusterSchedulingCard.tsx': ['ClusterSchedulingCard'],
  'cost/CurrentAllocationUse.tsx': ['CurrentAllocationUse'],
  'helm/ManifestViewer.tsx': ['ManifestViewer'],
  'helm/RevisionHistory.tsx': ['RevisionHistory'],
  'helm/ValuesDiffPreview.tsx': ['ValuesDiffPreview'],
  'home/TopologyPreview.tsx': ['TopologyPreview'],
  'timeline/LocalTimelineScrubber.tsx': ['LocalTimelineScrubber'],
  'traffic/TrafficGraph.tsx': ['TrafficGraph'],
  'traffic/TrafficFilterSidebar.tsx': ['TrafficFilterSidebar'],
};
const WEB_SRC = join(ROOT, 'web/src');

writeFileSync(join(OUT, 'entry.ts'), [
  "export * from '../src/index'",
  "export * from '../src/components/charts'",
  "export * from '../src/components/resources/renderers'",
  ...Object.entries(WEB_COMPONENTS).map(([file, names]) =>
    `export { ${names.join(', ')} } from '../../../web/src/components/${file.replace(/\.tsx$/, '')}'`),
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
  types: 'types/packages/k8s-ui/.ds-sync-pkg/entry.d.ts',
}, null, 2) + '\n');

writeFileSync(join(OUT, 'stubs/monaco-react.tsx'), `
import type { CSSProperties, ReactNode } from 'react'

const pane: CSSProperties = {
  height: '100%', overflow: 'auto', margin: 0, padding: '8px 0',
  background: 'var(--bg-base)', color: 'var(--text-primary)',
  fontFamily: 'var(--font-mono, ui-monospace, monospace)', fontSize: 12, lineHeight: '20px',
}
const gutter: CSSProperties = {
  display: 'inline-block', width: 40, paddingRight: 12, textAlign: 'right',
  color: 'var(--text-tertiary)', userSelect: 'none',
}
const TONE = {
  ' ': undefined,
  '-': 'rgba(239, 68, 68, 0.14)',
  '+': 'rgba(34, 197, 94, 0.14)',
} as const

function Lines({ rows }: { rows: { n: ReactNode; mark: keyof typeof TONE; text: string }[] }) {
  return (
    <pre style={pane}>
      {rows.map((r, i) => (
        <div key={i} style={{ background: TONE[r.mark], whiteSpace: 'pre' }}>
          <span style={gutter}>{r.n}</span>
          <span style={{ color: 'var(--text-tertiary)', paddingRight: 8 }}>{r.mark === ' ' ? ' ' : r.mark}</span>
          {r.text}
        </div>
      ))}
    </pre>
  )
}

export default function Editor(props: { value?: string; defaultValue?: string }) {
  const text = props.value ?? props.defaultValue ?? ''
  return <Lines rows={text.split('\\n').map((t, i) => ({ n: i + 1, mark: ' ', text: t }))} />
}

function diffLines(a: string[], b: string[]) {
  const dp = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0))
  for (let i = a.length - 1; i >= 0; i--)
    for (let j = b.length - 1; j >= 0; j--)
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
  const out: { n: number; mark: ' ' | '-' | '+'; text: string }[] = []
  let i = 0, j = 0
  while (i < a.length || j < b.length) {
    if (i < a.length && j < b.length && a[i] === b[j]) { out.push({ n: j + 1, mark: ' ', text: a[i] }); i++; j++ }
    else if (i < a.length && (j >= b.length || dp[i + 1][j] >= dp[i][j + 1])) { out.push({ n: i + 1, mark: '-', text: a[i] }); i++ }
    else { out.push({ n: j + 1, mark: '+', text: b[j] }); j++ }
  }
  return out
}

export function DiffEditor(props: { original?: string; modified?: string }) {
  const rows = diffLines((props.original ?? '').split('\\n'), (props.modified ?? '').split('\\n'))
  return <Lines rows={rows} />
}

export const loader = { config() {}, init: () => Promise.resolve({}) }
export function useMonaco() { return null }
`);

writeFileSync(join(OUT, 'stubs/monaco-api.ts'), `
const noop = () => {}
export const editor = {
  createWebWorker: () => ({ dispose: noop }),
  setTheme: noop, defineTheme: noop, setModelMarkers: noop,
  getModel: () => null, createModel: () => ({ dispose: noop }),
}
export const languages = { register: noop, setMonarchTokensProvider: noop }
export const Uri = { parse: (s: string) => ({ toString: () => s }), file: (s: string) => ({ toString: () => s }) }
export const MarkerSeverity = { Hint: 1, Info: 2, Warning: 4, Error: 8 }
`);
writeFileSync(join(OUT, 'stubs/monaco-yaml.ts'), `
export function configureMonacoYaml() { return { update: async () => {}, dispose: () => {} } }
`);
writeFileSync(join(OUT, 'stubs/empty.ts'), 'export {}\n');
// web/'s ThemeProvider calls the preferences API and forces <html class="dark">;
// synced components read the theme from the class the page already set instead.
writeFileSync(join(OUT, 'stubs/theme-context.tsx'), `
import type { ReactNode } from 'react'
type Theme = 'dark' | 'light'
export function useTheme() {
  const theme: Theme = typeof document !== 'undefined' && document.documentElement.classList.contains('dark') ? 'dark' : 'light'
  return { theme, setTheme: (_t: Theme) => {}, toggleTheme: () => {} }
}
export function ThemeProvider({ children }: { children: ReactNode }) { return <>{children}</> }
`);

const stubs = {
  name: 'sync-stubs',
  setup(b) {
    b.onResolve({ filter: /^@monaco-editor\/react$/ }, () => ({ path: join(OUT, 'stubs/monaco-react.tsx') }));
    b.onResolve({ filter: /^monaco-yaml(\/|$)/ }, () => ({ path: join(OUT, 'stubs/monaco-yaml.ts') }));
    b.onResolve({ filter: /^monaco-editor(\/|$)/ }, (args) => ({
      path: join(OUT, args.path.includes('editor.api') ? 'stubs/monaco-api.ts' : 'stubs/empty.ts'),
    }));
    b.onResolve({ filter: /^shiki$/ }, () => ({ path: join(OUT, 'stubs/shiki.ts') }));
    b.onResolve({ filter: /context\/ThemeContext$/ }, (args) =>
      args.importer.startsWith(WEB_SRC) ? { path: join(OUT, 'stubs/theme-context.tsx') } : undefined);
    // web/'s Vite alias
    b.onResolve({ filter: /^@\// }, (args) => {
      const stem = join(WEB_SRC, args.path.slice(2));
      for (const ext of ['', '.ts', '.tsx', '/index.ts', '/index.tsx']) {
        if (existsSync(stem + ext) && statSync(stem + ext).isFile()) return { path: stem + ext };
      }
      return undefined;
    });
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
  plugins: [stubs, reactShim],
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
    rootDir: '../../..',
    outDir: './types',
    noUnusedLocals: false,
    noUnusedParameters: false,
    paths: {
      '@skyhook/k8s-ui': ['../src/index.ts'],
      '@skyhook/k8s-ui/*': ['../src/*'],
      '@/*': ['../../../web/src/*'],
    },
  },
  include: ['entry.ts', 'stubs', '../src'],
  exclude: ['../src/**/*.test.ts', '../src/**/*.test.tsx'],
}, null, 2));
try {
  execFileSync('node', [join(ROOT, 'node_modules/typescript-7/bin/tsc'), '-p', join(OUT, 'tsconfig.json')], { stdio: 'inherit' });
} catch { /* type errors still emit; the .d.ts check below is the gate */ }
if (!existsSync(join(OUT, 'types/packages/k8s-ui/.ds-sync-pkg/entry.d.ts'))) {
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
// web/src/index.css carries the app's @theme (DM Sans/Mono font stacks, radii,
// shadows, breakpoints), its component classes (investigation panels, metrics
// grid) and base rules. It is written to ship as a library stylesheet, so it is
// included whole minus its own @import/@source/@variant lines, which the input
// below already provides.
const appCss = readFileSync(join(ROOT, 'web/src/index.css'), 'utf8')
  .split('\n')
  .filter((line) => !/^@(import|source|variant)\b/.test(line))
  .join('\n');
if (!appCss.includes('--font-sans')) {
  console.error('build-pkg: no @theme --font-sans found in web/src/index.css');
  process.exit(1);
}
writeFileSync(join(OUT, 'tailwind-input.css'), `@import "tailwindcss";
@source "../src/**/*.{ts,tsx}";
@source "../../../.design-sync/previews/**/*.tsx";
${Object.keys(WEB_COMPONENTS).map((f) => `@source "../../../web/src/components/${f}";`).join('\n')}
@variant dark (&:where(.dark, .dark *));
@import "../src/theme/variables.css";
@import "../src/theme/tailwind-theme.css";
@import "../src/theme/components.css";
@import "../src/topology.css";

${appCss}
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
