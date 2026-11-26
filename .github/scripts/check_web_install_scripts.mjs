import { readFileSync } from "node:fs";

const web = new URL("../../web/", import.meta.url);
const { allowScripts = {} } = JSON.parse(readFileSync(new URL("package.json", web), "utf8"));
const { packages } = JSON.parse(readFileSync(new URL("package-lock.json", web), "utf8"));

function excludesLinux(os) {
  const platforms = typeof os === "string" ? [os] : os ?? [];
  if (platforms.includes("!linux")) return true;
  if (platforms.length === 1 && platforms[0] === "any") return false;
  return platforms.some((platform) => !platform.startsWith("!")) && !platforms.includes("linux");
}

for (const [path, pkg] of Object.entries(packages)) {
  if (pkg.hasInstallScript !== true || (pkg.optional === true && excludesLinux(pkg.os))) continue;

  const name = pkg.name ?? path.split(/(?:^|\/)node_modules\//).at(-1);
  const entry = `${name}@${pkg.version}`;
  if (name && pkg.version && allowScripts[entry] === true) continue;

  console.error(`Unapproved install script for ${entry} (${path}) in web/package-lock.json`);
  process.exitCode = 1;
}
