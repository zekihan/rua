import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import { test, type TestContext } from "node:test";

function fixture(t: TestContext, script: string) {
  const root = mkdtempSync(join(tmpdir(), "rua-install-scripts-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const scripts = join(root, ".github", "scripts");
  mkdirSync(scripts, { recursive: true });
  mkdirSync(join(root, "web"));
  const scriptPath = join(scripts, script);
  copyFileSync(new URL(`../../.github/scripts/${script}`, import.meta.url), scriptPath);
  return { root, scriptPath };
}

test("the web installer disables lifecycle scripts", (t) => {
  const { root, scriptPath } = fixture(t, "install_build_deps.sh");
  const bin = join(root, "bin");
  mkdirSync(bin);
  writeFileSync(join(bin, "npm"), "#!/bin/sh\nprintf '%s\\n' \"$@\"\n", { mode: 0o755 });

  const result = spawnSync("sh", [scriptPath], {
    encoding: "utf8",
    env: { ...process.env, PATH: `${bin}${delimiter}${process.env.PATH}` },
  });
  assert.ifError(result.error);
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(result.stdout.trim().split("\n"), ["ci", "--prefix", "web", "--ignore-scripts"]);
});

test("the Linux policy checks exact versions and exempts only optional OS exclusions", (t) => {
  const { root, scriptPath } = fixture(t, "check_web_install_scripts.mjs");
  const scripted = { version: "1.0.0", hasInstallScript: true };
  const packages = {
    "node_modules/top-level": scripted,
    "node_modules/@scope/parent/node_modules/@scope/nested": scripted,
    "node_modules/alias": { ...scripted, name: "original" },
    "node_modules/mac-only": { ...scripted, optional: true, os: ["darwin"] },
    "node_modules/non-linux": { ...scripted, optional: true, os: ["!linux"] },
  };
  writeFileSync(join(root, "web", "package.json"), JSON.stringify({
    allowScripts: { "top-level@1.0.0": true, "@scope/nested@1.0.0": true, "original@1.0.0": true },
  }));
  const lockfile = join(root, "web", "package-lock.json");
  writeFileSync(lockfile, JSON.stringify({ lockfileVersion: 3, packages }));

  const accepted = spawnSync(process.execPath, [scriptPath], { encoding: "utf8" });
  assert.ifError(accepted.error);
  assert.equal(accepted.status, 0, accepted.stderr);

  writeFileSync(lockfile, JSON.stringify({ lockfileVersion: 3, packages: {
    ...packages,
    "node_modules/other/node_modules/top-level": { ...scripted, version: "2.0.0" },
    "node_modules/also-linux": { ...scripted, optional: true, os: ["darwin", "linux"] },
    "node_modules/required-mac": { ...scripted, os: ["darwin"] },
  } }));
  const rejected = spawnSync(process.execPath, [scriptPath], { encoding: "utf8" });
  assert.ifError(rejected.error);
  assert.equal(rejected.status, 1, rejected.stderr);
  assert.match(rejected.stderr, /Unapproved install script for top-level@2\.0\.0/);
  assert.match(rejected.stderr, /Unapproved install script for also-linux@1\.0\.0/);
  assert.match(rejected.stderr, /Unapproved install script for required-mac@1\.0\.0/);
});
