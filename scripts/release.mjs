import { createHash } from "node:crypto";
import {
  copyFile,
  mkdir,
  readFile,
  readdir,
  stat,
  writeFile,
} from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
export const releaseDirectory = resolve(root, "dist/release");
const stableVersion = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const maxFileSize = 64 * 1024 * 1024;
const maxTotalSize = 128 * 1024 * 1024;
const targets = [
  ["uir-agent-win-amd64.exe", "windows", "amd64"],
  ["uir-agent-win-arm64.exe", "windows", "arm64"],
  ["uir-agent-win-386.exe", "windows", "386"],
  ["uir-agent-linux-amd64", "linux", "amd64"],
  ["uir-agent-linux-arm64", "linux", "arm64"],
  ["uir-agent-linux-386", "linux", "386"],
  ["uir-agent-darwin-amd64", "darwin", "amd64"],
  ["uir-agent-darwin-arm64", "darwin", "arm64"],
  ["uir-agent-win-amd64-legacy.exe", "windows", "amd64"],
];

function requireValue(value, message) {
  if (!value) throw new Error(message);
}

export async function checkVersion(tag) {
  const source = await readFile(
    resolve(root, "internal/agent/version.go"),
    "utf8",
  );
  const version = /^const Version = "([^"]+)"$/m.exec(source)?.[1];
  requireValue(stableVersion.test(version ?? ""), "Invalid Agent version");
  requireValue(
    tag === `v${version}`,
    `Tag must be v${version}; received ${tag ?? "nothing"}`,
  );
  return version;
}

async function platformRange() {
  const { platform } = JSON.parse(
    await readFile(resolve(root, "release.config.json"), "utf8"),
  );
  requireValue(
    platform && typeof platform === "object" && !Array.isArray(platform),
    "Platform compatibility is not configured. Set a verified platform.min in release.config.json before preparing a release.",
  );
  const valid = (value) =>
    typeof value === "string" &&
    value.length <= 64 &&
    stableVersion.test(value);
  requireValue(
    Object.keys(platform).every((key) =>
      ["min", "maxExclusive"].includes(key),
    ) &&
      valid(platform.min) &&
      (platform.maxExclusive === undefined || valid(platform.maxExclusive)),
    "Invalid platform compatibility range",
  );
  if (platform.maxExclusive !== undefined) {
    const lower = platform.min.split(".").map(BigInt);
    const upper = platform.maxExclusive.split(".").map(BigInt);
    const difference = lower.findIndex(
      (value, index) => value !== upper[index],
    );
    requireValue(
      difference >= 0 && lower[difference] < upper[difference],
      "platform.maxExclusive must exceed platform.min",
    );
  }
  return platform;
}

async function fingerprint(file) {
  const info = await stat(file);
  requireValue(
    info.isFile() && info.size > 0 && info.size <= maxFileSize,
    `Invalid file size: ${file}`,
  );
  const bytes = await readFile(file);
  return {
    size: bytes.length,
    sha256: createHash("sha256").update(bytes).digest("hex"),
  };
}

async function validatePackages(packages, version, directoryFor) {
  requireValue(
    Array.isArray(packages) && packages.length === targets.length,
    "A release requires all nine Agent programs",
  );
  const seen = new Set();
  const result = [];
  for (const entry of packages) {
    const target = targets.find(([name]) => name === entry?.name);
    requireValue(
      target && !seen.has(entry.name),
      "Unknown or duplicate Agent program",
    );
    seen.add(entry.name);
    requireValue(
      entry.os === target[1] && entry.arch === target[2],
      `Target mismatch: ${entry.name}`,
    );
    requireValue(
      entry.version === undefined || entry.version === version,
      `Version mismatch: ${entry.name}`,
    );
    requireValue(
      Number.isSafeInteger(entry.size) &&
        entry.size > 0 &&
        entry.size <= maxFileSize &&
        typeof entry.sha256 === "string" &&
        /^[a-f0-9]{64}$/.test(entry.sha256) &&
        (entry.description === undefined ||
          (typeof entry.description === "string" &&
            entry.description.length <= 300)),
      `Invalid metadata: ${entry.name}`,
    );
    const actual = await fingerprint(
      resolve(directoryFor(entry.name), entry.name),
    );
    requireValue(
      actual.size === entry.size && actual.sha256 === entry.sha256,
      `Checksum mismatch: ${entry.name}`,
    );
    result.push({
      name: entry.name,
      os: entry.os,
      arch: entry.arch,
      version,
      ...actual,
      ...(entry.description === undefined
        ? {}
        : { description: entry.description }),
    });
  }
  requireValue(
    result.reduce((sum, entry) => sum + entry.size, 0) <= maxTotalSize,
    "Agent release exceeds 128 MiB",
  );
  return targets.map(([name]) => result.find((entry) => entry.name === name));
}

async function thirdPartyNotices() {
  return (
    (await readFile(resolve(root, "scripts/legacy/LICENSE"), "utf8")) +
    "\n\nWinPTY 0.4.3\n\n" +
    (await readFile(resolve(root, "internal/terminal/winpty/LICENSE"), "utf8"))
  );
}

export async function readPreparedRelease(tag) {
  const version = await checkVersion(tag);
  const platform = await platformRange();
  const index = JSON.parse(
    await readFile(resolve(releaseDirectory, "latest.json"), "utf8"),
  );
  requireValue(
    Object.keys(index).every((key) =>
      ["version", "tag", "platform", "packages"].includes(key),
    ),
    "Unexpected release metadata",
  );
  requireValue(
    index.version === version && index.tag === tag,
    "Prepared release version mismatch",
  );
  requireValue(
    JSON.stringify(index.platform) === JSON.stringify(platform),
    "Prepared platform range mismatch",
  );
  const packages = await validatePackages(
    index.packages,
    version,
    () => releaseDirectory,
  );
  requireValue(
    index.packages.every((entry) => entry.version === version),
    "Prepared package version missing",
  );
  requireValue(
    index.packages.every((entry) =>
      Object.keys(entry).every((key) =>
        [
          "name",
          "os",
          "arch",
          "version",
          "size",
          "sha256",
          "description",
        ].includes(key),
      ),
    ),
    "Unexpected package metadata",
  );
  requireValue(
    (await readFile(
      resolve(releaseDirectory, "THIRD_PARTY_NOTICES.txt"),
      "utf8",
    )) === (await thirdPartyNotices()),
    "Third-party notices mismatch",
  );
  const names = [
    ...packages.map((entry) => entry.name),
    "THIRD_PARTY_NOTICES.txt",
    "latest.json",
  ];
  const files = await readdir(releaseDirectory);
  requireValue(
    files.length === names.length &&
      files.every((name) => names.includes(name)),
    "Unexpected release attachment",
  );
  const assets = [];
  for (const name of names)
    assets.push({
      name,
      ...(await fingerprint(resolve(releaseDirectory, name))),
    });
  return { version, tag, assets };
}

async function prepareRelease(tag) {
  const version = await checkVersion(tag);
  const platform = await platformRange();
  const standard = JSON.parse(
    await readFile(resolve(root, "dist/manifest.json"), "utf8"),
  );
  const legacy = JSON.parse(
    await readFile(resolve(root, "dist/windows-legacy/manifest.json"), "utf8"),
  );
  requireValue(
    standard.version === version && legacy.version === version,
    "Build manifest version mismatch",
  );
  requireValue(
    Array.isArray(standard.releases) &&
      standard.releases.length === 8 &&
      Array.isArray(legacy.releases) &&
      legacy.releases.length === 1 &&
      legacy.releases[0]?.name === targets[8][0],
    "Incomplete build manifests",
  );
  const directoryFor = (name) =>
    resolve(root, name === targets[8][0] ? "dist/windows-legacy" : "dist");
  const packages = await validatePackages(
    [...standard.releases, ...legacy.releases],
    version,
    directoryFor,
  );
  const allowed = [
    ...packages.map((entry) => entry.name),
    "latest.json",
    "THIRD_PARTY_NOTICES.txt",
  ];
  await mkdir(releaseDirectory, { recursive: true });
  requireValue(
    (await readdir(releaseDirectory)).every((name) => allowed.includes(name)),
    "Unexpected file in release output directory",
  );
  for (const entry of packages)
    await copyFile(
      resolve(directoryFor(entry.name), entry.name),
      resolve(releaseDirectory, entry.name),
    );
  await writeFile(
    resolve(releaseDirectory, "THIRD_PARTY_NOTICES.txt"),
    await thirdPartyNotices(),
  );
  await writeFile(
    resolve(releaseDirectory, "latest.json"),
    JSON.stringify({ version, tag, platform, packages }, null, 2) + "\n",
  );
  await readPreparedRelease(tag);
  console.log(
    `Prepared ${packages.length} programs and release metadata in ${releaseDirectory}`,
  );
}

if (
  process.argv[1] &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  const [command, tag, ...extra] = process.argv.slice(2);
  try {
    requireValue(
      extra.length === 0 && ["check", "prepare"].includes(command),
      "Usage: node scripts/release.mjs <check|prepare> vX.Y.Z",
    );
    if (command === "check")
      console.log(`Agent ${await checkVersion(tag)} matches ${tag}`);
    else await prepareRelease(tag);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
