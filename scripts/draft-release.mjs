import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { readPreparedRelease, releaseDirectory, root } from "./release.mjs";

const repository = "SECWG/UIR-Agent";
const tag = process.env.RELEASE_TAG || process.env.GITHUB_REF_NAME;
const api = `https://api.github.com/repos/${repository}`;
let releaseId;

async function github(path, allowMissing = false) {
  const response = await fetch(`${api}/${path}`, {
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${process.env.GH_TOKEN}`,
      "x-github-api-version": "2022-11-28",
    },
    signal: AbortSignal.timeout(30000),
  });
  if (allowMissing && response.status === 404) return null;
  if (!response.ok)
    throw new Error(`GitHub request failed: HTTP ${response.status}`);
  return response.json();
}

async function currentDraft() {
  if (releaseId === undefined) {
    // The tag endpoint only finds published releases; authenticated listings include drafts.
    for (let page = 1; ; page++) {
      const releases = await github(`releases?per_page=100&page=${page}`);
      if (!Array.isArray(releases)) throw new Error("Invalid release listing");
      const match = releases.find((release) => release.tag_name === tag);
      if (match) {
        if (!Number.isSafeInteger(match.id) || match.id <= 0)
          throw new Error("Invalid release ID");
        releaseId = match.id;
        break;
      }
      if (releases.length < 100) return null;
    }
  }
  // Pin the ID so a deleted/replaced draft cannot redirect subsequent uploads.
  const release = await github(`releases/${releaseId}`, true);
  if (!release) throw new Error("Draft release disappeared during upload");
  if (
    release.id !== releaseId ||
    release.draft !== true ||
    release.prerelease !== false ||
    release.tag_name !== tag
  )
    throw new Error(
      "Refusing to modify an existing published or prerelease version",
    );
  return release;
}

function gh(args) {
  execFileSync("gh", args, {
    cwd: root,
    stdio: ["ignore", "ignore", "inherit"],
    windowsHide: true,
  });
}

try {
  const tagPush =
    process.env.GITHUB_EVENT_NAME === "push" &&
    process.env.GITHUB_REF === `refs/tags/${tag}`;
  const manualDraft =
    process.env.GITHUB_EVENT_NAME === "workflow_dispatch" &&
    process.env.RELEASE_MODE === "draft" &&
    Boolean(process.env.RELEASE_TAG);
  if (
    process.env.GITHUB_ACTIONS !== "true" ||
    !(tagPush || manualDraft) ||
    process.env.GITHUB_REPOSITORY?.toLowerCase() !== repository.toLowerCase() ||
    !process.env.GH_TOKEN
  )
    throw new Error(
      "Draft release creation is restricted to official tag-push or explicit manual-draft workflows",
    );
  const prepared = await readPreparedRelease(tag);
  const commit = execFileSync("git", ["rev-parse", "HEAD"], {
    cwd: root,
    encoding: "utf8",
    windowsHide: true,
  }).trim();
  const remote = await github(`commits/${encodeURIComponent(tag)}`);
  if (remote.sha !== commit)
    throw new Error("Remote tag no longer matches the checked-out commit");
  let release = await currentDraft();
  const names = prepared.assets.map((asset) => asset.name);
  if (
    release &&
    (!Array.isArray(release.assets) ||
      release.assets.some((asset) => !names.includes(asset.name)))
  )
    throw new Error(
      "Draft contains unexpected attachments; review it before retrying",
    );
  if (!release) {
    gh([
      "release",
      "create",
      tag,
      "--draft",
      "--verify-tag",
      "--title",
      `UIR-Agent ${prepared.version}`,
      "--notes",
      "请等待构建流程成功结束，检查更新说明和附件后再手动发布。",
      "--repo",
      repository,
    ]);
  }
  // Recheck draft state before each upload; never deliberately overwrite a published release.
  for (const asset of prepared.assets) {
    release = await currentDraft();
    if (!release) throw new Error("Draft release disappeared during upload");
    const existing = release.assets.find((entry) => entry.name === asset.name);
    if (
      existing?.state === "uploaded" &&
      existing.size === asset.size &&
      existing.digest === `sha256:${asset.sha256}`
    )
      continue;
    if (existing) {
      if (!Number.isSafeInteger(existing.id) || existing.id <= 0)
        throw new Error(`Invalid asset ID: ${asset.name}`);
      gh([
        "api",
        "--hostname",
        "github.com",
        "--method",
        "DELETE",
        `repos/${repository}/releases/assets/${existing.id}`,
      ]);
      await currentDraft();
    }
    gh([
      "api",
      "--hostname",
      "github.com",
      "--method",
      "POST",
      "--header",
      "Content-Type: application/octet-stream",
      "--header",
      `Content-Length: ${asset.size}`,
      "--input",
      resolve(releaseDirectory, asset.name),
      `https://uploads.github.com/repos/${repository}/releases/${releaseId}/assets?name=${encodeURIComponent(asset.name)}`,
    ]);
  }
  release = await currentDraft();
  if (!release || release.assets.length !== prepared.assets.length)
    throw new Error("Incomplete draft release");
  for (const expected of prepared.assets) {
    const matching = release.assets.filter(
      (asset) => asset.name === expected.name,
    );
    if (
      matching.length !== 1 ||
      matching[0].state !== "uploaded" ||
      matching[0].size !== expected.size ||
      (matching[0].digest && matching[0].digest !== `sha256:${expected.sha256}`)
    )
      throw new Error(`Uploaded asset mismatch: ${expected.name}`);
  }
  console.log(
    `Draft ${tag} is ready with ${prepared.assets.length} attachments. Publish it manually after review.`,
  );
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
