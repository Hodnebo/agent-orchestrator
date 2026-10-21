<div align="center">

### Orchestrator.inc

#### コーディングエージェントを使った作業の計画、実行、監督をひとつの場所で。

[![GitHub stars](https://img.shields.io/github/stars/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/stargazers)
![Top 6k repositories](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub release](https://img.shields.io/github/v/release/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/OrchestratorInc/agent-orchestrator/total?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@useOrchestrator-555?style=flat&logo=x&logoColor=white)](https://x.com/useOrchestrator)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Orchestrator.inc（旧称 Agent Orchestrator、AO。旧リポジトリは github.com/ComposioHQ/agent-orchestrator）は、Claude Code、Codex をはじめ 35 以上のコーディングエージェントに対応したオープンソース（Apache-2.0）のオーケストレーターです。エージェントを並列に実行し、それぞれに隔離された git worktree とブランチを割り当てます。プロジェクトのオーケストレーターエージェントが大きな目標を計画し、焦点を絞ったワーカーエージェントを起動します。自律的なプルリクエストのループにより、CI の失敗やレビューコメントは変更を担当するエージェントに戻されます。AO は macOS、Windows、Linux 向けのデスクトップアプリとして提供され、iPhone、iPad、Android 向けのコンパニオンアプリを使えば、スマートフォンからエージェントの状況を確認し、指示を出せます。

すべてのコーディングタスクに、それぞれ専用のエージェント、ワークスペース、フィードバックループを。<br />
プロジェクトを理解するオーケストレーターとともに、より大きな成果を計画し委任できます。<br />
すべてのワーカー、プルリクエスト、CI、レビューをライブ Kanban で追跡できます。

[**Orchestrator.inc をダウンロード**](#インストール) &nbsp;&bull;&nbsp; [ドキュメント](https://docs.orchestrator.inc) &nbsp;&bull;&nbsp; [リリース](https://github.com/OrchestratorInc/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [コントリビューション](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · **日本語** · [한국어](README.ko.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (Brasil)](README.pt-BR.md)

<br />

<img src="../docs/assets/readme/hero.png" alt="ワーカーセッションを現在の状態ごとに表示する Orchestrator.inc の Kanban" width="100%" />
</div>

## エージェント駆動開発のためのワークスペース

1 つのコーディングエージェントなら、1 つのタスクを処理できます。しかし、プロジェクト全体で複数のエージェントを動かすには、別の種類の仕事が生まれます。何が重要かを判断し、作業を適切に分割し、各エージェントに必要なコンテキストを渡し、ブランチの衝突を防ぎ、すべての変更をレビューからマージまで見届ける必要があります。

Orchestrator.inc は、その仕事のために作られたローカルのデスクトップワークスペースです。リポジトリを追加し、タスクに合ったコーディングエージェント、モデル、インターフェースを選んでワーカーセッションを作成します。Git を使う作業では、Orchestrator.inc がワーカー専用のブランチと worktree を用意します。タスク、会話、ターミナル、変更ファイル、ブラウザプレビュー、プルリクエスト、CI、レビューの状態は、最初から最後までそのセッションに紐づいたままです。

デスクトップアプリの背後では、Orchestrator.inc のローカルデーモンがエージェントの活動とソース管理の状態を監視します。その結果、分断されたターミナル、ブランチ、ブラウザタブの集まりではなく、プロジェクト全体をひとつの共有ライブビューで把握できます。

<img src="../docs/assets/readme/tui.png" alt="Orchestrator.inc workspace with a supervised native agent interface" width="100%" />

## ワーカーが明確なタスクを実行する

ワーカーは Orchestrator.inc における実行の単位です。1 つのタスク、1 つのコーディングエージェント、1 つの隔離されたワークスペースで構成されます。やるべきことが明確な場合は、**New task** を使います。求める成果を説明し、エージェントとモデルを選び、関連ファイルを添付して、構造化された Chat またはエージェント本来のターミナル UI で作業します。

ワーカーはいつでも開き直せます。会話を続ける、ターミナルに接続する、変更内容を確認する、隔離されたブラウザを使う、プルリクエストをレビューする、CI やレビューのフィードバックを同じエージェントへ戻す、といった操作が可能です。これにより、各タスクを単独で理解できる状態に保ち、並行作業がひとつの共有コンテキストへ崩れてしまうことを防ぎます。

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Orchestrator.inc" width="100%" />

## オーケストレーターがプロジェクト全体を計画する

プロジェクトオーケストレーターは、Orchestrator.inc の永続的な計画・調整エージェントです。個別のタスクより一段上の視点で、プロダクトの方向性、技術戦略、優先順位、リポジトリ全体にわたる作業の順序を扱います。

実装に入る前のアイデア探索、プロダクトや技術アプローチのブレインストーミング、トレードオフの検討、インパクトの大きい作業の特定、曖昧なゴールから具体的な計画への落とし込みにオーケストレーターを使えます。プロジェクト単位の会話には、目標、意思決定、制約、過去の検討内容が蓄積されます。さらに、その計画履歴をリポジトリのコンテキストや Orchestrator.inc のライブ状態と組み合わせます。進行中のワーカー、担当範囲、プルリクエスト、CI、レビューまで把握するため、計画はプロジェクトと現在進んでいる作業の両方に根ざしたものになります。

計画が実行可能になれば、オーケストレーターはそれを明確なタスクに分割し、ワーカーを起動または誘導し、それぞれに必要なコンテキストを渡し、進捗を追い、後続作業を調整できます。オーケストレーターは計画と委任を担い、ワーカーは実装、テスト、コミット、プルリクエストを担います。

<img src="../docs/assets/readme/orchestrator.png" alt="Orchestrator.inc coordinating multiple workers with project context" width="100%" />

## Kanban がシステム全体を見通せる状態に保つ

**New task** から直接始めた場合も、オーケストレーターが委任した場合も、すべてのワーカーは同じライブボードに表示されます。Orchestrator.inc はセッション、プルリクエスト、CI、レビューの事実から各カードの位置を導き出し、Kanban をプロジェクト運用のためのビューにします。

- **Working:** 実装中、または次の指示を受けられるワーカー
- **Needs you:** ブロック中、入力待ち、CI 失敗、変更要求、シグナル消失のいずれかに該当するセッション
- **In review:** チェックまたはレビューを待っているオープン中またはドラフトのプルリクエスト
- **Ready to merge:** 承認済み、またはマージ可能な作業。マージ済みのセッションもアーカイブされるまで表示されます

各カードには、タスク、エージェント、ブランチ、活動状況、プルリクエスト、ステータスがまとまっています。カードを開けば、会話またはターミナル、変更ファイル、PR の概要、レビュー、プレビューを確認できます。ボードを見るだけで、何が進んでいるか、何が止まっているか、どこに注意を向けると最も効果的かが分かります。

<img src="../docs/assets/readme/hero.png" alt="Orchestrator.inc Kanban showing workers grouped by live status" width="100%" />

## アイデアからマージまで、ひとつのワークフローで

1. **適切なレベルから始める。** 明確なタスクはワーカーに直接渡します。より大きな成果はプロジェクトオーケストレーターと検討し、計画を組み立てます。
2. **作業を明確に分けて委任する。** 自分でワーカーを起動するか、オーケストレーターに必要なコンテキストと担当範囲を持つワーカーを作成させます。
3. **隔離された環境で開発する。** Git を使うすべてのワーカーには専用のブランチと worktree が与えられます。Scratch ワーカーには Orchestrator.inc が管理するブランチなしのディレクトリが与えられます。
4. **ライブ状態を監督する。** Orchestrator.inc はエージェントの活動、プルリクエスト、CI、レビューのフィードバック、マージコンフリクトを追跡し、それらの事実を Kanban に反映します。
5. **フィードバックループを閉じる。** 任意のワーカーを直接確認し、オーケストレーターとプロジェクト全体の判断を行い、対処可能な失敗やレビューコメントを、その作業を担当するエージェントへ戻します。

Orchestrator.inc は、すでに利用しているコーディングエージェントやソース管理のワークフローと連携します。エージェントはそれぞれ本来の強みを維持し、Orchestrator.inc はそれらをひとつのシステムとして機能させるためのプロジェクトコンテキスト、隔離された実行環境、調整、運用ビューを提供します。

## プロダクトの特長

- **計画と委任を担うオーケストレーターエージェント。** 各プロジェクトのオーケストレーターが目標を計画に落とし込み、必要なコンテキストを渡してワーカーを起動し、そのプルリクエストを追跡します。サイドバーから、または macOS では ⌘⇧O、Windows と Linux では Ctrl+Shift+G で開けます。
- **自律的なプルリクエストのループ。** CI の失敗、レビューコメント、マージコンフリクトは、変更を担当するワーカーに戻されます。
- **組み込みの AO レビュー。** レビューに対応した任意のハーネス上で、厳しい視点を持つレビュアーエージェントがワーカーのプルリクエストを確認し、必要な変更ごとにインラインコメントを付けます。そのコメントはワーカーに戻されます。
- **37 のコーディングエージェントをプロジェクトごとに選択。** ワーカー、オーケストレーター、レビュアーにそれぞれ別のエージェントを選べます。構造化された Chat とエージェント本来のターミナル UI を使い分け、ハーネスが対応していれば切り替えられます。
- **流れを止めない Chat。** 次のメッセージの候補を Tab で採用できます。Developer mode では、インスペクターのサイドチャットで、プロバイダーが対応している場合にネイティブな会話フォークをプレビューできます。
- **アーティファクト。** エージェントはプルリクエストの代わりに、Markdown のレポート、SVG、画像、HTML ページを成果物として届けられます。成果物は全画面表示の編集可能なビューアーでレンダリングされ、バージョン履歴と復元に対応し、最新のものは Kanban のカードに表示されます。
- **オートメーション。** 定期的なプロンプトをスケジュールし、プロジェクトの指示やシェルコマンドを Cues として再利用できます。
- **デスクトップとモバイル。** macOS、Windows、Linux 向けのデスクトップアプリに加え、iPhone、iPad、Android 向けアプリで複数のコンピューターを管理し、セッションを開始し、プッシュ通知を受け取れます。macOS では Stay Awake がマシンを接続可能な状態に保ち、対応モデルでは MacBook のふたを閉じていても動作します。
- **アカウントとセットアップ。** エージェントの CLI を Settings からインストール・更新し、複数の Codex サブスクリプションアカウントを使い分け、Developer mode のプレビューとして Claude Code と Codex のフォルダー単位のアカウントを試せます。ライトとダークの 20 種類のカラーテーマから選べます。

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>プルリクエストとエージェントレビュー</h3>
      <p>CI、マージ可能性、レビュアーの状態、対話型のエージェントレビューをワーカーのそばに集約し、要求された変更を同じ担当エージェントへ戻せます。</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Orchestrator.inc でプルリクエスト、CI、エージェントレビューの状態を表示しているワーカーセッション" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>エージェントが操作できるブラウザ</h3>
      <p>ワーカーのインターフェースの隣でローカルアプリをプレビューして調査できます。ブラウザプロファイルはワーカーごとに隔離されるため、並行する UI タスクで状態が共有されることはありません。</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="隔離されたアプリ内ブラウザプレビューを操作するワーカー" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>ネイティブなインターフェースを一つの監督画面で</h3>
      <p>構造化された Chat またはエージェント本来のターミナル UI を使いながら、Orchestrator.inc がタスクのコンテキスト、ワークスペースの状態、フィードバックを一か所にまとめます。</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Orchestrator.inc" width="100%" />
    </td>
  </tr>
</table>

<a id="mobile-and-cross-device"></a>

## モバイルとデバイス間の連携

Orchestrator.inc には iPhone、iPad、Android 向けのコンパニオンアプリがあります。

- [App Store からダウンロード](https://apps.apple.com/app/ao-mobile/id6792552173)（iPhone と iPad）
- [Google Play で手に入れよう](https://play.google.com/store/apps/details?id=aoagents.dev)（Android）

スマートフォンはデスクトップアプリのリモコンです。リポジトリ、worktree、コーディングエージェントはあなた自身のコンピューターで動き続け、スマートフォンはホストされたコードのコピーではなく、そのコンピューターに接続します。ペアリングするには、デスクトップアプリで **Settings → Connect Mobile** を開き、**Generate** を選んで、モバイルアプリで QR コードを読み取ります。読み取れない場合は、アドレス、ポート、接続パスワードを手動で入力します。

スマートフォンからコンピューターへの接続方法:

- **同じネットワーク（LAN）:** 自宅やオフィスの信頼できる Wi-Fi なら、追加の設定なしで使えます。この接続は接続パスワードで保護された暗号化なしの HTTP なので、信頼できないネットワークでは使わないでください。
- **Tailscale:** コンピューターが tailnet に参加している場合、QR コードにはその Tailscale アドレスも含まれ、同じ tailnet のスマートフォンから使えます。iOS が Tailscale アドレスへの暗号化されていない接続をブロックするため、iPhone と iPad では使えません。Android アプリはこの接続を許可していますが、実機の Android 端末ではまだ検証されていません。外出先から確実に接続するには、次の HTTPS の方法を使ってください。
- **管理された HTTPS トンネル:** Connect Mobile は `cloudflared`（macOS アプリに同梱、Windows と Linux ではワンクリックでインストール）を実行し、外出先からどのデバイスでも使える HTTPS アドレスを追加します。この経路では Cloudflare が TLS を終端します。

スマートフォンは QR コードに含まれるすべてのアドレスを試し、最初に応答したものを使います。デスクトップのメイン API が `127.0.0.1` の外に出ることはありません。Connect Mobile は独立したリスナーで、既定ではオフになっており、接続パスワードが必要で、いつでもオフにしたりパスワードを変更したりできます。

スマートフォンからは、任意のプロジェクトとワーカーを開く、構造化された Chat やエージェントのターミナル UI を使う、新しいセッションを開始する、承認や質問に答える、エージェントやモデルを切り替える、音声で入力する、ファイルを添付する、プルリクエスト・レビュー・レビュアーの実行を追う、プッシュ通知を受け取る、といった操作ができます。

別の Orchestrator.inc デスクトップアプリから、このコンピューターにリモートホストとして接続することもできます。このコンピューターで `ao remote-host` を実行し、もう一方のコンピューターで **Settings → General → Developer mode** をオンにしてから **Settings → Remote hosts → Add host** を使います。手順、セキュリティモデル、トラブルシューティングは [Remote access and mobile](https://docs.orchestrator.inc/configuration/remote-access) を参照してください。

**Orchestrator Cloud がデスクトップアプリで利用できるようになりました**（**Settings → General → Developer mode** をオンにしてから **Cloud** をオンにします）。**Sign in to AO Cloud** を選ぶと、コンピューターではなくホストされたサンドボックス上で、クラウドでエージェントを実行できます。誰でもサインインしてすぐに始められます。クラウドセッションはリモートのサンドボックスで実行されるため、起動に時間がかかったり、コミットしていない作業が失われたりすることがあります。こまめにコミットとプッシュをしてください。[Cloud ガイド](https://docs.orchestrator.inc/guides/cloud) と [プラン](#plans) を参照してください。

<a id="supported-agents"></a>

## 対応エージェント

コーディングエージェントをひとつのワークフローで管理できます。現在対応するエージェントとモードは[機能一覧](https://docs.orchestrator.inc/plugins/agents)をご覧ください。

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

[エージェントのセットアップガイドを見る →](https://docs.orchestrator.inc/plugins/agents)

**状況に合ったインターフェースを選べます。構造化された Chat と、エージェント本来のターミナル UI に対応しています。**

## インストール

お使いのプラットフォーム向けの最新 Orchestrator.inc デスクトップアプリをダウンロードしてください。Orchestrator.inc は更新を自動的に確認します。

| プラットフォーム       | ダウンロード                                                                                                                      |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| macOS（Apple silicon） | [ダウンロード](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS（Intel）         | [ダウンロード](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows                | [ダウンロード](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux（AppImage）      | [ダウンロード](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux（Debian/Ubuntu） | [ダウンロード](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux（Fedora/RHEL）   | [ダウンロード](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Orchestrator.inc を開き、Orchestrator.inc に管理させたいリポジトリを指定します。デスクトップアプリがデーモンを実行するため、CLI は不要です。エージェント CLI のセットアップとトラブルシューティングは[インストールガイド](https://docs.orchestrator.inc/installation)を参照してください。

<a id="plans"></a>

## プラン

ローカルでの利用は無料です。対応するどのハーネスでもローカルエージェントを無制限に使え、オーケストレーター、ワーカー、PR レビューも含まれます。ローカルエージェントにはプランによる制限がありません。有料プランでは AO Cloud のエージェントが使えるようになり、デバイス間をつなぐクラウドリレーも近日提供予定です。

| プラン | 価格 | 含まれるもの |
| --- | --- | --- |
| **Free** | $0 | 手元のマシンでローカルエージェントを無制限に。オーケストレーター、ワーカー、PR レビュー。同じネットワーク内で、または自分の Tailscale か管理された HTTPS トンネルでどこからでも接続。クラウドエージェントなし。 |
| **Starter** | $20/月 | Free のすべてに加えてクラウドエージェント。コンピューターがスリープしている間もクラウドエージェントが作業を続け、プルリクエスト、CI の修正、レビューをクラウドで処理します。クラウドプロジェクト 1 つ、同時実行のクラウドエージェント 1 つ。近日提供: デバイス間アクセスのためのクラウドリレー。 |
| **Pro** | $40/月 | Starter のすべて。独自の Coder デプロイメントを利用可能。クラウドプロジェクト 5 つ、同時実行のクラウドエージェント 15。 |
| **Enterprise** | 個別見積もり | クラウドプロジェクトと同時実行のクラウドエージェントが無制限、セッション時間の上限なし、クラウドエージェントでの共同作業、SSO とチーム管理、SLA 付きの優先サポート。 |

プランは月額で請求されます。詳しくは [orchestrator.inc/pricing](https://orchestrator.inc/pricing) を参照してください。プランの選択や変更は、AO Cloud にサインインした後、デスクトップアプリの **Settings → Upgrade** で行います。Enterprise については同じページの **Talk to founder** を使うか、[Teams & Enterprise のウェイティングリスト](https://orchestrator.inc/waitlist) に登録してください。

## よくある質問

### Orchestrator.inc（Agent Orchestrator、AO）とは何ですか？

Orchestrator.inc（旧称 Agent Orchestrator、AO）は、オープンソースのデスクトップアプリと CLI です。Claude Code、Codex などのコーディングエージェントを、それぞれ専用の git worktree で並列に実行・監督し、プルリクエスト、CI、レビューも追跡します。

### ライセンスは何ですか？

Apache-2.0 です。[LICENSE](../LICENSE) を参照してください。

### どのコーディングエージェントに対応していますか？

37 のコーディングエージェントハーネス（35 以上）に対応しています。Claude Code、Codex、Cursor、Gemini CLI、GitHub Copilot、opencode、Aider、Amp、Droid、Goose、Cline、Kimi などです。上の [対応エージェント](#supported-agents) の表を参照してください。ワーカー、Chat、レビュアーとしての対応状況はエージェントごとに異なります。

### どのプラットフォームで動きますか？

デスクトップアプリは macOS（Apple silicon と Intel）、Windows、Linux（AppImage、`.deb`、`.rpm`）で動作します。

### モバイルアプリはありますか？

はい。iPhone と iPad 向けは [App Store](https://apps.apple.com/app/ao-mobile/id6792552173)、Android 向けは [Google Play](https://play.google.com/store/apps/details?id=aoagents.dev) で公開しています。アプリはローカルネットワーク、または管理された HTTPS トンネル経由でデスクトップとペアリングします。Tailscale アドレスも提供されますが、iPhone と iPad では使えず、Android ではまだ実機で検証されていません。[モバイルとデバイス間の連携](#mobile-and-cross-device) を参照してください。

### 無料ですか？

はい。ローカルアプリは無料です。デスクトップアプリ、CLI、モバイルアプリはすべて無料で、ソースコードは Apache-2.0 で公開されています。コーディングエージェントには、ご自身のサブスクリプションや API キーを使います。有料の AO Cloud プランは $20/月（Starter）からで、Pro は $40/月、Enterprise は個別見積もりです。[プラン](#plans) を参照してください。チームや企業の方は [Teams & Enterprise のウェイティングリスト](https://orchestrator.inc/waitlist) に登録できます。

### `ComposioHQ/agent-orchestrator` と同じプロジェクトですか？

はい。このプロジェクトは以前 `github.com/ComposioHQ/agent-orchestrator` にあり、現在は [github.com/OrchestratorInc/agent-orchestrator](https://github.com/OrchestratorInc/agent-orchestrator) にあります。ライセンスは Apache-2.0 です。

## バグを報告する

[バグ報告を開く](https://github.com/OrchestratorInc/agent-orchestrator/issues/new?template=bug_report.yml)ときは、自分の GitHub アカウントを使ってください。何をして何が起きたのかを、自分の言葉で数文書けば十分です。期待した結果、再現手順、Orchestrator.inc のバージョンと OS、スクリーンショットがあれば追加してください。これらは役立ちますが、必須ではありません。

ローカルのコーディングエージェントには、[bug-triage skill](https://github.com/OrchestratorInc/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md) を使って報告内容の整理や証拠の収集を手伝ってもらえます。Issue 本文は自分の観察に絞り、エージェントが集めたログ、データベースの抜粋、調査メモは内容が分かる添付ファイルに分けてください。自分のアカウントから送信する前に下書きを確認してください。ボット に代理で Issue を登録させないでください。報告者を正しく紐づける必要があります。

問題の説明について助けが必要な場合は、[Discord の bug-triaging チャンネル](https://discord.com/channels/1476302178913357958/1491735678156013588)に参加してください。詳しくは[コントリビューションガイド](../CONTRIBUTING.md#bugs-and-features)を参照してください。

## 開発とコントリビューション

コード、ドキュメント、トリアージ、サンプル、テストなど、さまざまな形でのコントリビューションを歓迎します。

```bash
git clone https://github.com/OrchestratorInc/agent-orchestrator.git
cd agent-orchestrator
```

前提条件、ローカル環境のセットアップ、テストコマンドについては、まず[開発ガイド](../docs/development.md)を確認してください。プルリクエストを作成する前に [CONTRIBUTING.md](../CONTRIBUTING.md) を読み、バグや機能リクエストには [GitHub Issues](https://github.com/OrchestratorInc/agent-orchestrator/issues) を利用してください。

## ドキュメント

| ドキュメント                                                        | 次の情報が必要な場合                                                                      |
| ------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| [プロダクトドキュメント](https://docs.orchestrator.inc)                    | インストール、エージェントのセットアップ、日常的な製品の使い方。                          |
| [docs/architecture.md](../docs/architecture.md)                     | バックエンドのメンタルモデル、ライフサイクル、永続化、CDC、ステータス導出、デーモン境界。 |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | パッケージの所有範囲と、バックエンドの各関心事を配置する場所。                            |
| [docs/cli/README.md](../docs/cli/README.md)                         | CLI の動作とデーモンルートの対応関係。                                                    |
| [docs/development.md](../docs/development.md)                       | ローカル開発の前提条件、ビルド手順、テスト実行、トラブルシューティング。                  |
| [docs/STATUS.md](../docs/STATUS.md)                                 | `main` で現在提供されているものと、まだ進行中のもの。                                     |

## コミュニティ

サポートやコントリビューター同士の議論には [Discord](https://discord.com/invite/UZv7JjxbwG) に参加し、最新情報は [@useOrchestrator](https://x.com/useOrchestrator) をフォローしてください。[GitHub Issues](https://github.com/OrchestratorInc/agent-orchestrator/issues) から会話を始めることもできます。

## 製品テレメトリ

Orchestrator.inc はプロジェクトの内容を除いた利用状況と信頼性の指標を収集します。ただし、GitHub 上のプロジェクト所有者やログイン中のユーザー名から個人を特定できるため、匿名ではありません。テレメトリをオフにすると、これらの情報の送信も止まります。[詳細と設定](../docs/telemetry.md)。

## ライセンス

Orchestrator.inc は [Apache License 2.0](../LICENSE) のもとで提供されています。
