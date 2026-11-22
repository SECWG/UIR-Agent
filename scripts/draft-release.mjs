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
    throw new Error(`GitHub GET ${path} failed: HTTP ${response.status}`);
  return response.json();
}

async function currentDraft(waitForCreation = false) {
  if (releaseId === undefined) {
    // Like gh release view, resolve draft pending tags through GraphQL.
    const result = JSON.parse(
      gh([
        "api",
        "--hostname",
        "github.com",
        "graphql",
        "-f",
        "query=query($owner:String!,$name:String!,$tag:String!){repository(owner:$owner,name:$name){release(tagName:$tag){databaseId}}}",
        "-f",
        "owner=SECWG",
        "-f",
        "name=UIR-Agent",
        "-f",
        `tag=${tag}`,
      ]),
    );
    if (result.errors?.length || !result.data?.repository)
      throw new Error("GitHub draft lookup failed");
    const match = result.data.repository.release;
    if (match === null) return null;
    if (!Number.isSafeInteger(match?.databaseId) || match.databaseId <= 0)
      throw new Error("Invalid release ID");
    releaseId = match.databaseId;
  }
  // Pin the ID so a deleted/replaced draft cannot redirect subsequent uploads.
  let release;
  for (let attempt = 0; ; attempt++) {
    release = await github(`releases/${releaseId}`, true);
    if (release) break;
    if (!waitForCreation || attempt === 3)
      throw new Error(
        `GitHub cannot read draft ${tag} (ID ${releaseId}): HTTP 404`,
      );
    console.log(
      `Waiting for new draft ${tag} (ID ${releaseId}) to become readable`,
    );
    await new Promise((resolve) => setTimeout(resolve, 1000 * 2 ** attempt));
  }
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
  return execFileSync("gh", args, {
    cwd: root,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "inherit"],
    maxBuffer: 4 * 1024 * 1024,
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
    // Verify the existing tag, then retain the ID from the creation response.
    const ref = await github(`git/ref/tags/${encodeURIComponent(tag)}`);
    if (ref.ref !== `refs/tags/${tag}`)
      throw new Error("Release tag is missing");
    const created = JSON.parse(
      gh([
        "api",
        "--hostname",
        "github.com",
        "--method",
        "POST",
        `repos/${repository}/releases`,
        "-f",
        `tag_name=${tag}`,
        "-f",
        `target_commitish=${commit}`,
        "-F",
        "draft=true",
        "-F",
        "prerelease=false",
        "-f",
        `name=UIR-Agent ${prepared.version}`,
        "-f",
        "body=请等待构建流程成功结束，检查更新说明和附件后再手动发布。",
      ]),
    );
    if (
      !Number.isSafeInteger(created.id) ||
      created.id <= 0 ||
      created.tag_name !== tag ||
      created.draft !== true ||
      created.prerelease !== false
    )
      throw new Error("Invalid draft creation response");
    releaseId = created.id;
    console.log(`Created draft ${tag} (ID ${releaseId})`);
    release = await currentDraft(true);
  }
  console.log(`Uploading attachments to draft ${tag} (ID ${releaseId})`);
  // Recheck draft state before each upload; never deliberately overwrite a published release.
  for (const asset of prepared.assets) {
    release = await currentDraft();
    if (!release) throw new Error("Draft release could not be resolved");
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
