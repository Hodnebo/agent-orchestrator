<div align="center">

### Orchestrator.inc

#### 集中规划、运行和监督编程智能体。

[![GitHub stars](https://img.shields.io/github/stars/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/stargazers)
![GitHub 前 6000 名仓库](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub 版本](https://img.shields.io/github/v/release/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest)
[![GitHub 下载量](https://img.shields.io/github/downloads/OrchestratorInc/agent-orchestrator/total?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases)
[![许可证：Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@useOrchestrator-555?style=flat&logo=x&logoColor=white)](https://x.com/useOrchestrator)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Orchestrator.inc（原名 Agent Orchestrator，简称 AO；原仓库为 github.com/ComposioHQ/agent-orchestrator）是一款开源（Apache-2.0）编排器，支持 Claude Code、Codex 等 35 个以上编程智能体。它并行运行智能体，每个智能体都在各自隔离的 git worktree 和分支中工作。项目编排器智能体负责规划更大的目标，并启动专注于具体任务的 worker 智能体。自主的拉取请求循环会把 CI 失败和评审意见交回负责该变更的智能体。AO 提供适用于 macOS、Windows 和 Linux 的桌面应用，以及适用于 iPhone、iPad 和 Android 的配套应用，让你在手机上也能跟进和指挥智能体。

让每个编程任务都有自己的智能体、工作区和反馈闭环。<br />
通过了解项目上下文的编排器规划和委派更大的目标。<br />
在实时看板中跟踪每个 worker、拉取请求、CI 运行和评审。

[**下载 Orchestrator.inc**](#安装) &nbsp;&bull;&nbsp; [文档](https://docs.orchestrator.inc) &nbsp;&bull;&nbsp; [版本](https://github.com/OrchestratorInc/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [参与贡献](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · **简体中文** · [日本語](README.ja.md) · [한국어](README.ko.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (Brasil)](README.pt-BR.md)

<br />

<img src="../docs/assets/readme/hero.png" alt="Orchestrator.inc 看板，按实时状态分组显示 worker 会话" width="100%" />
</div>

## 面向智能体驱动开发的工作区

一个编程智能体可以完成一项任务。当多个智能体同时参与一个项目时，你需要解决的是另一类工作：判断什么最重要，清晰地拆分任务，为每个智能体提供正确的上下文，避免分支冲突，并跟踪每项变更直至评审和合并。

Orchestrator.inc 是为此打造的本地桌面工作区。添加一个仓库，然后根据任务选择合适的编程智能体、模型和交互界面来创建 worker 会话。对于使用 Git 的工作，Orchestrator.inc 会为每个 worker 分配独立的分支和 worktree。从开始到结束，任务、对话、终端、变更文件、浏览器预览、拉取请求、CI 和评审状态始终与该会话关联。

在桌面应用背后，Orchestrator.inc 的本地守护进程会监视智能体活动和源代码管理状态。你看到的是项目统一且实时的全局视图，而不是彼此割裂的终端、分支和浏览器标签页。

<img src="../docs/assets/readme/tui.png" alt="Orchestrator.inc workspace with a supervised native agent interface" width="100%" />

## Worker 执行专注、明确的任务

Worker 是 Orchestrator.inc 的执行单元：一项任务、一个编程智能体和一个隔离工作区。当工作已经明确时，使用 **New task**。描述期望结果，选择智能体和模型，附上相关文件，然后通过结构化 Chat 或智能体原生的终端界面与其协作。

你可以随时打开 worker，继续对话、连接终端、检查变更、使用隔离浏览器、查看拉取请求，或把 CI 和评审反馈交还给同一个智能体。这样，每项任务都能独立理解，并行工作也不会挤进同一个共享上下文。

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Orchestrator.inc" width="100%" />

## 项目编排器负责全局规划

项目编排器是 Orchestrator.inc 中持久存在的规划与协调智能体。它工作在单项任务之上，关注整个仓库的产品方向、技术策略、优先级和工作顺序。

你可以在实施之前用编排器探索想法、讨论产品和技术方案、分析取舍、识别高影响力工作，并把模糊的目标转化为具体计划。项目级对话会保留目标、决策、约束和之前的推理。编排器将这些规划历史与仓库上下文及 Orchestrator.inc 的实时状态结合起来，其中包括活跃 worker、任务归属、拉取请求、CI 和评审。这让规划既立足于项目全局，也始终反映正在进行的工作。

当计划可以付诸执行时，编排器能够将其拆成专注的任务，启动或重定向 worker，把相关上下文交给每个 worker，跟踪进展并协调后续工作。编排器负责规划和委派；worker 负责实现、测试、提交和拉取请求。

<img src="../docs/assets/readme/orchestrator.png" alt="Orchestrator.inc coordinating multiple workers with project context" width="100%" />

## 看板让整个系统一目了然

无论 worker 是通过 **New task** 创建，还是由编排器委派，它都会出现在同一个实时看板上。Orchestrator.inc 根据会话、拉取请求、CI 和评审的实际状态决定每张卡片的位置，让看板成为项目的运行视图：

- **Working（处理中）：** 正在实施工作或可以接收下一条指令的 worker
- **Needs you（需要你）：** 被阻塞、缺少输入、CI 失败、被要求修改或信号丢失的会话
- **In review（评审中）：** 正在等待检查或评审的已打开拉取请求和草稿拉取请求
- **Ready to merge（可以合并）：** 已获批准或可合并的工作；已合并的会话会保持可见，直到被归档

每张卡片都把任务、智能体、分支、活动、拉取请求和状态放在一起。打开卡片即可查看对话或终端、变更文件、PR 摘要、评审和预览。看板会告诉你哪些工作正在推进，哪些工作被阻塞，以及你的注意力放在哪里能产生最大影响。

<img src="../docs/assets/readme/hero.png" alt="Orchestrator.inc Kanban showing workers grouped by live status" width="100%" />

## 从想法到合并，一套完整工作流

1. **从正确的层级开始。** 将明确的任务直接交给 worker，或者与项目编排器一起梳理更大的目标并由它形成计划。
2. **委派专注的工作。** 你可以自己启动 worker，也可以让编排器带着所需的上下文和明确的责任归属来创建 worker。
3. **在隔离环境中构建。** 每个使用 Git 的 worker 都有自己的分支和 worktree；Scratch worker 则使用由 Orchestrator.inc 管理的无分支目录。
4. **监督实时状态。** Orchestrator.inc 跟踪智能体活动、拉取请求、CI、评审反馈和合并冲突，并在看板中反映这些事实。
5. **闭合反馈循环。** 直接检查任意 worker，与编排器一起做出项目层面的决策，再把可操作的失败信息或评审意见交还给负责该工作的智能体。

Orchestrator.inc 与你现有的编程智能体和源代码管理流程配合使用。智能体保留各自的原生优势；Orchestrator.inc 提供项目上下文、隔离执行、协调能力和运行视图，让这些智能体能够作为一个系统协同工作。

## 产品亮点

- **会规划、会委派的编排器智能体。** 每个项目的编排器会把目标拆成计划，带着聚焦的上下文启动 worker，并跟进它们的拉取请求。可从侧边栏打开，也可在 macOS 上按 ⌘⇧O、在 Windows 和 Linux 上按 Ctrl+Shift+G 打开。
- **自主的拉取请求循环。** CI 失败、评审意见和合并冲突都会交回负责该变更的 worker。
- **内置 AO 评审。** 在任意支持评审的 harness 上，一个严格的评审智能体会审查 worker 的拉取请求，并为每项必须修改的内容留下行内评论。这些评论会交回给 worker。
- **37 个编程智能体，按项目选择。** 可以为 worker、编排器和评审者分别选择不同的智能体。使用结构化 Chat 或智能体原生的终端界面，并在 harness 支持时在两者之间切换。
- **跟得上节奏的 Chat。** 按 Tab 即可采用建议的下一条消息。在 Developer mode 下，检查器中的侧边对话可以预览原生会话分叉（需提供方支持）。
- **产出物。** 智能体可以交付 Markdown 报告、SVG、图片和 HTML 页面，而不必提交拉取请求。它们会在可编辑的全屏查看器中渲染打开，支持版本历史和恢复，最新的产出物会显示在看板卡片上。
- **自动化。** 定时运行重复的提示词，并把项目指令和 shell 命令保存为 Cues 重复使用。
- **桌面端与移动端。** 提供 macOS、Windows 和 Linux 桌面应用，以及 iPhone、iPad 和 Android 应用，可管理多台电脑、启动会话并接收推送通知。在 macOS 上，Stay Awake 可让电脑保持可连接状态，支持的机型即使合上 MacBook 也能保持。
- **账户与设置。** 在 Settings 中安装和更新智能体 CLI，使用多个 Codex 订阅账户，并以 Developer mode 预览功能试用 Claude Code 和 Codex 的按文件夹账户。可从 20 种浅色和深色配色主题中选择。

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>拉取请求和智能体评审</h3>
      <p>将 CI、可合并状态、评审者状态和交互式智能体评审放在 worker 旁边，再把要求的修改交还给同一个负责人。</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Orchestrator.inc 中包含拉取请求、CI 和智能体评审状态的 worker 会话" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>可由智能体控制的浏览器</h3>
      <p>在 worker 界面旁预览和检查本地应用。每个 worker 都有隔离的浏览器配置，因此并行的界面任务不会共享状态。</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="Worker 控制其隔离的应用内浏览器预览" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>原生界面，由一个监督层统一管理</h3>
      <p>使用结构化 Chat 或智能体原生终端界面，同时让 Orchestrator.inc 将任务上下文、工作区状态和反馈集中在一个地方。</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Orchestrator.inc" width="100%" />
    </td>
  </tr>
</table>

<a id="mobile-and-cross-device"></a>

## 移动端与跨设备

Orchestrator.inc 提供适用于 iPhone、iPad 和 Android 的配套应用：

- [在 App Store 下载](https://apps.apple.com/app/ao-mobile/id6792552173)（iPhone 和 iPad）
- [在 Google Play 下载](https://play.google.com/store/apps/details?id=aoagents.dev)（Android）

手机是桌面应用的遥控器。你的仓库、worktree 和编程智能体仍在你自己的电脑上运行，手机连接的是这台电脑，而不是托管的代码副本。要配对，请在桌面应用中打开 **Settings → Connect Mobile**，选择 **Generate**，然后用移动应用扫描二维码。如果无法扫描，可以手动输入地址、端口和连接密码。

手机连接电脑的方式：

- **同一网络（LAN）：** 在可信的家庭或办公室 Wi-Fi 中无需额外设置即可使用。该连接是受连接密码保护的未加密 HTTP，请勿在不可信的网络中使用。
- **Tailscale：** 当电脑加入 tailnet 时，二维码中还会包含它的 Tailscale 地址，供同一 tailnet 中的手机使用。iPhone 和 iPad 无法使用该地址，因为 iOS 会阻止对 Tailscale 地址的未加密连接。Android 应用允许这种连接，但尚未在真实的 Android 设备上验证。如需在外出时使用经过测试的方式，请使用下面的 HTTPS 选项。
- **托管 HTTPS 隧道：** Connect Mobile 会运行 `cloudflared`（macOS 应用已内置，Windows 和 Linux 可一键安装），添加一个 HTTPS 地址，让任何设备在外也能连接。在这条线路上，TLS 由 Cloudflare 终止。

手机会尝试二维码中的每个地址，并使用第一个响应的地址。桌面端的主 API 始终只在 `127.0.0.1` 上。Connect Mobile 是一个独立的监听器，默认关闭，需要连接密码，并且可以随时关闭或更换新密码。

在手机上，你可以打开任何项目和 worker，使用结构化 Chat 或智能体的终端界面，启动新会话，回复审批和提问，切换智能体和模型，语音输入，添加附件，跟进拉取请求、评审和评审运行，并接收推送通知。

其他 Orchestrator.inc 桌面应用也可以把这台电脑作为远程主机连接。在这台电脑上运行 `ao remote-host`。在另一台电脑上开启 **Settings → General → Developer mode**，然后使用 **Settings → Remote hosts → Add host**。分步设置、安全模型和故障排除请参阅 [Remote access and mobile](https://docs.orchestrator.inc/configuration/remote-access)。

**Orchestrator Cloud 现已在桌面应用中提供**（先开启 **Settings → General → Developer mode**，再开启 **Cloud**）。选择 **Sign in to AO Cloud**，即可在托管沙箱而不是你的电脑上运行云端智能体。任何人都可以登录并立即开始。云端会话运行在远程沙箱中，可能启动较慢，也可能丢失未提交的工作，因此请经常提交并推送。请参阅 [Cloud 指南](https://docs.orchestrator.inc/guides/cloud) 和[套餐](#plans)。

<a id="supported-agents"></a>

## 支持的智能体

在同一个受监督的工作流中使用编程智能体。当前支持的智能体和模式请查看[功能目录](https://docs.orchestrator.inc/plugins/agents)。

<table>
  <tr valign="middle">
    <td width="33%" valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/claude-code.svg" alt="Claude Code" width="24" height="24" align="middle" /> &nbsp; <b>Claude Code</b></td>
    <td width="33%" valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/codex.svg" alt="Codex" width="24" height="24" align="middle" /> &nbsp; <b>Codex</b></td>
    <td width="33%" valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/cursor-dark.svg" /><img src="../frontend/src/renderer/assets/agents/cursor-light.svg" alt="Cursor" width="24" height="24" align="middle" /></picture> &nbsp; <b>Cursor</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/opencode-dark.svg" /><img src="../frontend/src/renderer/assets/agents/opencode-light.svg" alt="opencode" width="24" height="24" align="middle" /></picture> &nbsp; <b>opencode</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/aider.svg" alt="Aider" width="24" height="24" align="middle" /> &nbsp; <b>Aider</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/copilot-dark.svg" /><img src="../frontend/src/renderer/assets/agents/copilot-light.svg" alt="GitHub Copilot" width="24" height="24" align="middle" /></picture> &nbsp; <b>GitHub Copilot</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/grok-dark.svg" /><img src="../frontend/src/renderer/assets/agents/grok-light.svg" alt="Grok" width="24" height="24" align="middle" /></picture> &nbsp; <b>Grok</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/kimi-dark.svg" /><img src="../frontend/src/renderer/assets/agents/kimi-light.svg" alt="Kimi" width="24" height="24" align="middle" /></picture> &nbsp; <b>Kimi</b></td>
    <td valign="middle" nowrap><img src="../docs/assets/readme/agents/pi-coding-agent.svg" alt="Pi" width="24" height="24" align="middle" /> &nbsp; <b>Pi</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/amp-dark.svg" /><img src="../frontend/src/renderer/assets/agents/amp-light.svg" alt="Amp" width="24" height="24" align="middle" /></picture> &nbsp; <b>Amp</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/auggie-dark.svg" /><img src="../frontend/src/renderer/assets/agents/auggie-light.svg" alt="Auggie" width="24" height="24" align="middle" /></picture> &nbsp; <b>Auggie</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/droid-dark.svg" /><img src="../frontend/src/renderer/assets/agents/droid-light.svg" alt="Droid" width="24" height="24" align="middle" /></picture> &nbsp; <b>Droid</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/crush.svg" alt="Crush" width="24" height="24" align="middle" /> &nbsp; <b>Crush</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/cline-dark.svg" /><img src="../frontend/src/renderer/assets/agents/cline-light.svg" alt="Cline" width="24" height="24" align="middle" /></picture> &nbsp; <b>Cline</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/goose-dark.svg" /><img src="../frontend/src/renderer/assets/agents/goose-light.svg" alt="Goose" width="24" height="24" align="middle" /></picture> &nbsp; <b>Goose</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/qwen-dark.svg" /><img src="../frontend/src/renderer/assets/agents/qwen-light.svg" alt="Qwen" width="24" height="24" align="middle" /></picture> &nbsp; <b>Qwen</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/continue-dark.svg" /><img src="../frontend/src/renderer/assets/agents/continue-light.svg" alt="Continue" width="24" height="24" align="middle" /></picture> &nbsp; <b>Continue</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/devin-dark.svg" /><img src="../frontend/src/renderer/assets/agents/devin-light.svg" alt="Devin" width="24" height="24" align="middle" /></picture> &nbsp; <b>Devin</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/kiro.svg" alt="Kiro" width="24" height="24" align="middle" /> &nbsp; <b>Kiro</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/kilocode-dark.svg" /><img src="../frontend/src/renderer/assets/agents/kilocode-light.svg" alt="Kilo Code" width="24" height="24" align="middle" /></picture> &nbsp; <b>Kilo Code</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/vibe.svg" alt="Vibe" width="24" height="24" align="middle" /> &nbsp; <b>Vibe</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/muse.svg" alt="Muse" width="24" height="24" align="middle" /> &nbsp; <b>Muse</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/agy.svg" alt="Agy" width="24" height="24" align="middle" /> &nbsp; <b>Agy</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../docs/assets/readme/agents/autohand-stacked-dark.png" /><img src="../docs/assets/readme/agents/autohand-stacked-light.png" alt="Autohand" width="24" height="24" align="middle" /></picture> <b>Autohand</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/kimchi.svg" alt="Kimchi" width="24" height="24" align="middle" /> &nbsp; <b>Kimchi</b></td>
    <td valign="middle" nowrap><img src="../docs/assets/readme/agents/prime-agent.svg" alt="Prime Agent" width="24" height="24" align="middle" /> &nbsp; <b>Prime Agent</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/omp.svg" alt="OMP" width="24" height="24" align="middle" /> &nbsp; <b>OMP</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/fx-dark.svg" /><img src="../frontend/src/renderer/assets/agents/fx-light.svg" alt="fx" width="24" height="24" align="middle" /></picture> &nbsp; <b>fx</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/unreal-agent-dark.png" /><img src="../frontend/src/renderer/assets/agents/unreal-agent-light.png" alt="Unreal Agent" width="24" height="24" align="middle" /></picture> &nbsp; <b>Unreal Agent</b></td>
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/mimo-code.svg" alt="MiMo Code" width="24" height="24" align="middle" /> &nbsp; <b>MiMo Code</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/gemini.svg" alt="Gemini CLI" width="24" height="24" align="middle" /> &nbsp; <b>Gemini CLI</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/deepseek-harness-dark.svg" /><img src="../frontend/src/renderer/assets/agents/deepseek-harness-light.svg" alt="DeepSeek" width="24" height="24" align="middle" /></picture> &nbsp; <b>DeepSeek</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/opencode-dark.svg" /><img src="../frontend/src/renderer/assets/agents/opencode-light.svg" alt="OpenCode 2" width="24" height="24" align="middle" /></picture> &nbsp; <b>OpenCode 2</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/codewhale-dark.svg" /><img src="../frontend/src/renderer/assets/agents/codewhale-light.svg" alt="Codewhale" width="24" height="24" align="middle" /></picture> &nbsp; <b>Codewhale</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/command-code-dark.svg" /><img src="../frontend/src/renderer/assets/agents/command-code-light.svg" alt="Command Code" width="24" height="24" align="middle" /></picture> &nbsp; <b>Command Code</b></td>
    <td valign="middle" nowrap><picture><source media="(prefers-color-scheme: dark)" srcset="../frontend/src/renderer/assets/agents/openhands-dark.svg" /><img src="../frontend/src/renderer/assets/agents/openhands-light.svg" alt="OpenHands" width="24" height="24" align="middle" /></picture> &nbsp; <b>OpenHands</b></td>
  </tr>
  <tr valign="middle">
    <td valign="middle" nowrap><img src="../frontend/src/renderer/assets/agents/memcode.png" alt="memcode" width="24" height="24" align="middle" /> &nbsp; <b>memcode</b></td>
    <td valign="middle" nowrap></td>
    <td valign="middle" nowrap></td>
  </tr>
</table>

[浏览智能体设置指南 →](https://docs.orchestrator.inc/plugins/agents)

**根据当下的需要选择交互方式：结构化 Chat 或智能体原生的终端界面。**

## 安装

下载适用于你所在平台的最新 Orchestrator.inc 桌面应用。Orchestrator.inc 会自动检查更新。

| 平台                   | 下载                                                                                                                      |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| macOS（Apple 芯片）    | [下载](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS（Intel）         | [下载](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows                | [下载](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux（AppImage）      | [下载](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux（Debian/Ubuntu） | [下载](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux（Fedora/RHEL）   | [下载](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

打开 Orchestrator.inc，并选择你希望 Orchestrator.inc 管理的仓库。桌面应用会为你运行守护进程，因此无需使用 CLI。有关智能体 CLI 的设置和故障排除，请参阅[安装指南](https://docs.orchestrator.inc/installation)。

<a id="plans"></a>

## 套餐

本地使用免费：可使用任何受支持的 harness 无限运行本地智能体，并包含编排器、worker 和 PR 评审。本地智能体没有套餐限制。付费套餐增加 AO Cloud 智能体，用于跨设备连接的云端中继也即将推出。

| 套餐 | 价格 | 包含 |
| --- | --- | --- |
| **Free** | $0 | 在本机无限运行本地智能体。编排器、worker 和 PR 评审。可在同一网络内连接，也可通过你自己的 Tailscale 或托管 HTTPS 隧道随时随地连接。无云端智能体。 |
| **Starter** | $20/月 | 包含 Free 的全部内容，另加云端智能体。电脑休眠时云端智能体仍会继续工作，并在云端处理拉取请求、CI 修复和评审。1 个云端项目，1 个同时运行的云端智能体。即将推出：用于跨设备访问的云端中继。 |
| **Pro** | $40/月 | 包含 Starter 的全部内容。可使用自己的 Coder 部署。5 个云端项目，15 个同时运行的云端智能体。 |
| **Enterprise** | 定制价格 | 云端项目和同时运行的云端智能体不限数量，无会话时长上限，可协作使用云端智能体，支持 SSO 和团队管理，并提供带 SLA 的优先支持。 |

套餐按月计费，详见 [orchestrator.inc/pricing](https://orchestrator.inc/pricing)。登录 AO Cloud 后，可在桌面应用的 **Settings → Upgrade** 中选择或更改套餐。如需 Enterprise，请在同一页面使用 **Talk to founder**，或加入 [Teams & Enterprise 候补名单](https://orchestrator.inc/waitlist)。

## 常见问题

### Orchestrator.inc（Agent Orchestrator，AO）是什么？

Orchestrator.inc（原名 Agent Orchestrator，简称 AO）是一款开源桌面应用和 CLI。它在各自独立的 git worktree 中并行运行和监督 Claude Code、Codex 及其他编程智能体，并跟踪它们的拉取请求、CI 和评审。

### 使用什么许可证？

Apache-2.0。参见 [LICENSE](../LICENSE)。

### 支持哪些编程智能体？

支持 37 个编程智能体 harness（35 个以上），包括 Claude Code、Codex、Cursor、Gemini CLI、GitHub Copilot、opencode、Aider、Amp、Droid、Goose、Cline 和 Kimi。参见上方的[支持的智能体](#supported-agents)表格。各智能体对 worker、Chat 和评审者角色的支持程度不同。

### 支持哪些平台？

桌面应用支持 macOS（Apple silicon 和 Intel）、Windows 和 Linux（AppImage、`.deb` 和 `.rpm`）。

### 有移动应用吗？

有。Orchestrator.inc 已在 [App Store](https://apps.apple.com/app/ao-mobile/id6792552173)（iPhone 和 iPad）和 [Google Play](https://play.google.com/store/apps/details?id=aoagents.dev)（Android）上架。应用通过本地网络或托管 HTTPS 隧道与桌面端配对。也会提供 Tailscale 地址，但 iPhone 和 iPad 无法使用，Android 也尚未在真实设备上验证。参见[移动端与跨设备](#mobile-and-cross-device)。

### 免费吗？

免费。本地应用不收费：桌面应用、CLI 和移动应用都免费，源代码以 Apache-2.0 开源。你使用自己的编程智能体订阅或 API 密钥。付费的 AO Cloud 套餐起价为 $20/月（Starter）；Pro 为 $40/月，Enterprise 为定制价格。参见[套餐](#plans)。团队和企业可以加入 [Teams & Enterprise 候补名单](https://orchestrator.inc/waitlist)。

### 这和 `ComposioHQ/agent-orchestrator` 是同一个项目吗？

是的。该项目原先位于 `github.com/ComposioHQ/agent-orchestrator`，现在位于 [github.com/OrchestratorInc/agent-orchestrator](https://github.com/OrchestratorInc/agent-orchestrator)。它采用 Apache-2.0 许可证。

## 报告 bug

[提交 bug 报告](https://github.com/OrchestratorInc/agent-orchestrator/issues/new?template=bug_report.yml)时，请使用你自己的 GitHub 账号。用自己的话写几句话，说明你做了什么以及发生了什么问题即可。如果有的话，请补充预期结果、复现步骤、Orchestrator.inc 版本和操作系统，或附上截图。这些信息很有帮助，但不是必需的。

本地编程智能体可以使用 [bug-triage skill](https://github.com/OrchestratorInc/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md)帮助你整理报告和收集证据。Issue 正文只保留你的观察；智能体收集的日志、数据库摘录和调查笔记应放在清楚标注的附件中。请在使用自己的账号提交前检查草稿。不要要求 机器人 代你提交 issue，因为需要保留报告者归属。

如果需要帮助描述问题，可以加入 [Discord bug-triaging 频道](https://discord.com/channels/1476302178913357958/1491735678156013588)。更多信息请参阅[贡献指南](../CONTRIBUTING.md#bugs-and-features)。

## 开发与贡献

欢迎贡献代码、文档、bug 分诊、示例和测试。

```bash
git clone https://github.com/OrchestratorInc/agent-orchestrator.git
cd agent-orchestrator
```

请先阅读[开发指南](../docs/development.md)，了解先决条件、本地设置和测试命令。提交拉取请求前请阅读 [CONTRIBUTING.md](../CONTRIBUTING.md)，并通过 [GitHub Issues](https://github.com/OrchestratorInc/agent-orchestrator/issues) 报告 bug 和提出功能请求。

## 文档

| 文档                                                                | 需要以下内容时从这里开始                                      |
| ------------------------------------------------------------------- | ------------------------------------------------------------- |
| [产品文档](https://docs.orchestrator.inc)                                  | 安装、智能体设置和日常产品使用。                              |
| [docs/architecture.md](../docs/architecture.md)                     | 后端心智模型、生命周期、持久化、CDC、状态推导和守护进程边界。 |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | 包职责以及各项后端关注点应归属的位置。                        |
| [docs/cli/README.md](../docs/cli/README.md)                         | CLI 行为和守护进程路由映射。                                  |
| [docs/development.md](../docs/development.md)                       | 本地开发的先决条件、构建步骤、测试运行方法和故障排除。        |
| [docs/STATUS.md](../docs/STATUS.md)                                 | `main` 当前已提供的功能和尚在开发中的内容。                   |

## 社区

加入 [Discord](https://discord.com/invite/UZv7JjxbwG) 获取帮助并参与贡献者讨论，关注 [@useOrchestrator](https://x.com/useOrchestrator) 了解最新动态，或在 [GitHub Issues](https://github.com/OrchestratorInc/agent-orchestrator/issues) 中发起讨论。

## 产品遥测

Orchestrator.inc 收集不包含项目内容的使用和可靠性指标。但项目的 GitHub 所有者及已登录的 GitHub 用户名可能识别个人，因此这些遥测数据并非匿名。关闭遥测也会停止发送这些信息。[查看详情和设置](../docs/telemetry.md)。

## 许可证

Orchestrator.inc 基于 [Apache License 2.0](../LICENSE) 提供。
