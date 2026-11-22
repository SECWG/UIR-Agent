import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  mkdir,
  mkdtemp,
  readFile,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { dirname, resolve, relative, isAbsolute } from "node:path";
import { fileURLToPath } from "node:url";
import { prepareLegacyToolchain } from "./legacy-toolchain.mjs";
import { prepareWindowsRelease, signingOptions } from "./windows-release.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
if (process.argv.length > 3)
  throw new Error("Usage: node scripts/build-legacy.mjs [output-directory]");
const out = resolve(root, process.argv[2] || "dist/windows-legacy");
const version = /const Version = "([^"]+)"/.exec(
  await readFile(resolve(root, "internal/agent/version.go"), "utf8"),
)?.[1];
if (!version) throw new Error("Agent version is missing");
const toolchain = await prepareLegacyToolchain(root);
await mkdir(out, { recursive: true });
const stage = await mkdtemp(resolve(out, ".build-"));
const name = "uir-agent-win-amd64-legacy.exe";
try {
  const binary = resolve(stage, name);
  execFileSync(
    toolchain.go,
    [
      "build",
      "-trimpath",
      "-ldflags=-s -w -X github.com/SECWG/UIR-Agent/internal/agent.BuildProfile=windows-legacy",
      "-o",
      binary,
      "./cmd/ir-agent",
    ],
    {
      cwd: root,
      env: {
        ...toolchain.env,
        GOOS: "windows",
        GOARCH: "amd64",
        GOAMD64: "v1",
      },
      stdio: "inherit",
      windowsHide: true,
    },
  );
  await prepareWindowsRelease({
    binary,
    filename: name,
    version,
    legacy: true,
    resourcePath: resolve(stage, "resources.json"),
    root,
    signing: signingOptions(process.env),
    env: process.env,
  });
  const bytes = await readFile(binary);
  const pe = bytes.readUInt32LE(0x3c);
  const optional = pe + 24;
  if (
    bytes.toString("ascii", pe, pe + 4) !== "PE\0\0" ||
    bytes.readUInt16LE(pe + 4) !== 0x8664 ||
    bytes.readUInt16LE(optional + 40) > 6 ||
    bytes.readUInt16LE(optional + 48) > 6
  )
    throw new Error("Legacy executable has an incompatible PE target");
  const release = {
    name,
    os: "windows",
    arch: "amd64",
    version,
    size: bytes.length,
    sha256: createHash("sha256").update(bytes).digest("hex"),
    description:
      "Windows 7 SP1 / Server 2008 R2 SP1、2012、2012 R2（x64，需系统补丁）。支持交互终端。",
  };
  await writeFile(
    resolve(stage, "manifest.json"),
    JSON.stringify({ version, releases: [release] }, null, 2),
  );
  await writeFile(
    resolve(stage, "build-record.json"),
    JSON.stringify(
      {
        version,
        profile: "windows-legacy",
        target: "windows/amd64",
        toolchain: toolchain.stamp,
        cgo: false,
        goamd64: "v1",
        targetOSActuallyTested: false,
        terminalBackends: ["ConPTY", "WinPTY"],
        winpty: JSON.parse(
          await readFile(
            resolve(root, "internal/terminal/winpty/manifest.json"),
            "utf8",
          ),
        ),
        release,
      },
      null,
      2,
    ),
  );
  await writeFile(
    resolve(stage, "THIRD_PARTY_NOTICES.txt"),
    (await readFile(resolve(root, "scripts/legacy/LICENSE"), "utf8")) +
      "\n\nWinPTY 0.4.3\n\n" +
      (await readFile(
        resolve(root, "internal/terminal/winpty/LICENSE"),
        "utf8",
      )),
  );
  for (const file of [
    name,
    "manifest.json",
    "build-record.json",
    "THIRD_PARTY_NOTICES.txt",
  ])
    await rename(resolve(stage, file), resolve(out, file));
} finally {
  const child = relative(out, stage);
  if (!child.startsWith(".build-") || child.includes("..") || isAbsolute(child))
    throw new Error("Invalid legacy build staging path");
  await rm(stage, { recursive: true, force: true });
}
console.log(`Built Windows x64 legacy Agent in ${out}`);
