# CPA 主页面集成

此目录提供 Management Center 的局部覆盖文件，使 ACP 插件在 CPA 主面板中复用登录状态、同步主题，并支持凭证、OAuth 与配额操作。

基线：上游 [Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) **v1.27.4**，提交 `d06a110594e8a3949684834d990eeb9c8e888499`。覆盖代码对应定制标识 `v1.27.4-acp.6`，构建使用 Bun 1.4.3。升级上游后应重新合并并检查接口，不要盲目覆盖不同版本。

## 构建

先完成根目录 [部署步骤](../README.md#部署流程)。以下在 Linux Bash 执行，假定本仓库位于 `/opt/cpa-acp`，Bun 已安装：

~~~bash
cd /opt/cpa-acp
mkdir -p build
git clone --branch v1.27.4 --depth 1 \
  https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git \
  build/management-upstream

cp management-center/PluginResourcePage.tsx \
  build/management-upstream/src/features/plugins/PluginResourcePage.tsx
cp management-center/acpManagementBridge.ts \
  build/management-upstream/src/features/plugins/acpManagementBridge.ts
cp management-center/acpManagementBridge.test.ts \
  build/management-upstream/tests/acpManagementBridge.test.ts

cd build/management-upstream
bun install --frozen-lockfile
bun test tests/acpManagementBridge.test.ts
VERSION=v1.27.4-acp.6 bun run build
~~~

如果构建目录已经存在，先检查其版本和未提交修改，不要直接重复 clone 或覆盖自己的改动。产物为 `build/management-upstream/dist/index.html`，通过 Vite 单文件插件打包。

## 部署

备份现有 `management.html`。可让 CPA 显式使用独立静态目录：

~~~bash
cd /opt/cpa-acp
mkdir -p state/management
install -m 644 build/management-upstream/dist/index.html \
  state/management/management.html
~~~

在 CPA 自己的 Compose 服务中合并以下配置，保留其他环境变量与挂载：

~~~yaml
services:
  cpa: # 替换为现有服务名
    environment:
      MANAGEMENT_STATIC_PATH: /opt/acp-management
    volumes:
      - /opt/cpa-acp/state/management:/opt/acp-management:ro
~~~

在 CPA 配置中禁止自动替换定制页面：

~~~yaml
remote-management:
  disable-auto-update-panel: true
~~~

新增挂载或环境变量后，在 CPA Compose 目录执行 `docker compose up -d <CPA服务名>`。打开 CPA 的 `/management.html` 并刷新页面，登录后从插件入口打开 Antigravity ACP。没有保存登录会话时，刷新可能需要再次登录。

如果已有明确的静态目录或 `MANAGEMENT_STATIC_PATH`，直接更新原目录中的文件即可，不必添加第二套目录。

## 工作方式

- 父页面只通过固定白名单代理状态、模型目录、账号管理、OAuth、配额刷新、原生联网开关保存、混淆规则读取与保存操作。
- 验证 iframe 来源、窗口和插件 ID；CPA 管理密钥不会传给 iframe。
- 路由或登录状态变化时取消未完成请求。
- 同步白色、羊毛纸和暗色主题。
- 独立页保留手动连接方式，管理密钥只保存在当前页面内存中。

账号卡片的名称解析与配额标签复用 CPA 的 MIT 辅助代码，保存在 `shared/`，许可见 [shared/LICENSE](shared/LICENSE)。其构建产物已随 Go 插件的网页资源提交；只有修改这些辅助函数时才需要重新生成：

~~~bash
cd /opt/cpa-acp
bun management-center/build-account-helpers.ts
~~~

然后重新构建插件。它不引入 React 运行时，也不复用 CPA 原账号文件的凭证或配额请求接口。

## 回滚

恢复备份的 `management.html`，必要时恢复原 `MANAGEMENT_STATIC_PATH`、挂载和自动更新设置，再重新创建 CPA。恢复前保留其他新配置。面板回滚不需要删除 Google 登录状态或更改 Bridge 令牌。
