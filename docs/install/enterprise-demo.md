# 企业AI 智能运维平台演示版部署

## 当前演示边界

这是独立定制分支上的纯前端沙盘，不接入真实资产、不调用模型、不提供真实登录认证。所有示例状态只保存在访问者浏览器，换浏览器、域名或端口后不共享。默认入口为 http://192.168.1.11:18860，填写账号（至少 3 位）和密码（至少 6 位）后点击“登录”。仅校验本地输入形式，不验证服务端身份，不使用真实系统凭据。

示例包含 24 项资源、12 条告警、8 篇知识、3 个流程和 6 条执行记录。账户菜单支持语言、深浅主题、恢复示例和退出；恢复需确认。状态损坏或存储额度不足会显示警告，编辑前请保留知识文本副本。

## 构建与打包

要求 Node 20.11+、npm、Git；构建与测试沿用 web/package-lock.json。

```bash
cd web
npm ci
cd ..
make build-enterprise-demo
make test-enterprise-demo
```

浏览器测试默认连接 http://127.0.0.1:5180；先在另一终端执行 `npm run dev --prefix web -- --host 127.0.0.1 --port 5180`，或使用 E2E_BASE_URL 指向指定部署。

首次部署可先提交源码，执行 `make bootstrap-enterprise-demo` 与 `make deploy-enterprise-demo` 生成用于截图的部署；引导包暂不包含手册，完成手册后必须重新正式打包并发布。

先按 `scripts/build-enterprise-manual.py` 生成并逐页渲染检查手册，保存为 `output/enterprise-manual/enterprise-manual.docx`。正式打包前必须提交当前修改，以保证源码归档与构建版本一致：

```bash
make package-enterprise-demo
```

打包目录为 `output/enterprise-release`，包含 html、Dockerfile、nginx.conf、compose.yaml、VERSION 和非敏感镜像版本配置 .env。源码归档仅使用 `git archive HEAD`，不打包未跟踪文件、Git 历史或本地凭据。包内包含 LICENSE、第三方声明、对应源码和手册下载。

## 测试服务器发布

服务器使用 Ubuntu 24.04、Docker Compose，独立目录 `/opt/enterprise-ai-demo`，独立项目 `enterprise-ai-demo`。仅发布 18860 端口，不修改既有 nginx、80 端口或其他容器。发布前检查 `ss -lnt sport = :18860`，确认没有不属于本项目的服务。服务器默认 Docker 地址池已耗尽；本项目使用检查无重叠的独立网段 `10.253.57.0/28`。迁移到其他环境前重新检查路由和 Docker IPAM，不修改宿主机全局地址池。服务器现有业务较多，静态容器限制 1 CPU、512 MiB 内存与 100 个进程。

执行 `make deploy-enterprise-demo` 上传完整发布目录并部署到 `releases/<VERSION>`。脚本会检查端口、配置和健康状态，更新 current，并保留 previous 链接；不要指向未经授权的其他环境。底层在发布目录运行以下命令：

```bash
docker compose config --quiet
docker compose build
docker compose up -d --wait
```

验证 `/healthz`、`/readyz`、`/metrics`，首页与详情深链接、手册和源码下载，再更新 `current` 符号链接。运行容器 user 为 101:101，只读根文件系统、临时目录 tmpfs，无需数据库卷。健康检查只验证静态服务器；不代表后端健康或真实 AI 可用。

## 回滚与停用

保留原发布目录和镜像。更新前保存 current 链接目标；回滚时进入上一版本目录，执行 `docker compose up -d --wait --no-build`，验收后恢复 current。首次部署没有上一个版本，停用时仅在本项目目录执行 `docker compose down`。不要使用全局 prune 或停止其他应用。

回滚不会删除浏览器数据。v1 只读取 v1 存储命名空间；未来升级需要单独说明迁移策略。平台说明提供当前修改版源码，不用上游仓库链接替代对应源码。

## 操作手册复现

生成脚本使用 python-docx 与 Pillow，渲染使用 documents 技能的 render_docx.py、LibreOffice、Poppler 和 pdf2image。在隔离工具容器内运行，不向测试服务器宿主机安装 Office。截图由真实部署上的 Playwright 浏览器取得，存放 `output/enterprise-manual/screenshots`，覆盖登录、总览、告警、资产、指标、AI、流程、执行与知识。

运行 `scripts/build-enterprise-manual.py --help` 查看参数。Word 手册、渲染页和截图是 output 下的发布产物，不提交到 Git；源代码保留生成脚本。交付前必须检查所有渲染页，正文和截图不能裁切或缺字。

## 第二版监控与手册

监控中心增加九个菜单，共 56 个分类对象、60 条日志与 8 条阈值规则。规则保存在 enterprise-ai.rules.v1，与原业务状态相互独立，恢复业务数据不会清除规则；阈值不下发到真实系统。原有浏览器业务数据继续兼容，内置知识的旧版展示措辞在读取时作精确替换，不替换用户其他内容。

公文风格手册需要中文仿宋、小标宋、黑体、楷体和宋体。无这些字体时，可在隔离文档容器安装 https://mirrors.ctan.org/fonts/fandol.zip 内的开源字体，使用 fontconfig 将上述字体分别映射到 FandolFang、FandolSong、FandolHei、FandolKai、FandolSong。不向测试服务器安装字体或 Office。生成时使用最终部署截图与版本号，逐页检查正文、目录页码、截图比例和页脚。


## 详细操作手册复现与核验

手册内容由 `scripts/enterprise_manual_content.py` 维护，生成器为 `scripts/build-enterprise-manual.py`。每章提供功能字段表、四步操作、列表和详情图示、结果核验与异常说明；附录包含一次完整检查路径，以及阈值保存、告警关闭、流程完成和知识填写的放大图。保留 A4 公文风格与 16pt 仿宋正文，字段表使用 13pt，核验与注意事项使用 14pt，截图不拉伸。

1. 执行 `node scripts/capture-enterprise-manual.mjs`。脚本使用独立浏览器上下文，在授权测试站点通过实际操作采集 47 个状态，生成截图清单 `output/enterprise-manual/screenshots/manifest.json`。本地变更只影响该上下文，不替换其他访问者的数据。截图时会修改阈值、关闭一条告警、执行一个本地流程和新增一篇知识；恢复弹窗只截图并取消。
2. 将生成器、内容文件和索引脚本复制到隔离文档运行时，生成 DOCX。`--version` 填写平台说明中核实的应用版本；仅修改手册时可保留应用版本，不把资料修订误写为系统功能升级。
3. 用 `render_docx.py --emit_pdf` 渲染，并用 `pdftotext -layout` 导出文本。执行 `index-enterprise-manual.py <文本路径> <目录页码.json>`，从真实正文标题生成目录页码。
4. 再次生成手册时传入 `--contents-pages <目录页码.json>`，重新渲染。再次建立索引并与前一次比较，确保目录更新没有改变正文分页。
5. 检查所有最终页的表格、截图、正文、图号与页脚。确认无空白页、单段溢出、图片裁切或缺字，并检查 DOCX 文本和元数据不含已移除的产品展示措辞。截图必须属于实际可用的功能，不能为手册增加不存在的操作。
6. 源码提交后重新打包并发布隔离容器，核对在线 Word 下载与本地产物逐字节相同。仅资料修订时前端构建可保持不变；发布镜像标识仍使用资料提交版本，应用内版本继续对应原有前端构建。保留前一发布目录，回滚时恢复上一镜像即可恢复上一手册。
