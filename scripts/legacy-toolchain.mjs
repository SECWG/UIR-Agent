import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  cp,
  chmod,
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { dirname, join, resolve, relative, isAbsolute } from "node:path";
import { fileURLToPath } from "node:url";

export const legacyToolchain = {
  version: "go1.27.4",
  upstream: "https://github.com/XTLS/go-win7/tree/dda9e23",
  patchSha256:
    "98f2fce830337c07d19786c1b872cfa6d725de333e7bafd5ce3009345514a236",
};

// Patch a PRIVATE copy of the matching official SDK. Only the linker has tool
// source changes; runtime/stdlib sources are rebuilt by go build automatically.
// Never mutate the global SDK, go.mod, or the standard release build.
export async function prepareLegacyToolchain(root) {
  const patch = resolve(
    dirname(fileURLToPath(import.meta.url)),
    "legacy/go-1-27.diff",
  );
  const patchHash = createHash("sha256")
    .update(await readFile(patch))
    .digest("hex");
  if (patchHash !== legacyToolchain.patchSha256)
    throw new Error("Legacy Go patch digest mismatch");
  const host = `${process.platform === "win32" ? "windows" : process.platform}-${process.arch === "x64" ? "amd64" : process.arch}`;
  const cache = resolve(root, ".cache/legacy-toolchain");
  const destination = join(
    cache,
    `${legacyToolchain.version}-${host}-${patchHash.slice(0, 12)}`,
  );
  const exe = process.platform === "win32" ? ".exe" : "";
  const go = join(destination, "bin", `go${exe}`);
  const env = {
    ...process.env,
    GOROOT: destination,
    GOTOOLCHAIN: "local",
    CGO_ENABLED: "0",
    GOCACHE: join(cache, `build-cache-${legacyToolchain.version}-${host}`),
  };
  delete env.GOOS;
  delete env.GOARCH;
  delete env.GOFLAGS;
  const stamp = { ...legacyToolchain, host, preparation: 2 };
  try {
    if (
      (await readFile(join(destination, "uir-legacy.json"), "utf8")) ===
      JSON.stringify(stamp)
    )
      return { go, env, stamp };
  } catch {
    /* Prepare a new, complete SDK below. */
  }
  const bootstrapEnv = { ...process.env, GOTOOLCHAIN: legacyToolchain.version };
  delete bootstrapEnv.GOROOT;
  const source = execFileSync("go", ["env", "GOROOT"], {
    cwd: root,
    env: bootstrapEnv,
    encoding: "utf8",
    windowsHide: true,
  }).trim();
  if (
    !(await readFile(join(source, "VERSION"), "utf8")).startsWith(
      `${legacyToolchain.version}\n`,
    )
  )
    throw new Error("Legacy build requires the pinned official Go SDK");
  await mkdir(cache, { recursive: true });
  const stage = await mkdtemp(join(cache, ".prepare-"));
  try {
    console.log(
      `Preparing isolated ${legacyToolchain.version} Windows legacy toolchain`,
    );
    await cp(source, stage, { recursive: true });
    const options = { cwd: stage, stdio: "inherit", windowsHide: true };
    // Apply against the SDK directory, not the enclosing Agent repository.
    const patchOptions = {
      ...options,
      env: { ...process.env, GIT_CEILING_DIRECTORIES: cache },
    };
    execFileSync("git", ["apply", "--check", patch], patchOptions);
    execFileSync("git", ["apply", patch], patchOptions);
    // The copied compiler/go driver are unchanged by this patch. Rebuild the
    // patched linker before building any endpoint executable.
    execFileSync(
      join(stage, "bin", `go${exe}`),
      ["build", "-a", "-o", join(stage, `uir-patched-link${exe}`), "cmd/link"],
      {
        ...options,
        env: { ...env, GOROOT: stage },
      },
    );
    const linker = join(
      stage,
      "pkg",
      "tool",
      host.replace("-", "_"),
      `link${exe}`,
    );
    // Toolchain modules are read-only in Go's module cache; cp preserves that
    // mode on Windows. Only our private copy is made writable.
    await chmod(linker, 0o755);
    await copyFile(join(stage, `uir-patched-link${exe}`), linker);
    await rm(join(stage, `uir-patched-link${exe}`));
    // Windows may briefly retain the just-built linker directory. Copying the
    // completed SDK avoids renaming a directory still held by the build tools.
    const destinationChild = relative(cache, destination);
    if (
      !destinationChild.startsWith(`${legacyToolchain.version}-`) ||
      destinationChild.includes("..") ||
      isAbsolute(destinationChild)
    )
      throw new Error("Invalid private toolchain path");
    await rm(destination, { recursive: true, force: true });
    await cp(stage, destination, { recursive: true });
    await writeFile(
      join(destination, "uir-legacy.json"),
      JSON.stringify(stamp),
    );
  } finally {
    const child = relative(cache, stage);
    if (
      !child.startsWith(".prepare-") ||
      child.includes("..") ||
      isAbsolute(child)
    )
      throw new Error("Invalid toolchain staging path");
    await rm(stage, { recursive: true, force: true });
  }
  return { go, env, stamp };
}
