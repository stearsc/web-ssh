# S6-C2 验收日志

## 命令与结果

| 命令 | 退出码 | 日志 |
|---|---:|---|
| `npm test`（`frontend/`） | 0 | `frontend-tests.log`（178/178 通过） |
| `npm run typecheck`（`frontend/`） | 0 | `frontend-typecheck.log` |
| `npm run build`（`frontend/`） | 0 | `frontend-build.log` |
| `go test ./...`（`backend/`） | 0 | `backend-tests.log` |
| `git diff --check` | 0 | `git-diff-check.log`（仅 LF/CRLF 提示） |
| `node s6-c2-real-e2e.mjs 34505` | 0 | `e2e-real-browser.json` |

## 截图

`screenshots/` 下共 15 张，对应关系见
`doc/测试报告/stage-5-s6-c2-test-report.md` 第 5 节。

## 真实浏览器条件

- Chrome `157.0.8103.79`，headless，两个隔离 profile / CDP 端口 `9223`、`9224`。
- 视口 `1440 × 900`。
- 前端 Vite `127.0.0.1:5173`；后端 `127.0.0.1:8080`；受控 SSH 对端 `127.0.0.1:34505`。
- T42 使用受控 4002 WebSocket 关闭和 Prepare 端点阻断进入 `retry_wait`；这是测试注入，不修改生产代码。
- SSH 对端只回显字节，不执行 shell 命令；T41 的交互以唯一 token 回显证明。

E2E 脚本与原始截图同时保存在工作区外：

`C:\Users\x\.codex\visualizations\2026\11\18\01a1eeb6-9a1d-76e3-be54-79a2111c8d07\`
