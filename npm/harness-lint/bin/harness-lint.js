#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');

const PLATFORM_PACKAGES = Object.freeze({
  'darwin/arm64': '@kespineira/harness-lint-darwin-arm64',
  'darwin/x64': '@kespineira/harness-lint-darwin-x64',
  'linux/arm64': '@kespineira/harness-lint-linux-arm64',
  'linux/x64': '@kespineira/harness-lint-linux-x64',
});

class LauncherError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'LauncherError';
    this.code = code;
  }
}

function packageFor(platform, arch) {
  const packageName = PLATFORM_PACKAGES[`${platform}/${arch}`];
  if (!packageName) {
    throw new LauncherError(
      `unsupported platform or architecture: ${platform}/${arch}. ` +
        'Supported targets are darwin/arm64, darwin/x64, linux/arm64, and linux/x64.',
      'UNSUPPORTED_PLATFORM',
    );
  }
  return packageName;
}

function packageRoot(packageName, fromDirectory) {
  try {
    const manifest = require.resolve(`${packageName}/package.json`, {
      paths: [fromDirectory],
    });
    return path.dirname(manifest);
  } catch {
    throw new LauncherError(
      `optional dependency ${packageName} is not installed.`,
      'MISSING_DEPENDENCY',
    );
  }
}

function installHint(platform, arch) {
  return (
    `Reinstall without disabling optional dependencies using npm install -g harness-lint ` +
    `(target ${platform}/${arch}), or download a release from ` +
    'https://github.com/kespineira/harness-lint/releases/latest.'
  );
}

function resolveNativeBinary(
  packageName,
  fromDirectory = __dirname,
  platform = process.platform,
  arch = process.arch,
) {
  let root;
  try {
    root = packageRoot(packageName, fromDirectory);
  } catch (error) {
    if (error instanceof LauncherError && error.code !== 'MISSING_DEPENDENCY') throw error;
    throw new LauncherError(
      `optional dependency ${packageName} is not installed for ${platform}/${arch}. ` +
        installHint(platform, arch),
      'MISSING_DEPENDENCY',
    );
  }

  const binary = path.join(root, 'bin', 'harness-lint');
  try {
    const stat = fs.statSync(binary);
    if (!stat.isFile()) throw new Error('not a file');
    fs.accessSync(binary, fs.constants.X_OK);
  } catch {
    throw new LauncherError(
      `executable ${path.join(packageName, 'bin', 'harness-lint')} is missing or not executable for ` +
        `${platform}/${arch}. ${installHint(platform, arch)}`,
      'MISSING_BINARY',
    );
  }
  return binary;
}

function writeError(message, stderr = process.stderr) {
  stderr.write(`harness-lint: ${message}\n`);
}

function run(argv, options = {}) {
  const platform = options.platform || process.platform;
  const arch = options.arch || process.arch;
  const fromDirectory = options.fromDirectory || __dirname;
  const stderr = options.stderr || process.stderr;

  let binary;
  try {
    const packageName = packageFor(platform, arch);
    binary = resolveNativeBinary(packageName, fromDirectory, platform, arch);
  } catch (error) {
    writeError(error.message, stderr);
    return Promise.resolve(1);
  }

  const child = spawn(binary, argv, { stdio: 'inherit' });
  const signals = ['SIGHUP', 'SIGINT', 'SIGTERM', 'SIGQUIT'];
  let exited = false;
  const forward = (signal) => {
    if (!exited) child.kill(signal);
  };
  signals.forEach((signal) => process.on(signal, forward));

  const removeSignalHandlers = () => {
    signals.forEach((signal) => process.removeListener(signal, forward));
  };

  return new Promise((resolve) => {
    child.once('error', (error) => {
      exited = true;
      removeSignalHandlers();
      if (error.code === 'ENOENT') {
        writeError(
          `executable ${path.basename(path.dirname(binary))}/harness-lint could not be started for ` +
            `${platform}/${arch}. ${installHint(platform, arch)}`,
          stderr,
        );
      } else {
        writeError(`could not start native executable: ${error.message}`, stderr);
      }
      resolve(1);
    });

    child.once('exit', (code, signal) => {
      exited = true;
      removeSignalHandlers();
      if (signal) {
        // Re-emit the child's signal so callers observe the same termination
        // mode instead of an arbitrary launcher exit status.
        try {
          process.kill(process.pid, signal);
        } catch {
          resolve(128);
        }
        return;
      }
      resolve(code === null ? 1 : code);
    });
  });
}

if (require.main === module) {
  run(process.argv.slice(2)).then((code) => {
    process.exitCode = code;
  });
}

module.exports = {
  LauncherError,
  PLATFORM_PACKAGES,
  packageFor,
  packageRoot,
  resolveNativeBinary,
  run,
};
