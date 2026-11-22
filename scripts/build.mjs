import { execFileSync } from "node:child_process";
import {
  mkdir,
  mkdtemp,
  readFile,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { createHash } from "node:crypto";
import { resolve, dirname, relative, isAbsolute } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import {
  prepareWindowsRelease,
  signingOptions,
  windowsResources,
} from "./windows-release.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const { values, positionals } = parseArgs({
  options: { "with-x86": { type: "boolean", default: false } },
  allowPositionals: true,
});
if (positionals.length > 1)
  throw new Error(
    "Usage: node scripts/build.mjs [output-directory] [--with-x86]",
  );
const out = resolve(root, positionals[0] || "dist");
const version = /const Version = "([^"]+)"/.exec(
  await readFile(resolve(root, "internal/agent/version.go"), "utf8"),
)?.[1];
if (!version) throw new Error("Agent version is missing");
windowsResources(version, "uir-agent.exe");
const signing = signingOptions(process.env);
if (signing && process.platform !== "win32")
  throw new Error(
    "Configured Authenticode signing requires a Windows build host",
  );
await mkdir(out, { recursive: true });
const stage = await mkdtemp(resolve(out, ".build-"));
const releases = [];
try {
  for (const os of ["windows", "linux", "darwin"])
    for (const arch of [
      "amd64",
      "arm64",
      ...(values["with-x86"] && os !== "darwin" ? ["386"] : []),
    ]) {
      const name =
        "uir-agent-" +
        (os === "windows" ? "win" : os) +
        "-" +
        arch +
        (os === "windows" ? ".exe" : "");
      const binary = resolve(stage, name);
      execFileSync(
        "go",
        [
          "build",
          "-trimpath",
          "-ldflags=-s -w",
          "-o",
          binary,
          "./cmd/ir-agent",
        ],
        {
          cwd: root,
          env: { ...process.env, CGO_ENABLED: "0", GOOS: os, GOARCH: arch },
          stdio: "inherit",
          windowsHide: true,
        },
      );
      if (os === "windows") {
        await prepareWindowsRelease({
          binary,
          filename: "uir-agent.exe",
          version,
          resourcePath: resolve(stage, arch + ".resources.json"),
          root,
          signing,
          env: process.env,
        });
      }
      // 必须在资源写入、签名和验证全部完成后计算下载摘要。
      const bytes = await readFile(binary);
      releases.push({
        name,
        os,
        arch,
        size: bytes.length,
        sha256: createHash("sha256").update(bytes).digest("hex"),
      });
    }
  await writeFile(
    resolve(stage, "manifest.json"),
    JSON.stringify({ version, releases }, null, 2),
  );
  for (const { name } of releases)
    await rename(resolve(stage, name), resolve(out, name));
  await rename(resolve(stage, "manifest.json"), resolve(out, "manifest.json"));
} finally {
  const child = relative(out, stage);
  if (!child.startsWith(".build-") || child.includes("..") || isAbsolute(child))
    throw new Error("Invalid build staging path");
  await rm(stage, { recursive: true, force: true });
}
console.log("Built " + releases.length + " targets in " + out);
console.log(
  signing
    ? "Windows Authenticode signatures verified."
    : "Windows releases are unsigned: no signing certificate configured.",
);
