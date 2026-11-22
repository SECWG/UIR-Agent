<p align="center">
  <img src="assets/uir.svg" width="128" height="128" alt="UIR 平台图标" />
</p>

<h3 align="center">UIR-Agent</h3>
<p align="center">UIR 应急响应平台配套终端程序</p>
<p align="center">
  <a href="https://github.com/SECWG/UIR-Agent/stargazers">
    <img src="https://img.shields.io/github/stars/SECWG/UIR-Agent?style=flat-square&amp;color=yellow" alt="Stars" />
  </a>
  <a href="https://github.com/SECWG/UIR-Agent/network/members">
    <img src="https://img.shields.io/github/forks/SECWG/UIR-Agent?style=flat-square&amp;color=orange" alt="Forks" />
  </a>
  <a href="https://github.com/SECWG/UIR-Agent/releases">
    <img src="https://img.shields.io/github/downloads/SECWG/UIR-Agent/total?style=flat-square&amp;color=brightgreen" alt="Downloads" />
  </a>
  <a href="#快速使用">
    <img src="https://img.shields.io/badge/platform-Windows%20%7C%20Linux%20%7C%20macOS-blue?style=flat-square" alt="Windows / Linux / macOS" />
  </a>
</p>

## 简介

UIR 应急响应平台的配套终端程序，支持 Windows、Linux 和 macOS。连接平台后，可执行调查命令、采集文件，并进行远程设备管理。

## 主要功能

- 执行平台下发的命令，回传输出和执行结果。
- 采集、上传和下载文件，支持远程目录浏览。
- 查看主机基本信息，使用交互终端。
- 提供 SSH 中转，连接可达的内网设备。
- 后台运行、断线重连，通过平台退出或卸载。

## 快速使用

在 UIR 平台的「添加设备」中生成接入命令，在目标设备执行即可。也可以下载对应系统和架构的程序，手动指定平台地址和接入码：

```sh
./uir-agent --server "https://ir.example.com" --join-token "平台接入码"
```

Windows 使用 `./uir-agent.exe`，下载文件名不同时替换为实际文件名。

默认后台运行；添加 `--foreground` 可前台运行，按 `Ctrl+C` 退出。其他启动参数通过 `--help` 查看。

## 源码构建

安装 Go 1.27.4，在仓库根目录执行：

```sh
# Linux / macOS
go build -o uir-agent ./cmd/ir-agent

# Windows
go build -o uir-agent.exe ./cmd/ir-agent
```

批量构建还需 Node.js 24.19.0 或更高版本：

```sh
# Windows、Linux、macOS 的 amd64 / arm64 程序
node scripts/build.mjs

# 同时生成 Windows、Linux 的 32 位程序
node scripts/build.mjs --with-x86

# 旧版 Windows x64 兼容程序，另需安装 Git
node scripts/build-legacy.mjs
```

产物位于 `dist/`，兼容版位于 `dist/windows-legacy/`。

## 发布

发布标签对应的提交须包含本发布流程。`release.config.json` 中的 `platform.min` 为最低平台版本，首版设为 `0.0.0`，暂不限制平台版本；需要限定上界时设置 `maxExclusive`。版本与兼容范围会在编译前检查，未配置兼容范围仍可仅构建程序。

- 自动：推送与源码版本一致的 `vX.Y.Z` 标签，构建九种程序后生成 Release 草稿。
- 手动：进入 Actions → Build and prepare release → Run workflow，选择 `mode: draft`，在 `release_tag` 填写已有标签（如 `v1.0.0`）。流程使用该标签的源码与配置；`mode: build` 只构建，不创建草稿。

检查草稿说明和附件后再手动发布。已正式发布的版本不会被覆盖。
