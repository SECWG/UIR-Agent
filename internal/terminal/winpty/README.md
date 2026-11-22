# Embedded WinPTY runtime

WinPTY 0.4.3 provides interactive terminals on Windows x64 when ConPTY is unavailable.

The two gzip-compressed components in `amd64/` come from `x64/bin/` in the
[upstream 0.4.3 MSVC 2015 release](https://github.com/rprichard/winpty/releases/tag/0.4.3).
They use a static CRT and require no separate VC runtime installation.
See [manifest.json](./manifest.json) for source and SHA-256 hashes and
[LICENSE](./LICENSE) for the upstream MIT license.

These resources are embedded during compilation and must remain in the source tree.
