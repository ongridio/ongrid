// Package only tracked source at HEAD, built assets, and explicit public artifacts.
import { execFileSync } from 'node:child_process';
import {
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  writeFileSync,
} from 'node:fs';
import { resolve } from 'node:path';
import { gzipSync } from 'node:zlib';
const root = resolve(import.meta.dirname, '..');
const pending = execFileSync(
  'git',
  ['status', '--porcelain', '--untracked-files=all'],
  { cwd: root, encoding: 'utf8' },
);
if (pending.trim())
  throw new Error(
    'Commit all source changes before packaging so the archive matches the build',
  );
const out = resolve(root, 'output/enterprise-release');
const html = resolve(out, 'html');
mkdirSync(html, { recursive: true });
cpSync(resolve(root, 'web/dist'), html, { recursive: true });
for (const file of ['Dockerfile', 'nginx.conf', 'compose.yaml'])
  cpSync(resolve(root, 'deploy/enterprise', file), resolve(out, file));
cpSync(resolve(root, 'LICENSE'), resolve(html, 'LICENSE.txt'));
const archive = execFileSync('git', ['archive', '--format=tar', 'HEAD'], {
  cwd: root,
  maxBuffer: 150 * 1024 * 1024,
});
writeFileSync(resolve(html, 'enterprise-source.tar.gz'), gzipSync(archive));
const manual = resolve(root, 'output/enterprise-manual/enterprise-manual.docx');
if (existsSync(manual)) cpSync(manual, resolve(html, 'enterprise-manual.docx'));
else if (process.env.ENTERPRISE_BOOTSTRAP !== '1')
  throw new Error(
    'Generate and visually verify output/enterprise-manual/enterprise-manual.docx before final packaging',
  );
else
  console.log(
    'Bootstrap package: generate the manual from this deployment, then publish the final package',
  );
const notices = [
  'Enterprise AI independent distribution. Based on Ongrid.\nUpstream LICENSE and TRADEMARK.md are included in the corresponding source archive.\nThird-party package notices follow.\n',
];
notices.push(readFileSync(resolve(root, 'TRADEMARK.md'), 'utf8'));
const modules = resolve(root, 'web/node_modules');
for (const entry of readdirSync(modules).filter((n) => !n.startsWith('.'))) {
  const packages = entry.startsWith('@')
    ? readdirSync(resolve(modules, entry)).map((n) => `${entry}/${n}`)
    : [entry];
  for (const name of packages) {
    const dir = resolve(modules, name);
    if (!existsSync(resolve(dir, 'package.json'))) continue;
    const p = JSON.parse(readFileSync(resolve(dir, 'package.json'), 'utf8'));
    notices.push(
      `\n--- ${p.name} ${p.version} (${JSON.stringify(p.license || 'see source')}) ---\n`,
    );
    for (const file of readdirSync(dir).filter((n) =>
      /^(licen[cs]e|copying|notice)(\.|$)/i.test(n),
    )) {
      try {
        notices.push(readFileSync(resolve(dir, file), 'utf8'));
      } catch {
        /* License directories are covered by the package's corresponding source. */
      }
    }
  }
}
notices.push(
  readFileSync(resolve(root, 'web/src/components/ui/shadcn.LICENSE'), 'utf8'),
);
writeFileSync(resolve(html, 'THIRD-PARTY-NOTICES.txt'), notices.join('\n'));
const version = execFileSync('git', ['rev-parse', '--short=12', 'HEAD'], {
  cwd: root,
  encoding: 'utf8',
}).trim();
writeFileSync(resolve(out, 'VERSION'), version + '\n');
writeFileSync(resolve(out, '.env'), `ENTERPRISE_VERSION=${version}\n`);
console.log(`Packaged ${version} in ${out}`);
