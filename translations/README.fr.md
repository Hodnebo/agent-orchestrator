<div align="center">

### Orchestrator.inc

#### Planifiez, exécutez et supervisez vos agents de développement depuis un seul endroit.

[![GitHub stars](https://img.shields.io/github/stars/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/stargazers)
![Top 6k repositories](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub release](https://img.shields.io/github/v/release/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/OrchestratorInc/agent-orchestrator/total?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@useOrchestrator-555?style=flat&logo=x&logoColor=white)](https://x.com/useOrchestrator)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Orchestrator.inc (anciennement Agent Orchestrator, AO ; anciennement github.com/ComposioHQ/agent-orchestrator) est un orchestrateur open source (Apache-2.0) pour Claude Code, Codex et plus de 35 agents de développement. Il exécute les agents en parallèle, chacun dans son propre worktree git et sa propre branche, isolés. Un agent orchestrateur de projet planifie les objectifs plus larges et lance des agents workers ciblés. Une boucle autonome de pull requests renvoie les échecs de CI et les commentaires de revue à l'agent responsable de la modification. AO est disponible en application de bureau pour macOS, Windows et Linux, avec des applications compagnons pour iPhone, iPad et Android pour suivre et piloter vos agents depuis votre téléphone.

Donnez à chaque tâche de développement son propre agent, son propre espace de travail et sa propre boucle de feedback.<br />
Planifiez et déléguez les objectifs plus vastes avec un orchestrateur qui connaît votre projet.<br />
Suivez chaque worker, pull request, exécution CI et revue sur un Kanban actualisé en direct.

[**Télécharger Orchestrator.inc**](#installation) &nbsp;&bull;&nbsp; [Documentation](https://docs.orchestrator.inc) &nbsp;&bull;&nbsp; [Versions](https://github.com/OrchestratorInc/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [Contribuer](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Español](README.es.md) · **Français** · [Deutsch](README.de.md) · [Português (Brasil)](README.pt-BR.md)

<br />

<img src="../docs/assets/readme/hero.png" alt="Kanban d'Orchestrator.inc montrant les sessions de workers regroupées selon leur état en direct" width="100%" />
</div>

## Un espace de travail pour le développement piloté par des agents

Un agent de développement peut prendre en charge une tâche. Dès que plusieurs agents travaillent en parallèle sur un projet, le défi change : il faut décider de ce qui compte, découper le travail proprement, donner à chaque agent le bon contexte, éviter les collisions entre branches et suivre chaque changement jusqu'à la revue et à la fusion.

Orchestrator.inc est un espace de travail local conçu pour ce rôle. Ajoutez un dépôt, puis créez une session de worker avec l'agent, le modèle et l'interface adaptés à la tâche. Pour les travaux suivis avec Git, Orchestrator.inc attribue au worker sa propre branche et son propre worktree. La tâche, la conversation, le terminal, les fichiers modifiés, l'aperçu du navigateur, la pull request, la CI et l'état des revues restent attachés à cette session du début à la fin.

Derrière l'application de bureau, le daemon local d'Orchestrator.inc observe l'activité des agents et l'état du contrôle de version. Vous obtenez ainsi une vue partagée et actualisée du projet, au lieu d'une collection de terminaux, de branches et d'onglets de navigateur déconnectés.

<img src="../docs/assets/readme/tui.png" alt="Orchestrator.inc workspace with a supervised native agent interface" width="100%" />

## Les workers exécutent des tâches bien délimitées

Un worker est l'unité d'exécution d'Orchestrator.inc : une tâche, un agent de développement et un espace de travail isolé. Utilisez **New task** lorsque le travail est déjà clair. Décrivez le résultat attendu, choisissez un agent et un modèle, joignez les fichiers utiles, puis travaillez avec l'agent dans le Chat structuré ou dans son interface de terminal native.

Vous pouvez ouvrir un worker à tout moment pour poursuivre la conversation, vous connecter à son terminal, examiner ses changements, utiliser son navigateur isolé, relire sa pull request ou renvoyer les retours de CI et de revue au même agent. Chaque tâche reste ainsi compréhensible indépendamment, sans que le travail parallèle ne se mélange dans un contexte partagé.

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Orchestrator.inc" width="100%" />

## L'orchestrateur planifie à l'échelle du projet

L'orchestrateur du projet est l'agent permanent de planification et de coordination d'Orchestrator.inc. Il travaille à un niveau supérieur à celui des tâches individuelles : la direction produit, la stratégie technique, les priorités et l'ordre des travaux dans l'ensemble du dépôt.

Utilisez l'orchestrateur pour explorer une idée avant son implémentation, élaborer des approches produit et techniques, raisonner sur les compromis, repérer les tâches à fort impact et transformer un objectif ambigu en plan concret. Sa conversation, liée au projet, conserve les objectifs, les décisions, les contraintes et les raisonnements précédents. Il associe cet historique de planification au contexte du dépôt et à l'état actuel d'Orchestrator.inc, notamment les workers actifs, les responsabilités, les pull requests, la CI et les revues. La planification reste ainsi ancrée dans le projet et dans le travail déjà en cours.

Lorsqu'un plan devient exploitable, l'orchestrateur peut le découper en tâches ciblées, lancer ou réorienter des workers, transmettre à chacun le contexte pertinent, suivre leur progression et coordonner la suite. L'orchestrateur prend en charge la planification et la délégation. Les workers prennent en charge l'implémentation, les tests, les commits et les pull requests.

<img src="../docs/assets/readme/orchestrator.png" alt="Orchestrator.inc coordinating multiple workers with project context" width="100%" />

## Le Kanban rend le système lisible

Chaque worker apparaît sur le même tableau en direct, qu'il ait été lancé depuis **New task** ou délégué par l'orchestrateur. Orchestrator.inc détermine la position de chaque carte à partir des faits concernant la session, la pull request, la CI et la revue. Le Kanban devient ainsi une vue opérationnelle du projet :

- **Working:** workers en cours d'implémentation ou prêts à recevoir une nouvelle instruction
- **Needs you:** sessions bloquées, informations manquantes, CI en échec, changements demandés ou signaux perdus
- **In review:** pull requests ouvertes ou en brouillon qui attendent des vérifications ou une revue
- **Ready to merge:** travaux approuvés ou prêts à fusionner ; les sessions fusionnées restent visibles jusqu'à leur archivage

Chaque carte rassemble la tâche, l'agent, la branche, l'activité, la pull request et l'état. Ouvrez-la pour examiner la conversation ou le terminal, les fichiers modifiés, le résumé de la PR, les revues et l'aperçu. Le tableau indique ce qui avance, ce qui est bloqué et où votre attention aura le plus d'impact.

<img src="../docs/assets/readme/hero.png" alt="Orchestrator.inc Kanban showing workers grouped by live status" width="100%" />

## Un seul workflow, de l'idée à la fusion

1. **Commencez au bon niveau.** Confiez une tâche claire directement à un worker, ou développez un objectif plus large avec l'orchestrateur du projet et laissez-le structurer le plan.
2. **Déléguez un travail ciblé.** Lancez vous-même les workers, ou demandez à l'orchestrateur de les créer avec le contexte et les responsabilités nécessaires.
3. **Travaillez en isolation.** Chaque worker suivi avec Git reçoit sa propre branche et son propre worktree. Les workers Scratch reçoivent des répertoires sans branche gérés par Orchestrator.inc.
4. **Supervisez l'état en direct.** Orchestrator.inc suit l'activité des agents, les pull requests, la CI, les retours de revue et les conflits de fusion, puis reflète ces faits dans le Kanban.
5. **Fermez la boucle de feedback.** Examinez directement chaque worker, prenez les décisions à l'échelle du projet avec l'orchestrateur et renvoyez les échecs exploitables ou les commentaires de revue à l'agent responsable.

Orchestrator.inc fonctionne avec les agents de développement et le workflow de contrôle de version que vous utilisez déjà. Les agents conservent leurs points forts. Orchestrator.inc apporte le contexte du projet, l'exécution isolée, la coordination et la vue opérationnelle qui leur permettent de fonctionner comme un système.

## Points forts du produit

- **Un agent orchestrateur qui planifie et délègue.** L'orchestrateur de chaque projet transforme un objectif en plan, lance des workers avec un contexte ciblé et suit leurs pull requests. Ouvrez-le depuis la barre latérale ou avec ⌘⇧O sur macOS, Ctrl+Shift+G sur Windows et Linux.
- **Une boucle autonome de pull requests.** Les échecs de CI, les commentaires de revue et les conflits de fusion reviennent au worker responsable de la modification.
- **Revue AO intégrée.** Un agent relecteur exigeant, sur n'importe quel harness compatible avec la revue, examine la pull request d'un worker et publie un commentaire en ligne pour chaque modification exigée. Ces commentaires reviennent au worker.
- **37 agents de développement, au choix pour chaque projet.** Choisissez des agents différents pour les workers, l'orchestrateur et les relecteurs. Utilisez le Chat structuré ou l'interface terminal native de l'agent, et passez de l'un à l'autre lorsque le harness le permet.
- **Un chat qui suit le rythme.** Tab accepte le prochain message suggéré. En Developer mode, les chats latéraux de l'inspecteur proposent en aperçu des embranchements de conversation natifs lorsque le fournisseur les prend en charge.
- **Artefacts.** Les agents peuvent livrer des rapports Markdown, des SVG, des images et des pages HTML au lieu d'une pull request. Ils s'ouvrent rendus dans une visionneuse modifiable en plein écran, avec historique des versions et restauration, et les plus récents apparaissent sur les cartes du Kanban.
- **Automatisations.** Planifiez des prompts récurrents et réutilisez les instructions de projet et les commandes shell sous forme de Cues.
- **Bureau et mobile.** Des applications de bureau pour macOS, Windows et Linux, ainsi que des applications iPhone, iPad et Android qui gèrent plusieurs ordinateurs, lancent des sessions et envoient des notifications push. Sur macOS, Stay Awake garde la machine joignable, même MacBook fermé sur les modèles compatibles.
- **Comptes et configuration.** Installez et mettez à jour les CLI des agents depuis Settings, gérez plusieurs comptes d'abonnement Codex et essayez les comptes Claude Code et Codex par dossier en aperçu dans Developer mode. Choisissez parmi 20 thèmes de couleur clairs et sombres.

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>Pull requests et revues par des agents</h3>
      <p>Gardez la CI, la fusionnabilité, l'état des relecteurs et les revues interactives par des agents aux côtés du worker, puis renvoyez les changements demandés au même responsable.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Session de worker avec pull request, CI et état de revue par des agents dans Orchestrator.inc" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Navigateur contrôlable par les agents</h3>
      <p>Prévisualisez et inspectez l'application locale d'un worker à côté de son interface. Les profils de navigateur sont isolés par worker afin que les tâches d'interface parallèles ne partagent pas leur état.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="Un worker contrôle son aperçu isolé dans le navigateur intégré" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Interfaces natives, un seul superviseur</h3>
      <p>Utilisez le Chat structuré ou l'interface de terminal native de l'agent, tandis qu'Orchestrator.inc conserve le contexte, l'état de l'espace de travail et les retours au même endroit.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Orchestrator.inc" width="100%" />
    </td>
  </tr>
</table>

<a id="mobile-and-cross-device"></a>

## Mobile et multi-appareils

Orchestrator.inc propose des applications compagnons pour iPhone, iPad et Android :

- [Télécharger dans l'App Store](https://apps.apple.com/app/ao-mobile/id6792552173) (iPhone et iPad)
- [Disponible sur Google Play](https://play.google.com/store/apps/details?id=aoagents.dev) (Android)

Le téléphone sert de télécommande pour l'application de bureau. Vos dépôts, worktrees et agents de développement continuent de tourner sur votre propre ordinateur, et le téléphone se connecte à cet ordinateur plutôt qu'à une copie hébergée de votre code. Pour l'associer, ouvrez **Settings → Connect Mobile** dans l'application de bureau, choisissez **Generate** et scannez le code QR avec l'application mobile. Si le scan n'est pas possible, saisissez manuellement l'adresse, le port et le mot de passe de connexion.

Comment le téléphone joint votre ordinateur :

- **Même réseau (LAN) :** fonctionne sans configuration sur un Wi-Fi de confiance, à la maison ou au bureau. Cette connexion passe en HTTP non chiffré, protégé par le mot de passe de connexion : ne l'utilisez donc pas sur un réseau non fiable.
- **Tailscale :** lorsque l'ordinateur fait partie d'un tailnet, le code QR contient aussi son adresse Tailscale, pour les téléphones du même tailnet. L'iPhone et l'iPad ne peuvent pas l'utiliser, car iOS bloque les connexions non chiffrées vers les adresses Tailscale. L'application Android autorise cette connexion, mais elle n'a pas encore été vérifiée sur un appareil Android physique. Pour une solution testée hors de chez vous, utilisez l'option HTTPS ci-dessous.
- **Tunnel HTTPS géré :** Connect Mobile exécute `cloudflared` (inclus dans l'application macOS, installable en un clic sur Windows et Linux) pour ajouter une adresse HTTPS utilisable hors de chez vous depuis n'importe quel appareil. Sur ce trajet, c'est Cloudflare qui termine le TLS.

Le téléphone essaie chaque adresse du code QR et utilise la première qui répond. L'API principale du bureau ne quitte jamais `127.0.0.1`. Connect Mobile est un listener distinct, désactivé par défaut, qui exige le mot de passe de connexion et peut être coupé ou recevoir un nouveau mot de passe à tout moment.

Depuis le téléphone, vous pouvez ouvrir n'importe quel projet et worker, utiliser le Chat structuré ou l'interface terminal de l'agent, démarrer de nouvelles sessions, répondre aux validations et aux questions, changer d'agent et de modèle, dicter à la voix, joindre des fichiers, suivre les pull requests, les revues et les exécutions de relecteurs, et recevoir des notifications push.

Une autre application de bureau Orchestrator.inc peut aussi se connecter à cet ordinateur en tant qu'hôte distant. Exécutez `ao remote-host` sur cet ordinateur. Sur l'autre, activez **Settings → General → Developer mode** et utilisez **Settings → Remote hosts → Add host**. Consultez [Remote access and mobile](https://docs.orchestrator.inc/configuration/remote-access) pour la configuration pas à pas, le modèle de sécurité et le dépannage.

**Orchestrator Cloud est disponible dès maintenant dans l'application de bureau** (activez **Settings → General → Developer mode**, puis **Cloud**). Choisissez **Sign in to AO Cloud** et exécutez des agents dans le cloud, dans des sandboxes hébergées plutôt que sur votre ordinateur. Tout le monde peut se connecter et commencer. Les sessions cloud s'exécutent dans des sandboxes distantes qui peuvent être lentes à démarrer ou perdre le travail non commité : faites des commits et des push régulièrement. Consultez le [guide Cloud](https://docs.orchestrator.inc/guides/cloud) et les [offres](#plans).

<a id="supported-agents"></a>

## Agents pris en charge

Les agents de développement fonctionnent dans un même workflow supervisé. Consultez le [catalogue des capacités](https://docs.orchestrator.inc/plugins/agents) pour les agents et modes pris en charge.

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

[Consulter les guides de configuration des agents →](https://docs.orchestrator.inc/plugins/agents)

**Utilisez l'interface adaptée au moment : le Chat structuré ou l'interface de terminal native de l'agent.**

## Installation

Téléchargez la dernière application de bureau Orchestrator.inc pour votre plateforme. Orchestrator.inc vérifie automatiquement la disponibilité des mises à jour.

| Plateforme            | Téléchargement                                                                                                                   |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| macOS (Apple silicon) | [Télécharger](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS (Intel)         | [Télécharger](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows               | [Télécharger](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [Télécharger](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [Télécharger](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [Télécharger](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Ouvrez Orchestrator.inc et sélectionnez le dépôt qu'Orchestrator.inc doit gérer. L'application de bureau exécute le daemon pour vous, aucune CLI n'est donc nécessaire. Consultez le [guide d'installation](https://docs.orchestrator.inc/installation) pour configurer les CLI des agents et résoudre les problèmes.

<a id="plans"></a>

## Offres

L'usage local est gratuit : agents locaux illimités avec n'importe quel harness pris en charge, ainsi que l'orchestrateur, les workers et les revues de PR. Les agents locaux ne sont soumis à aucune limite d'offre. Les offres payantes ajoutent des agents AO Cloud, et un relais cloud pour la connexion entre appareils arrive bientôt.

| Offre | Prix | Inclus |
| --- | --- | --- |
| **Free** | 0 $ | Agents locaux illimités sur votre machine. Orchestrateur, workers et revues de PR. Connexion sur le même réseau, ou de n'importe où avec votre propre Tailscale ou le tunnel HTTPS géré. Aucun agent cloud. |
| **Starter** | 20 $/mois | Tout le contenu de Free, plus des agents cloud. Les agents cloud continuent de travailler pendant que votre ordinateur est en veille, avec pull requests, corrections de CI et revues dans le cloud. 1 projet cloud et 1 agent cloud simultané. Bientôt : un relais cloud pour l'accès entre appareils. |
| **Pro** | 40 $/mois | Tout le contenu de Starter. Utilisez votre propre déploiement Coder. 5 projets cloud et 15 agents cloud simultanés. |
| **Enterprise** | Sur mesure | Projets cloud et agents cloud simultanés illimités, aucun plafond d'heures de session, collaboration sur les agents cloud, SSO et gestion d'équipe, et support prioritaire avec SLA. |

Les offres sont facturées au mois ; consultez [orchestrator.inc/pricing](https://orchestrator.inc/pricing). Choisissez ou changez d'offre dans l'application de bureau, sous **Settings → Upgrade**, après vous être connecté à AO Cloud. Pour Enterprise, utilisez **Talk to founder** sur la même page ou rejoignez la [liste d'attente Teams & Enterprise](https://orchestrator.inc/waitlist).

## Questions fréquentes

### Qu'est-ce qu'Orchestrator.inc (Agent Orchestrator, AO) ?

Orchestrator.inc, anciennement Agent Orchestrator (AO), est une application de bureau et une CLI open source. Elle exécute et supervise Claude Code, Codex et d'autres agents de développement en parallèle, chacun dans son propre worktree git. Elle suit aussi leurs pull requests, leur CI et leurs revues.

### Sous quelle licence est-il distribué ?

Apache-2.0. Voir [LICENSE](../LICENSE).

### Quels agents de développement sont pris en charge ?

37 harnesses d'agents de développement (plus de 35), dont Claude Code, Codex, Cursor, Gemini CLI, GitHub Copilot, opencode, Aider, Amp, Droid, Goose, Cline et Kimi. Voir le tableau des [agents pris en charge](#supported-agents) ci-dessus. La prise en charge en tant que worker, dans le Chat et comme relecteur varie selon l'agent.

### Sur quelles plateformes fonctionne-t-il ?

L'application de bureau fonctionne sur macOS (Apple silicon et Intel), Windows et Linux (AppImage, `.deb` et `.rpm`).

### Existe-t-il une application mobile ?

Oui. Orchestrator.inc est disponible sur l'[App Store](https://apps.apple.com/app/ao-mobile/id6792552173) pour iPhone et iPad et sur [Google Play](https://play.google.com/store/apps/details?id=aoagents.dev) pour Android. L'application s'associe à votre bureau via votre réseau local ou un tunnel HTTPS géré. Des adresses Tailscale sont aussi proposées, mais l'iPhone et l'iPad ne peuvent pas les utiliser et Android n'a pas encore été vérifié sur un appareil physique. Voir [Mobile et multi-appareils](#mobile-and-cross-device).

### Est-ce gratuit ?

Oui. L'application locale est gratuite : l'application de bureau, la CLI et les applications mobiles ne coûtent rien, et le code source est ouvert sous Apache-2.0. Vous utilisez vos propres abonnements ou clés d'API pour les agents de développement. Les offres payantes AO Cloud commencent à 20 $/mois (Starter) ; Pro coûte 40 $/mois et Enterprise est sur mesure. Voir les [offres](#plans). Les équipes et les entreprises peuvent rejoindre la [liste d'attente Teams & Enterprise](https://orchestrator.inc/waitlist).

### Est-ce le même projet que `ComposioHQ/agent-orchestrator` ?

Oui. Le projet se trouvait auparavant sur `github.com/ComposioHQ/agent-orchestrator` et se trouve désormais sur [github.com/OrchestratorInc/agent-orchestrator](https://github.com/OrchestratorInc/agent-orchestrator). Il est distribué sous licence Apache-2.0.

## Signaler un bug

[Ouvrez un rapport de bug](https://github.com/OrchestratorInc/agent-orchestrator/issues/new?template=bug_report.yml) depuis votre propre compte GitHub. Quelques phrases, avec vos propres mots, sur ce que vous avez fait et ce qui s'est mal passé suffisent. Ajoutez si possible ce que vous attendiez, les étapes pour reproduire le problème, votre version d'Orchestrator.inc et votre système d'exploitation, ou une capture d'écran. Ces informations sont utiles, mais facultatives.

Un agent de développement local peut vous aider à préciser le rapport et à recueillir des éléments avec la [skill de triage des bugs](https://github.com/OrchestratorInc/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md). Limitez le contenu de l'issue à vos observations. Placez les journaux, extraits de base de données et notes d'enquête recueillis par l'agent dans des pièces jointes clairement identifiées. Relisez le brouillon avant de l'envoyer depuis votre propre compte. Ne demandez pas à un bot de créer une issue à votre place : l'auteur du signalement doit rester identifié.

Pour obtenir de l'aide afin de décrire un problème, rejoignez le [canal de triage des bugs sur Discord](https://discord.com/channels/1476302178913357958/1491735678156013588). Consultez le [guide de contribution](../CONTRIBUTING.md#bugs-and-features) pour plus de détails.

## Développer et contribuer

Les contributions au code, à la documentation, au triage, aux exemples et aux tests sont les bienvenues.

```bash
git clone https://github.com/OrchestratorInc/agent-orchestrator.git
cd agent-orchestrator
```

Commencez par le [guide de développement](../docs/development.md) pour connaître les prérequis, la configuration locale et les commandes de test. Lisez [CONTRIBUTING.md](../CONTRIBUTING.md) avant d'ouvrir une pull request et utilisez les [GitHub Issues](https://github.com/OrchestratorInc/agent-orchestrator/issues) pour les bugs et les demandes de fonctionnalités.

## Documentation

| Document                                                            | Commencez ici pour                                                                                   |
| ------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| [Documentation produit](https://docs.orchestrator.inc)                     | Installation, configuration des agents et utilisation quotidienne du produit.                        |
| [docs/architecture.md](../docs/architecture.md)                     | Modèle mental du backend, cycle de vie, persistance, CDC, dérivation de l'état et limites du daemon. |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | Responsabilité des paquets et emplacement de chaque domaine du backend.                              |
| [docs/cli/README.md](../docs/cli/README.md)                         | Comportement de la CLI et correspondance avec les routes du daemon.                                  |
| [docs/development.md](../docs/development.md)                       | Prérequis, compilation, tests et dépannage pour le développement local.                              |
| [docs/STATUS.md](../docs/STATUS.md)                                 | Ce qui est actuellement livré sur `main` et ce qui reste en cours.                                   |

## Communauté

Rejoignez [Discord](https://discord.com/invite/UZv7JjxbwG) pour obtenir de l'aide et échanger avec les contributeurs, suivez [@useOrchestrator](https://x.com/useOrchestrator) pour les nouveautés ou lancez une discussion dans les [GitHub Issues](https://github.com/OrchestratorInc/agent-orchestrator/issues).

## Télémétrie produit

Orchestrator.inc collecte des métriques d'usage et de fiabilité sans contenu des projets. Le propriétaire GitHub d'un projet et le nom d'utilisateur GitHub connecté peuvent toutefois identifier une personne; la télémétrie n'est donc pas anonyme. La désactiver arrête aussi l'envoi de ces identifiants. [Détails et réglages](../docs/telemetry.md).

## Licence

Orchestrator.inc est disponible sous [licence Apache 2.0](../LICENSE).
