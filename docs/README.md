# 下游桌面发布

## 发布链路

`fork-desktop-release.yml` 随新的 `v*.*.*` tag 自动构建 Linux x64、Windows x64，以及 macOS arm64 和 x64 安装包。macOS 分别使用 `macos-15` 和 `macos-15-intel` 原生 runner，执行冻结锁文件安装，并以 `CSC_IDENTITY_AUTO_DISCOVERY=false`、`--publish never` 调用现有打包脚本。

所有平台构建成功后，发布任务在同一草稿收集安装包、blockmap 和 `latest*.yml`，校验版本、架构及远端文件哈希后发布为 Latest。macOS 每个架构生成 dmg 和 zip：arm64 更新频道为 `latest-mac.yml`，x64 为 `latest-x64-mac.yml`。保留 zip 与元数据，供 electron-updater 使用。

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
- 构建与发布分开：任一平台构建失败时，本流程不上传任何平台到 Release；修复并重跑后再核对完整制品。同一 tag 已公开时只核验资产。

## 容器镜像对

`fork-container-release.yml` 从同一稳定 tag 构建 backend/web 的 amd64 和 arm64 镜像。串行发布任务在 `container-pair` artifact 中封存两个索引，再按版本 tag、`latest` 的顺序发布。重跑使用同一 run 的清单；已存在的版本 tag 必须匹配封存 digest。首次正式发布前，管理员需按已批准的切换步骤停用并排空旧的 `release.yml`，运维需完成消费端切换。

升级入口在同一把锁内运行 `python3 scripts/fork-release/container-pair.py pin --last-successful last-successful.json --output candidate`。首次切换时，由运维从当前健康容器核实 backend 和 web 的 `tag`、`digest` 并写入成功记录。校验通过后，使用生成的 `candidate/compose.json` 作为最后一份 Compose 覆盖，用同一组参数执行 `config --images`、备份、pull、启动及健康检查。两侧健康后原子替换成功记录为 `candidate/pair.json`。每次调用使用新的输出目录。
