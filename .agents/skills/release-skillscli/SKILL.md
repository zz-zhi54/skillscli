---
name: release-skillscli
description: 为 skillscli 准备发布 PR：确认用户指定的完整版本，更新 VERSION，在开发分支最终提交上打同名标签，推送并检查 PR；用户手动保留提交合并 main 后由 CI 自动使用已有标签发布。用户说“发布 skillscli”“准备发布 PR”“检查发布状态”时使用。版本未指定则询问，不自动生成版本；不自动合并、不移动标签、不直接发布。仅询问状态时只读检查。
compatibility: 需要 Git、已认证的 GitHub CLI（gh）及 Go。适用于 zz-zhi54/skillscli 仓库。
---

# skillscli 版本与发布

**确认版本 → 更新 VERSION → 完成最终提交 → 开发分支同名标签 → PR 检查 → 用户手动 merge → CI 使用已有标签发布。**

## 事实来源与权限边界

先确认工作区和 origin 对应 `zz-zhi54/skillscli`；仓库不符则停止，不展示 URL 中的凭证。读取项目指引、[VERSION](<../../../VERSION>)、[工作流](<../../../.github/workflows/ci.yml>)、[README](<../../../README.md>) 和[发布规范](<../../../spec/spec-process-cicd-pr-checks-and-release.md>)。工具若按工作区解析路径，使用仓库根目录对应路径。

- 明确要求准备发布 PR：可以验证、提交、推送独立分支及确认版本的最终提交标签，创建/更新 PR；**不能推 main、自动合并或直接发布**。
- 只询问版本/发布状态，或只修改 Skill：仅只读查询，不视作提交/推标签许可。
- 手动合并将自动触发真实发布；PR 阶段停止等待用户，绝不运行 `gh pr merge` 或开启 auto-merge。
- 不强推，不创建额外标签，不移动、覆盖或删除已有标签/Release/草稿；保留用户无关改动，未知运行配置先取得读取许可，不提交凭证和运行锁。
- 所有发布逻辑在唯一工作流中，不新增独立 Go/Shell 版本或发布程序。

## 1. 确认完整版本与最终提交准备

1. 检查工作区、暂存区、分支与提交，确认 gh 已认证、当前凭证具备仓库内容写权限及草稿可见权限；仓库可读不等于草稿可见。获取最新远程 main，复用属于本次范围的未合并 PR，否则准备独立分支。
2. 询问完整 `vX.Y.Z[-prerelease]`，用户已经明确指定则不重复询问；不从 PR 标题获取，不按历史自动递增，不自动建议/生成下一版。版本必须三段、无数值前导零或构建元数据，预发布数值标识符也不能有前导零。`v0.1.0` 永久禁用，保留模块撤回声明。进入 v2 及以上需核对 Go 模块路径兼容要求。
3. 查询远程精确同名 Tag 和全部分页 Releases（包含草稿），核对本地标签。任何查询失败或权限不明均停止，不当成“未占用”。已有 Release/草稿则停止；已有同名标签只允许复用其指向当前最终 PR head 的情况，不覆盖。冲突时等待用户决定，不自动跳号。
4. 将确认版本写入根目录 [VERSION](<../../../VERSION>)（一行完整版本，行尾 LF）。完成所有改动、测试、文档、main 基线更新及提交，保留无关工作。业务逻辑不因发布而调整。
5. 验证格式、单元测试、race、vet、直接构建；实际运行 `-v` 和 `--version` 应只输出 VERSION 中的完整版本。版本通过 Go `go:embed` 包含在可执行文件中，不需要发布注入参数或运行时 VERSION/Git。
6. 核实 main 的必需检查 **Version, tests and build** 及分支最新要求，不能绕过或擅自改保护规则。确认仓库允许 merge commit；即便还允许 Squash/Rebase，也必须告知用户此次不能使用它们。

## 2. 打标签、推送并创建 PR

所有提交和 main 基线更新完成后，冻结准确 `HEAD_SHA`。再次核对 VERSION、精确同名 Tag/Release/草稿占用，并确认标签 push 本身不会触发当前仓库的发布流程。若已有其他标签发布流程，先告知用户并停止，不能借准备 PR 提前发布。

对未占用版本，在开发分支最终提交创建直接指向 commit 的 annotated tag；已存在且同名同 SHA 的标签仅复用。执行前核实仓库、分支、完整版本与 SHA，参数经变量传入，不拼接用户输入为 Shell 程序：

```bash
set -euo pipefail
test "$(git rev-parse HEAD)" = "${HEAD_SHA:?}"
test "$(< VERSION)" = "${VERSION:?}"
git tag -a "${VERSION:?}" "${HEAD_SHA:?}" -m "Release ${VERSION:?}"
git push origin "HEAD:refs/heads/${BRANCH:?}" "refs/tags/${VERSION:?}"
```

- 不使用 `--tags`、`--force` 或推 main。只推本次分支及该同名标签。
- 推送失败时保留原错误和本地标签；重试前重新查询远程分支/标签，防止部分推送已成功。不删除或重打标签。
- **打标签后不再提交或更新 main 基线**，否则 PR head 与标签不符。若仍需修改，停止并请用户确认新的未占用版本，不自动换号；不移动旧标签。
- 创建/更新目标 main 的 PR；标题仅描述变更，不是版本输入。正文列出 VERSION、tag、最终 head SHA、实际测试、未运行验证的平台，并强调 **Create a merge commit，禁止 Squash/Rebase**。
- 检查最新 PR head 的 `Version, tests and build`：校验 VERSION 与标签及 head，测试、race、vet、五平台构建、Linux x64 两个版本参数及校验和。发布 Job 在普通 PR 跳过正常；CI 排队、失败或取消不能称为通过。
- 停在 PR 阶段，输出 PR、确认版本、标签 SHA 和检查结果，等待用户手动合并。标签应指向 PR head，而不是后来生成的 merge commit。

## 3. 手动合并后仅跟踪自动发布

用户表示已合并或询问状态时，只读核对对应 PR 为本仓库 main 的已合并 PR、最终 head 与标签一致，且 head 是实际 merge commit 和 main 历史的祖先。不能用当前 main tip 替代，也不能把标签移动到 merge commit。

- 查询该 PR 的 `pull_request_target / closed` Actions 运行；CI 在保留标签提交的合并后重新测试构建该标签源码，不从 PR 标题取版本、不创建新标签。
- 发布 Job 仅使用已有标签，拒绝占用的 Release/草稿；上传五个不压缩原生可执行文件及 `checksums.txt`，校验和、附件名称/数量/大小和远程标签 SHA 均通过才公开。预发布后缀标记 Pre-release。
- 核实 Actions 成功、标签 SHA 仍匹配最终 head、Release 非草稿且六附件完整后，才能报告已发布。仅合并成功、标签存在或草稿存在都不代表发布成功。
- Squash/Rebase 丢失标签提交、标签移动/删除、网络/构建/上传失败时报告原错误和残留状态，等待维护者处理；不重新打标签、不删除/覆盖草稿、不直接运行发布命令。

## 回复与调用

简短报告：**确认版本、PR、实际验证、标签 SHA、Release 状态、下一步**。PR 阶段下一步为使用 Create a merge commit 手动合并；合并后为查看自动发布结果，不是再推标签。交叉编译不称作对应系统原生运行验证。

> 使用 release-skillscli，准备版本 vX.Y.Z 的发布 PR；不合并。
>
> 准备发布 skillscli（版本未指定，先询问完整版本）。
>
> PR 已合并，检查发布状态（仅只读查询，不打标签或手动发布）。
