# 下游桌面发布

## 发布链路

`fork-desktop-release.yml` 随新的 `v*.*.*` tag 自动构建 Linux x64、Windows x64，以及 macOS arm64 和 x64 安装包。macOS 分别使用 `macos-15` 和 `macos-15-intel` 原生 runner，执行冻结锁文件安装，并以 `CSC_IDENTITY_AUTO_DISCOVERY=false`、`--publish never` 调用现有打包脚本。

所有平台构建成功后，统一上传安装包、blockmap 和 `latest*.yml` 到该 tag 对应的 GitHub Release。macOS 每个架构生成 dmg 和 zip：arm64 更新频道为 `latest-mac.yml`，x64 为 `latest-x64-mac.yml`。保留 zip 与元数据，供 electron-updater 使用。

本流程不提供 Developer ID 签名或 Apple 公证：arm64 为 ad-hoc 签名，x64 为未签名。首次打开若受 Gatekeeper 拦截，可尝试右键 → 打开；若系统仍阻止，按 [Apple 的说明](https://support.apple.com/102445)在“系统设置 → 隐私与安全性”中选择“仍要打开”。仅对确认来源的安装包执行此操作。签名和公证（方案 2）待 Owner 决定，不在 CI 引入 Apple 凭据。

新逻辑从合并后的下一个 tag 起生效；不重打或补传 `v0.7.0`。创建发布 tag 仍需 Owner 明确授权，PR 合并不等于发版。

## 无发布验证

- 修改桌面发布 workflow、打包脚本或 electron-builder 配置的 PR 会自动干跑全部四个构建任务，可在合并前验证 macOS 双架构。
- 合并后可在 Actions 中选择 **Desktop release (fork) → Run workflow**，对所选分支或 tag 干跑。
- PR 和手动运行仅保存 `desktop-linux-x64`、`desktop-win-x64`、`desktop-mac-arm64`、`desktop-mac-x64` workflow artifacts，保留 7 天；不创建或上传 GitHub Release。即使手动选择 tag，也不发布。
- 核对运行的 commit、两个 macOS 任务成功、各 artifact 内的 dmg、zip 与对应更新元数据。真正的发布闭环仍以下一个获准 tag 的 Release 附带 mac 制品为准。

## 踩坑记录

- `--publish never` 必须保留，避免 electron-builder 在 tag CI 中自行上传；只有 tag push 的统一上传任务拥有 Release 写权限。
- 两个 macOS 架构须使用各自的更新频道，避免同名元数据互相覆盖；不要将 x64 元数据改成 `latest-mac.yml`。
- `windows-2022` 仍用于现有 Windows 原生依赖构建，避免更换 runner 导致 node-gyp 找不到 MSVC。
- 构建与发布分开：任一平台构建失败时，本流程不上传任何平台到 Release；修复并重跑后再核对完整制品。`release.yml` 的服务端镜像链保持独立。
