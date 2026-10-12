---
name: release-skillscli
description: 为 skillscli 准备版本发布：确认版本、更新 VERSION、在最终提交打同名 Git Tag、推送开发分支及标签并创建 PR；用户手动合并后由 CI 自动发布。询问状态时只读检查，不主动合并或发布。
compatibility: 需要 Git、已认证的 GitHub CLI（gh）及 Go；适用于 zz-zhi54/skillscli。
---

# skillscli 发布

**确认版本 → 更新 VERSION → 完成提交 → 打同名标签 → PR 检查 → 用户手动合并 → CI 自动发布。**

先核对当前仓库和 origin 为 `zz-zhi54/skillscli`，阅读 `AGENTS.md`、`VERSION`、`.github/workflows/ci.yml`；发布细节以现有工作流和[发布规范](../../../spec/spec-process-cicd-pr-checks-and-release.md)为准。

## 准备发布

1. 检查工作区、远程 `main`、已有 PR，以及 Git/GitHub CLI 权限。只处理本次发布的改动，不覆盖无关文件。
2. 使用用户明确指定的完整版本 `vX.Y.Z[-prerelease]`；未指定则询问，不自动递增。确认同名 Git Tag、Release（含草稿）未占用；已有同名标签仅当它指向本次最终提交时才可复用。禁止复用已撤回的 `v0.1.0`。
3. 更新根目录 `VERSION`，完成本次代码、文档和提交；运行适用的测试、`go vet`，编译并核对 `skillscli -v` 与 `--version` 均输出该版本。版本通过 `go:embed` 包含在可执行文件内。
4. **所有修改和同步 main 的操作完成后**，在开发分支最终提交上创建同名 Git Tag，推送本次分支及该标签。不得推送 `main`，不得强推、移动、删除或覆盖已有标签；若打标签后代码又发生提交，停止并让用户确认新版本，不移动旧标签。
5. 创建或更新目标为 `main` 的 PR，记录版本、标签和验证结果。确认 CI 的 **Version, tests and build** 检查通过后停止，由用户手动选择 **Create a merge commit** 合并。禁止 Squash/Rebase，因为标签必须指向保留在 `main` 历史中的原始最终提交。

## 合并后

只有用户实际合并 PR 才会触发 CI 发布。Skill **不自动合并，不手动发布 Release，不在合并后重新打标签**。

用户要求检查状态时，只读核对 PR 合并结果、标签对应提交、Actions 和 Release；以 CI 成功且 Release 已公开、六个附件齐全为发布成功的依据。失败时报告原因，不自行重发或覆盖已有版本。具体构建、校验和、安全权限及草稿发布由 `ci.yml` 负责，不在 Skill 中重复实现。

只问状态或只要求修改 Skill 时，不修改版本、标签、PR 或 Release。

## 回复

简短说明版本、PR、验证结果、标签和 Release 状态，以及下一步。准备阶段的下一步是**用户手动合并**。
