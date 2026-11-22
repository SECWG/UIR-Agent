import { execFileSync } from "node:child_process";
import { writeFile } from "node:fs/promises";

export function signingOptions(env) {
  const certificate = (env.WG_IR_SIGN_CERT_SHA1 || "").trim();
  const timestamp = (env.WG_IR_SIGN_TIMESTAMP_URL || "").trim();
  const tool = (env.WG_IR_SIGNTOOL || "").trim();
  if (!certificate && !timestamp && !tool) return null;
  if (!/^[a-f0-9]{40}$/i.test(certificate)) {
    throw new Error(
      "WG_IR_SIGN_CERT_SHA1 must be the exact certificate thumbprint from CurrentUser/My",
    );
  }
  let url;
  try {
    url = new URL(timestamp);
  } catch {
    /* 下方统一报告无效配置。 */
  }
  if (
    !url ||
    !["https:", "http:"].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.hash
  ) {
    throw new Error(
      "WG_IR_SIGN_TIMESTAMP_URL must be an HTTP/HTTPS RFC 3161 timestamp service URL",
    );
  }
  return { certificate, timestamp: url.href, tool: tool || "signtool.exe" };
}

export function windowsResources(version, filename, legacy = false) {
  if (
    !/^\d+\.\d+\.\d+$/.test(version) ||
    version.split(".").some((part) => Number(part) > 65535)
  ) {
    throw new Error(
      "Agent version must have three numeric components between 0 and 65535",
    );
  }
  return {
    RT_MANIFEST: {
      "#1": {
        "0409": {
          description: "UIR Endpoint Agent",
          "minimum-os": legacy ? "win7" : "win10",
          "execution-level": "as invoker",
          "ui-access": false,
          "auto-elevate": false,
          "long-path-aware": true,
        },
      },
    },
    RT_VERSION: {
      "#1": {
        "0000": {
          fixed: {
            file_version: version + ".0",
            product_version: version + ".0",
          },
          info: {
            "0409": {
              CompanyName: "SECWG",
              FileDescription: legacy
                ? "UIR Endpoint Agent (Windows Legacy)"
                : "UIR Endpoint Agent",
              FileVersion: version,
              InternalName: "uir-agent",
              OriginalFilename: filename,
              ProductName: "UIR",
              ProductVersion: version,
            },
          },
        },
      },
    },
  };
}

export async function prepareWindowsRelease(
  {
    binary,
    filename,
    version,
    resourcePath,
    root,
    signing,
    env,
    legacy = false,
  },
  run = execFileSync,
) {
  await writeFile(
    resourcePath,
    JSON.stringify(windowsResources(version, filename, legacy), null, 2),
  );
  // 资源工具运行于构建主机；目标架构已写入二进制，不能继承交叉编译的 GOOS/GOARCH。
  const toolEnv = { ...env };
  delete toolEnv.GOOS;
  delete toolEnv.GOARCH;
  const options = {
    cwd: root,
    env: toolEnv,
    stdio: "inherit",
    windowsHide: true,
  };
  run(
    "go",
    ["tool", "go-winres", "patch", "--in", resourcePath, "--no-backup", binary],
    options,
  );
  if (!signing) return;
  // 明确指定证书，不自动挑选证书、不导出私钥；签名或验证失败即停止发布。
  run(
    signing.tool,
    [
      "sign",
      "/s",
      "My",
      "/sha1",
      signing.certificate,
      "/fd",
      "SHA256",
      "/tr",
      signing.timestamp,
      "/td",
      "SHA256",
      binary,
    ],
    options,
  );
  run(signing.tool, ["verify", "/pa", "/all", "/tw", binary], options);
}
