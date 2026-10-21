<div align="center">

### Orchestrator.inc

#### Planeje, execute e supervisione agentes de programação em um só lugar.

[![GitHub stars](https://img.shields.io/github/stars/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/stargazers)
![Entre os 6 mil maiores repositórios](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![Versão no GitHub](https://img.shields.io/github/v/release/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest)
[![Downloads no GitHub](https://img.shields.io/github/downloads/OrchestratorInc/agent-orchestrator/total?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases)
[![Licença: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@useOrchestrator-555?style=flat&logo=x&logoColor=white)](https://x.com/useOrchestrator)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Orchestrator.inc (antes Agent Orchestrator, AO; antes github.com/ComposioHQ/agent-orchestrator) é um orquestrador de código aberto (Apache-2.0) para Claude Code, Codex e mais de 35 agentes de programação. Ele executa agentes em paralelo, cada um em seu próprio worktree e branch git isolados. Um agente orquestrador de projeto planeja objetivos maiores e inicia agentes workers focados. Um ciclo autônomo de pull requests devolve falhas de CI e comentários de revisão ao agente responsável pela mudança. O AO é distribuído como app desktop para macOS, Windows e Linux, com apps complementares para iPhone, iPad e Android para você acompanhar e conduzir os agentes pelo celular.

Dê a cada tarefa de programação seu próprio agente, workspace e ciclo de feedback.<br />
Planeje e delegue objetivos maiores com um orquestrador que conhece o projeto.<br />
Acompanhe cada worker, pull request, execução de CI e revisão em um Kanban ao vivo.

[**Baixar o Orchestrator.inc**](#instalação) &nbsp;&bull;&nbsp; [Documentação](https://docs.orchestrator.inc) &nbsp;&bull;&nbsp; [Versões](https://github.com/OrchestratorInc/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [Como contribuir](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · **Português (Brasil)**

<br />

<img src="../docs/assets/readme/hero.png" alt="Kanban do Orchestrator.inc com sessões de workers agrupadas por status em tempo real" width="100%" />
</div>

## Um workspace para desenvolvimento orientado por agentes

Um agente de programação pode cuidar de uma tarefa. Quando vários agentes trabalham em paralelo no mesmo projeto, surge um trabalho diferente: decidir o que importa, dividir o trabalho com clareza, dar o contexto certo a cada agente, evitar conflitos entre branches e acompanhar cada mudança até a revisão e o merge.

O Orchestrator.inc é um workspace desktop local criado para esse trabalho. Adicione um repositório e crie uma sessão de worker com o agente de programação, o modelo e a interface adequados para a tarefa. Em trabalhos com Git, o Orchestrator.inc dá ao worker sua própria branch e seu próprio worktree. Tarefa, conversa, terminal, arquivos alterados, prévia no navegador, pull request, CI e estado de revisão permanecem associados à sessão do início ao fim.

Por trás do aplicativo desktop, o daemon local do Orchestrator.inc monitora a atividade dos agentes e o estado do controle de versão. O resultado é uma visão compartilhada e atualizada do projeto, em vez de um conjunto de terminais, branches e abas do navegador desconectados.

<img src="../docs/assets/readme/tui.png" alt="Orchestrator.inc workspace with a supervised native agent interface" width="100%" />

## Workers executam tarefas focadas

Um worker é a unidade de execução do Orchestrator.inc: uma tarefa, um agente de programação e um workspace isolado. Use **New task** quando o trabalho já estiver claro. Descreva o resultado desejado, escolha um agente e um modelo, anexe os arquivos relevantes e trabalhe com o agente pelo Chat estruturado ou pela interface nativa de terminal.

Abra um worker a qualquer momento para continuar a conversa, conectar-se ao terminal, inspecionar as mudanças, usar o navegador isolado, revisar o pull request ou devolver ao mesmo agente o feedback de CI e de revisão. Assim, cada tarefa permanece compreensível por conta própria, e o trabalho paralelo não se mistura em um único contexto compartilhado.

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Orchestrator.inc" width="100%" />

## O orquestrador planeja o projeto como um todo

O orquestrador do projeto é o agente persistente de planejamento e coordenação do Orchestrator.inc. Ele atua em um nível acima das tarefas individuais: direção do produto, estratégia técnica, prioridades e sequência de trabalho em todo o repositório.

Use o orquestrador para explorar uma ideia antes da implementação, discutir abordagens de produto e técnicas, analisar trade-offs, identificar trabalho de alto impacto e transformar um objetivo ambíguo em um plano concreto. A conversa vinculada ao projeto preserva objetivos, decisões, restrições e raciocínios anteriores. O orquestrador combina esse histórico de planejamento com o contexto do repositório e o estado atual do Orchestrator.inc, incluindo workers ativos, responsáveis, pull requests, CI e revisões. Assim, o planejamento permanece conectado tanto ao projeto quanto ao trabalho que já está em andamento.

Quando um plano está pronto para sair do papel, o orquestrador pode dividi-lo em tarefas focadas, iniciar ou redirecionar workers, passar o contexto relevante a cada um, acompanhar o progresso e coordenar o trabalho seguinte. O orquestrador cuida do planejamento e da delegação; os workers cuidam da implementação, dos testes, dos commits e dos pull requests.

<img src="../docs/assets/readme/orchestrator.png" alt="Orchestrator.inc coordinating multiple workers with project context" width="100%" />

## O Kanban torna o sistema compreensível

Todo worker aparece no mesmo quadro ao vivo, independentemente de ter sido iniciado por **New task** ou delegado pelo orquestrador. O Orchestrator.inc determina a posição de cada cartão a partir dos fatos da sessão, do pull request, da CI e da revisão, transformando o Kanban em uma visão operacional do projeto:

- **Working (Em andamento):** workers implementando ativamente ou prontos para receber outra instrução
- **Needs you (Precisa de você):** sessões bloqueadas, informações ausentes, falhas de CI, mudanças solicitadas ou perda de sinal
- **In review (Em revisão):** pull requests abertos ou em rascunho aguardando verificações ou revisão
- **Ready to merge (Pronto para merge):** trabalho aprovado ou pronto para merge; sessões já integradas continuam visíveis até serem arquivadas

Cada cartão reúne tarefa, agente, branch, atividade, pull request e status. Abra-o para inspecionar a conversa ou o terminal, os arquivos alterados, o resumo do PR, as revisões e a prévia. O quadro mostra o que está avançando, o que está bloqueado e onde sua atenção terá mais impacto.

<img src="../docs/assets/readme/hero.png" alt="Orchestrator.inc Kanban showing workers grouped by live status" width="100%" />

## Um fluxo único, da ideia ao merge

1. **Comece no nível certo.** Entregue uma tarefa clara diretamente a um worker ou desenvolva um objetivo maior com o orquestrador do projeto e deixe que ele estruture o plano.
2. **Delegue trabalho focado.** Inicie workers por conta própria ou peça ao orquestrador que os crie com o contexto e a responsabilidade necessários.
3. **Construa com isolamento.** Cada worker associado ao Git recebe sua própria branch e seu próprio worktree; workers Scratch recebem diretórios sem branch gerenciados pelo Orchestrator.inc.
4. **Supervisione o estado em tempo real.** O Orchestrator.inc acompanha a atividade dos agentes, pull requests, CI, feedback de revisão e conflitos de merge, e reflete esses fatos no Kanban.
5. **Feche o ciclo de feedback.** Inspecione qualquer worker diretamente, tome decisões sobre o projeto com o orquestrador e devolva falhas acionáveis ou comentários de revisão ao agente responsável pelo trabalho.

O Orchestrator.inc funciona com os agentes de programação e o fluxo de controle de versão que você já usa. Os agentes mantêm seus pontos fortes nativos; o Orchestrator.inc fornece o contexto do projeto, a execução isolada, a coordenação e a visão operacional que fazem esses agentes trabalhar como um sistema.

## Destaques do produto

- **Um agente orquestrador que planeja e delega.** O orquestrador de cada projeto transforma um objetivo em um plano, inicia workers com o contexto certo e acompanha os pull requests deles. Abra-o pela barra lateral ou com ⌘⇧O no macOS e Ctrl+Shift+G no Windows e no Linux.
- **Um ciclo autônomo de pull requests.** Falhas de CI, comentários de revisão e conflitos de merge voltam para o worker responsável pela mudança.
- **Revisão do AO integrada.** Um agente revisor rigoroso, em qualquer harness compatível com revisão, revisa o pull request de um worker e publica um comentário inline para cada mudança exigida. Esses comentários voltam para o worker.
- **37 agentes de programação, escolhidos por projeto.** Escolha agentes diferentes para workers, orquestrador e revisores. Use o Chat estruturado ou a interface de terminal nativa do agente e alterne entre eles quando o harness permitir.
- **Um chat que acompanha seu ritmo.** O Tab aceita a próxima mensagem sugerida. No Developer mode, os side chats do inspetor oferecem uma prévia de forks nativos da conversa quando o provedor dá suporte a eles.
- **Artefatos.** Os agentes podem entregar relatórios em Markdown, SVGs, imagens e páginas HTML em vez de um pull request. Eles abrem renderizados em um visualizador editável em tela cheia, com histórico de versões e restauração, e os mais recentes aparecem nos cards do Kanban.
- **Automações.** Agende prompts recorrentes e reutilize instruções do projeto e comandos de shell como Cues.
- **Desktop e celular.** Apps desktop para macOS, Windows e Linux, além de apps para iPhone, iPad e Android que gerenciam vários computadores, iniciam sessões e enviam notificações push. No macOS, o Stay Awake mantém a máquina acessível, até com a tampa do MacBook fechada nos modelos compatíveis.
- **Contas e configuração.** Instale e atualize as CLIs dos agentes em Settings, mantenha várias contas de assinatura do Codex e experimente contas por pasta do Claude Code e do Codex como prévia no Developer mode. Escolha entre 20 temas de cores claros e escuros.

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>Pull requests e revisões por agentes</h3>
      <p>Mantenha a CI, a possibilidade de merge, o estado dos revisores e as revisões interativas por agentes junto ao worker, e depois devolva as mudanças solicitadas ao mesmo responsável.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Sessão de worker com pull request, CI e estado de revisão por agente no Orchestrator.inc" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Navegador controlável pelo agente</h3>
      <p>Visualize e inspecione o aplicativo local de um worker ao lado de sua interface. Os perfis de navegador são isolados por worker, para que tarefas paralelas de interface não compartilhem estado.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="Um worker controlando sua prévia isolada no navegador integrado" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Interfaces nativas, um único supervisor</h3>
      <p>Use o Chat estruturado ou a interface de terminal nativa do agente enquanto o Orchestrator.inc mantém o contexto, o estado do workspace e o feedback em um só lugar.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Orchestrator.inc" width="100%" />
    </td>
  </tr>
</table>

<a id="mobile-and-cross-device"></a>

## Celular e entre dispositivos

O Orchestrator.inc tem apps complementares para iPhone, iPad e Android:

- [Baixar na App Store](https://apps.apple.com/app/ao-mobile/id6792552173) (iPhone e iPad)
- [Disponível no Google Play](https://play.google.com/store/apps/details?id=aoagents.dev) (Android)

O celular funciona como controle remoto do app desktop. Seus repositórios, worktrees e agentes de programação continuam rodando no seu próprio computador, e o celular se conecta a esse computador, não a uma cópia hospedada do seu código. Para parear, abra **Settings → Connect Mobile** no app desktop, escolha **Generate** e escaneie o QR code com o app do celular. Se não for possível escanear, digite manualmente o endereço, a porta e a senha de conexão.

Como o celular chega ao seu computador:

- **Mesma rede (LAN):** funciona sem configuração em uma rede Wi-Fi confiável, em casa ou no escritório. Essa conexão é HTTP sem criptografia, protegido pela senha de conexão, então não a use em redes não confiáveis.
- **Tailscale:** quando o computador está em uma tailnet, o QR code também traz o endereço Tailscale dele, para celulares na mesma tailnet. iPhone e iPad não conseguem usá-lo, porque o iOS bloqueia conexões sem criptografia para endereços Tailscale. O app para Android permite essa conexão, mas ela ainda não foi verificada em um aparelho Android físico. Para um caminho testado fora de casa, use a opção HTTPS abaixo.
- **Túnel HTTPS gerenciado:** o Connect Mobile executa o `cloudflared` (incluído no app para macOS, instalação com um clique no Windows e no Linux) para adicionar um endereço HTTPS que funciona fora de casa em qualquer aparelho. Nessa rota, o TLS é encerrado pela Cloudflare.

O celular tenta cada endereço do QR code e usa o primeiro que responder. A API principal do desktop nunca sai de `127.0.0.1`. O Connect Mobile é um listener separado, desativado por padrão, que exige a senha de conexão e pode ser desligado ou receber uma nova senha a qualquer momento.

Pelo celular, você pode abrir qualquer projeto e worker, usar o Chat estruturado ou a interface de terminal do agente, iniciar novas sessões, responder aprovações e perguntas, trocar de agente e de modelo, ditar por voz, anexar arquivos, acompanhar pull requests, revisões e execuções de revisores e receber notificações push.

Outro app desktop do Orchestrator.inc também pode se conectar a este computador como host remoto. Execute `ao remote-host` neste computador. No outro, ative **Settings → General → Developer mode** e use **Settings → Remote hosts → Add host**. Veja [Remote access and mobile](https://docs.orchestrator.inc/configuration/remote-access) para a configuração passo a passo, o modelo de segurança e a solução de problemas.

**O Orchestrator Cloud já está disponível no app desktop** (ative **Settings → General → Developer mode** e depois **Cloud**). Escolha **Sign in to AO Cloud** e rode agentes na nuvem, em sandboxes hospedados em vez de no seu computador. Qualquer pessoa pode entrar e começar. As sessões na nuvem rodam em sandboxes remotos que podem demorar para iniciar ou perder trabalho sem commit, então faça commit e push com frequência. Veja o [guia do Cloud](https://docs.orchestrator.inc/guides/cloud) e os [planos](#plans).

<a id="supported-agents"></a>

## Agentes compatíveis

Os agentes de programação trabalham em um único fluxo supervisionado. Consulte o [catálogo de recursos](https://docs.orchestrator.inc/plugins/agents) para ver os agentes e modos atuais.

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

[Consulte os guias de configuração dos agentes →](https://docs.orchestrator.inc/plugins/agents)

**Use a interface adequada para cada momento: o Chat estruturado ou a interface nativa de terminal do agente.**

## Instalação

Baixe a versão mais recente do aplicativo desktop do Orchestrator.inc para sua plataforma. O Orchestrator.inc verifica atualizações automaticamente.

| Plataforma            | Download                                                                                                                      |
| --------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| macOS (Apple silicon) | [Download](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS (Intel)         | [Download](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows               | [Download](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [Download](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [Download](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [Download](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Abra o Orchestrator.inc e indique o repositório que você deseja que o Orchestrator.inc gerencie. O aplicativo desktop executa o daemon para você, portanto não é necessário usar a CLI. Consulte o [guia de instalação](https://docs.orchestrator.inc/installation) para configurar as CLIs dos agentes e resolver problemas.

<a id="plans"></a>

## Planos

O uso local é gratuito: agentes locais ilimitados com qualquer harness compatível, além do orquestrador, dos workers e das revisões de PR. Agentes locais não têm limites de plano. Os planos pagos adicionam agentes do AO Cloud, e um relay na nuvem para conexão entre dispositivos chega em breve.

| Plano | Preço | Inclui |
| --- | --- | --- |
| **Free** | $0 | Agentes locais ilimitados na sua máquina. Orquestrador, workers e revisões de PR. Conexão na mesma rede ou de qualquer lugar com seu próprio Tailscale ou o túnel HTTPS gerenciado. Sem agentes na nuvem. |
| **Starter** | $20/mês | Tudo do Free, mais agentes na nuvem. Os agentes na nuvem continuam trabalhando enquanto seu computador está em repouso, com pull requests, correções de CI e revisões na nuvem. 1 projeto na nuvem e 1 agente na nuvem simultâneo. Em breve: um relay na nuvem para acesso entre dispositivos. |
| **Pro** | $40/mês | Tudo do Starter. Use sua própria implantação do Coder. 5 projetos na nuvem e 15 agentes na nuvem simultâneos. |
| **Enterprise** | Sob consulta | Projetos na nuvem e agentes na nuvem simultâneos ilimitados, sem limite de horas de sessão, colaboração em agentes na nuvem, SSO e gestão de equipes, e suporte prioritário com SLA. |

Os planos são cobrados mensalmente; veja [orchestrator.inc/pricing](https://orchestrator.inc/pricing). Escolha ou troque de plano no app desktop, em **Settings → Upgrade**, depois de entrar no AO Cloud. Para o Enterprise, use **Talk to founder** na mesma página ou entre na [lista de espera de Teams & Enterprise](https://orchestrator.inc/waitlist).

## Perguntas frequentes

### O que é o Orchestrator.inc (Agent Orchestrator, AO)?

O Orchestrator.inc, antes Agent Orchestrator (AO), é um app desktop e uma CLI de código aberto. Ele executa e supervisiona Claude Code, Codex e outros agentes de programação em paralelo, cada um em seu próprio worktree git. Também acompanha os pull requests, a CI e as revisões deles.

### Qual é a licença?

Apache-2.0. Veja [LICENSE](../LICENSE).

### Quais agentes de programação são compatíveis?

37 harnesses de agentes de programação (mais de 35), entre eles Claude Code, Codex, Cursor, Gemini CLI, GitHub Copilot, opencode, Aider, Amp, Droid, Goose, Cline e Kimi. Veja a tabela de [agentes compatíveis](#supported-agents) acima. O suporte como worker, no Chat e como revisor varia de agente para agente.

### Em quais plataformas ele roda?

O app desktop roda no macOS (Apple silicon e Intel), no Windows e no Linux (AppImage, `.deb` e `.rpm`).

### Existe app para celular?

Sim. O Orchestrator.inc está na [App Store](https://apps.apple.com/app/ao-mobile/id6792552173) para iPhone e iPad e no [Google Play](https://play.google.com/store/apps/details?id=aoagents.dev) para Android. O app pareia com o seu desktop pela rede local ou por um túnel HTTPS gerenciado. Endereços Tailscale também são oferecidos, mas iPhone e iPad não conseguem usá-los e no Android eles ainda não foram verificados em um aparelho físico. Veja [Celular e entre dispositivos](#mobile-and-cross-device).

### É gratuito?

Sim. O app local é gratuito: o app desktop, a CLI e os apps para celular não custam nada, e o código-fonte é aberto sob Apache-2.0. Você usa suas próprias assinaturas ou chaves de API dos agentes de programação. Os planos pagos do AO Cloud começam em $20/mês (Starter); o Pro custa $40/mês e o Enterprise tem preço sob consulta. Veja os [planos](#plans). Equipes e empresas podem entrar na [lista de espera de Teams & Enterprise](https://orchestrator.inc/waitlist).

### É o mesmo projeto que `ComposioHQ/agent-orchestrator`?

Sim. O projeto ficava em `github.com/ComposioHQ/agent-orchestrator` e agora está em [github.com/OrchestratorInc/agent-orchestrator](https://github.com/OrchestratorInc/agent-orchestrator). Ele é licenciado sob Apache-2.0.

## Relatar um bug

[Abra um relatório de bug](https://github.com/OrchestratorInc/agent-orchestrator/issues/new?template=bug_report.yml) usando sua própria conta do GitHub. Algumas frases, escritas com suas próprias palavras, sobre o que você fez e o que deu errado são suficientes. Se tiver, inclua o que esperava, as etapas para reproduzir, a versão do Orchestrator.inc e seu sistema operacional, ou uma captura de tela. Essas informações ajudam, mas não são obrigatórias.

Um agente de programação local pode ajudar você a esclarecer o relatório e reunir evidências usando a [skill de triagem de bugs](https://github.com/OrchestratorInc/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md). Mantenha o texto da issue focado nas suas observações. Coloque logs, trechos do banco de dados e notas de investigação coletados pelo agente em anexos claramente identificados. Revise o rascunho antes de enviá-lo pela sua própria conta. Não peça a um bot para abrir issues em seu nome; a atribuição do relato é importante.

Para obter ajuda ao descrever um problema, participe do [canal de triagem de bugs no Discord](https://discord.com/channels/1476302178913357958/1491735678156013588). Consulte o [guia de contribuição](../CONTRIBUTING.md#bugs-and-features) para mais detalhes.

## Desenvolver e contribuir

Contribuições em código, documentação, triagem, exemplos e testes são bem-vindas.

```bash
git clone https://github.com/OrchestratorInc/agent-orchestrator.git
cd agent-orchestrator
```

Comece pelo [guia de desenvolvimento](../docs/development.md) para ver pré-requisitos, configuração local e comandos de teste. Leia [CONTRIBUTING.md](../CONTRIBUTING.md) antes de abrir um pull request e use as [Issues do GitHub](https://github.com/OrchestratorInc/agent-orchestrator/issues) para bugs e solicitações de recursos.

## Documentação

| Documento                                                           | Comece aqui quando precisar de                                                                       |
| ------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| [Documentação do produto](https://docs.orchestrator.inc)                   | Instalação, configuração de agentes e uso cotidiano do produto.                                      |
| [docs/architecture.md](../docs/architecture.md)                     | Modelo mental do backend, ciclo de vida, persistência, CDC, derivação de status e limites do daemon. |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | Responsabilidade dos pacotes e onde cada aspecto do backend deve ficar.                              |
| [docs/cli/README.md](../docs/cli/README.md)                         | Comportamento da CLI e mapeamento das rotas do daemon.                                               |
| [docs/development.md](../docs/development.md)                       | Pré-requisitos, etapas de build, execução de testes e solução de problemas no desenvolvimento local. |
| [docs/STATUS.md](../docs/STATUS.md)                                 | O que está disponível atualmente em `main` e o que ainda está em desenvolvimento.                    |

## Comunidade

Participe do [Discord](https://discord.com/invite/UZv7JjxbwG) para receber ajuda e conversar com outros colaboradores, siga [@useOrchestrator](https://x.com/useOrchestrator) para acompanhar as novidades ou inicie uma conversa nas [Issues do GitHub](https://github.com/OrchestratorInc/agent-orchestrator/issues).

## Telemetria do produto

O Orchestrator.inc coleta métricas de uso e confiabilidade sem o conteúdo dos projetos. O proprietário de um projeto no GitHub e o usuário conectado podem identificar uma pessoa; portanto, a telemetria não é anônima. Desativá-la também interrompe o envio desses dados. [Detalhes e controles](../docs/telemetry.md).

## Licença

O Orchestrator.inc está disponível sob a [Licença Apache 2.0](../LICENSE).
