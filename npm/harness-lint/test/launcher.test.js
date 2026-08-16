'use strict';

const assert = require('node:assert/strict');
const { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync, copyFileSync } = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const os = require('node:os');
const test = require('node:test');

const launcher = require('../bin/harness-lint.js');

const launcherPath = path.resolve(__dirname, '../bin/harness-lint.js');

test('maps every supported platform and architecture', () => {
  assert.deepEqual(
    Object.fromEntries(
      [
        ['darwin', 'arm64'],
        ['darwin', 'x64'],
        ['linux', 'arm64'],
        ['linux', 'x64'],
      ].map(([platform, arch]) => [`${platform}/${arch}`, launcher.packageFor(platform, arch)]),
    ),
    {
      'darwin/arm64': '@kespineira/harness-lint-darwin-arm64',
      'darwin/x64': '@kespineira/harness-lint-darwin-x64',
      'linux/arm64': '@kespineira/harness-lint-linux-arm64',
      'linux/x64': '@kespineira/harness-lint-linux-x64',
    },
  );
});

test('rejects unsupported platform and architecture', () => {
  for (const [platform, arch] of [
    ['freebsd', 'x64'],
    ['linux', 'ia32'],
    ['win32', 'x64'],
  ]) {
    assert.throws(
      () => launcher.packageFor(platform, arch),
      (error) => error.code === 'UNSUPPORTED_PLATFORM' && error.message.includes(`${platform}/${arch}`),
    );
  }
});

test('reports missing optional dependency and missing executable', () => {
  const root = mkdtempSync(path.join(os.tmpdir(), 'harness-lint-launcher-test-'));
  try {
    assert.throws(
      () => launcher.resolveNativeBinary('@kespineira/missing-native', root),
      (error) => error.code === 'MISSING_DEPENDENCY' && error.message.includes('Reinstall harness-lint'),
    );

    const packageRoot = path.join(root, 'node_modules', '@kespineira', 'harness-lint-linux-x64');
    mkdirSync(packageRoot, { recursive: true });
    writeFileSync(path.join(packageRoot, 'package.json'), '{"name":"@kespineira/harness-lint-linux-x64"}\n');
    assert.throws(
      () => launcher.resolveNativeBinary('@kespineira/harness-lint-linux-x64', root),
      (error) => error.code === 'MISSING_BINARY' && error.message.includes('Reinstall @kespineira/harness-lint-linux-x64'),
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

function makeFixture() {
  const root = mkdtempSync(path.join(os.tmpdir(), 'harness-lint-launcher-integration-'));
  const launcherBin = path.join(root, 'bin');
  const packageName = launcher.packageFor(process.platform, process.arch);
  const nativeRoot = path.join(root, 'node_modules', ...packageName.split('/'));
  mkdirSync(launcherBin, { recursive: true });
  mkdirSync(path.join(nativeRoot, 'bin'), { recursive: true });
  copyFileSync(launcherPath, path.join(launcherBin, 'harness-lint.js'));
  writeFileSync(path.join(nativeRoot, 'package.json'), `{"name":"${packageName}"}\n`);
  const native = path.join(nativeRoot, 'bin', 'harness-lint');
  writeFileSync(
    native,
    `#!/usr/bin/env node
const fs = require('node:fs');
const args = process.argv.slice(2);
process.stdout.write('stdout:' + args.join('|') + '\\n');
process.stderr.write('stderr:' + args.join('|') + '\\n');
if (args[0] === '--stdin') process.stdout.write('input:' + fs.readFileSync(0, 'utf8'));
if (args[0] === '--exit') process.exit(Number(args[1]));
if (args[0] === '--signal') setTimeout(() => process.kill(process.pid, 'SIGTERM'), 30);
`,
  );
  chmodSync(native, 0o755);
  return { root, launcher: path.join(launcherBin, 'harness-lint.js') };
}

function runFixture(fixture, args, input = '') {
  const child = spawn(process.execPath, [fixture.launcher, ...args], {
    cwd: fixture.root,
    env: { ...process.env },
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  child.stdin.end(input);
  return new Promise((resolve, reject) => {
    let stdout = '';
    let stderr = '';
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', (chunk) => (stdout += chunk));
    child.stderr.on('data', (chunk) => (stderr += chunk));
    child.once('error', reject);
    child.once('close', (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}

test('preserves argv, stdin, stdout, stderr, and exit code', async () => {
  const fixture = makeFixture();
  try {
    const result = await runFixture(fixture, ['--stdin', 'two words'], 'payload');
    assert.equal(result.code, 0);
    assert.equal(result.signal, null);
    assert.equal(result.stdout, 'stdout:--stdin|two words\ninput:payload');
    assert.equal(result.stderr, 'stderr:--stdin|two words\n');

    const exited = await runFixture(fixture, ['--exit', '17']);
    assert.equal(exited.code, 17);
    assert.equal(exited.signal, null);
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test('preserves a deterministic child signal', async () => {
  const fixture = makeFixture();
  try {
    const result = await runFixture(fixture, ['--signal']);
    assert.equal(result.code, null);
    assert.equal(result.signal, 'SIGTERM');
  } finally {
    rmSync(fixture.root, { recursive: true, force: true });
  }
});
