<div align="center">

### Orchestrator.inc

#### Coding-Agenten an einem Ort planen, ausführen und überwachen.

[![GitHub stars](https://img.shields.io/github/stars/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/stargazers)
![Top 6k repositories](https://img.shields.io/badge/Top%206k%20repositories-181717?style=flat&logo=github&logoColor=white)
[![GitHub release](https://img.shields.io/github/v/release/OrchestratorInc/agent-orchestrator?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/OrchestratorInc/agent-orchestrator/total?style=flat&logo=github)](https://github.com/OrchestratorInc/agent-orchestrator/releases)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat)](../LICENSE)
[![X](https://img.shields.io/badge/@useOrchestrator-555?style=flat&logo=x&logoColor=white)](https://x.com/useOrchestrator)
[![Discord](https://img.shields.io/badge/Discord-555?style=flat&logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)

Orchestrator.inc (früher Agent Orchestrator, AO; früher github.com/ComposioHQ/agent-orchestrator) ist ein Open-Source-Orchestrator (Apache-2.0) für Claude Code, Codex und 35+ Coding-Agenten. Er führt Agenten parallel aus, jeden in einem eigenen isolierten Git-Worktree und Branch. Ein Projekt-Orchestrator-Agent plant größere Vorhaben und startet fokussierte Worker-Agenten. Ein autonomer Pull-Request-Zyklus leitet fehlgeschlagene CI-Läufe und Review-Kommentare an den Agenten zurück, dem die Änderung gehört. AO gibt es als Desktop-App für macOS, Windows und Linux sowie als Begleit-Apps für iPhone, iPad und Android, mit denen du Agenten vom Smartphone aus verfolgen und steuern kannst.

Gib jeder Coding-Aufgabe einen eigenen Agenten, Workspace und Feedback-Zyklus.<br />
Plane und delegiere größere Vorhaben mit einem projektkundigen Orchestrator.<br />
Verfolge jeden Worker, Pull Request, CI-Lauf und jedes Review in einem Live-Kanban.

[**Orchestrator.inc herunterladen**](#installation) &nbsp;&bull;&nbsp; [Dokumentation](https://docs.orchestrator.inc) &nbsp;&bull;&nbsp; [Releases](https://github.com/OrchestratorInc/agent-orchestrator/releases) &nbsp;&bull;&nbsp; [Mitwirken](../CONTRIBUTING.md) &nbsp;&bull;&nbsp; [Discord](https://discord.com/invite/UZv7JjxbwG)

[English](../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Español](README.es.md) · [Français](README.fr.md) · **Deutsch** · [Português (Brasil)](README.pt-BR.md)

<br />

<img src="../docs/assets/readme/hero.png" alt="Orchestrator.inc-Kanban mit Worker-Sessions, gruppiert nach ihrem Live-Status" width="100%" />
</div>

## Ein Workspace für agentengestützte Entwicklung

Ein Coding-Agent kann eine Aufgabe erledigen. Sobald mehrere Agenten parallel an einem Projekt arbeiten, entsteht eine andere Herausforderung: entscheiden, was wichtig ist, die Arbeit sauber aufteilen, jedem Agenten den richtigen Kontext geben, Branch-Konflikte vermeiden und jede Änderung bis zu Review und Merge begleiten.

Orchestrator.inc ist ein lokaler Desktop-Workspace für genau diese Aufgabe. Füge ein Repository hinzu und erstelle eine Worker-Session mit dem Coding-Agenten, dem Modell und der Oberfläche, die am besten zur Aufgabe passen. Bei Git-basierter Arbeit erhält der Worker einen eigenen Branch und Worktree. Aufgabe, Konversation, Terminal, geänderte Dateien, Browser-Vorschau, Pull Request, CI- und Review-Status bleiben von Anfang bis Ende mit dieser Session verbunden.

Hinter der Desktop-App beobachtet der lokale Daemon von Orchestrator.inc die Aktivität der Agenten und den Zustand der Versionsverwaltung. So entsteht eine gemeinsame Live-Ansicht des Projekts statt einer Sammlung unverbundener Terminals, Branches und Browser-Tabs.

<img src="../docs/assets/readme/tui.png" alt="Orchestrator.inc workspace with a supervised native agent interface" width="100%" />

## Worker erledigen klar abgegrenzte Aufgaben

Ein Worker ist die Ausführungseinheit von Orchestrator.inc: eine Aufgabe, ein Coding-Agent und ein isolierter Workspace. Nutze **New task**, wenn die Aufgabe bereits klar ist. Beschreibe das gewünschte Ergebnis, wähle Agent und Modell, hänge relevante Dateien an und arbeite mit dem Agenten im strukturierten Chat oder in seiner nativen Terminal-Oberfläche.

Du kannst einen Worker jederzeit öffnen, um das Gespräch fortzusetzen, sein Terminal aufzurufen, Änderungen zu prüfen, den isolierten Browser zu verwenden, den Pull Request zu begutachten oder CI- und Review-Feedback an denselben Agenten zurückzugeben. Dadurch bleibt jede Aufgabe für sich verständlich und parallele Arbeit landet nicht in einem gemeinsamen Kontext.

<img src="../docs/assets/readme/new-task.png" alt="Creating a focused worker task in Orchestrator.inc" width="100%" />

## Der Orchestrator plant über das gesamte Projekt hinweg

Der Projekt-Orchestrator ist der dauerhafte Planungs- und Koordinationsagent von Orchestrator.inc. Er arbeitet eine Ebene über einzelnen Aufgaben und behält Produktentwicklung, technische Strategie, Prioritäten und die Reihenfolge der Arbeit im gesamten Repository im Blick.

Nutze den Orchestrator, um eine Idee vor der Umsetzung zu erkunden, Produkt- und Technikansätze zu entwickeln, Zielkonflikte abzuwägen, besonders wirkungsvolle Aufgaben zu erkennen und aus einem unklaren Vorhaben einen konkreten Plan zu machen. Seine projektbezogene Konversation bewahrt Ziele, Entscheidungen, Einschränkungen und frühere Überlegungen. Diese Planungshistorie verbindet er mit dem Repository-Kontext und dem aktuellen Zustand von Orchestrator.inc, darunter aktive Worker, Zuständigkeiten, Pull Requests, CI und Reviews. Damit bleibt die Planung sowohl im Projekt als auch in der bereits laufenden Arbeit verankert.

Sobald ein Plan umsetzbar ist, kann der Orchestrator ihn in klar abgegrenzte Aufgaben zerlegen, Worker starten oder neu ausrichten, jedem Worker den relevanten Kontext mitgeben, den Fortschritt verfolgen und Folgearbeiten koordinieren. Der Orchestrator verantwortet Planung und Delegation. Worker verantworten Umsetzung, Tests, Commits und Pull Requests.

<img src="../docs/assets/readme/orchestrator.png" alt="Orchestrator.inc coordinating multiple workers with project context" width="100%" />

## Das Kanban macht das System übersichtlich

Jeder Worker erscheint auf demselben Live-Board, unabhängig davon, ob du ihn über **New task** gestartet oder über den Orchestrator delegiert hast. Orchestrator.inc leitet die Position jeder Karte aus Fakten zu Session, Pull Request, CI und Review ab. So wird das Kanban zur operativen Projektansicht:

- **Working:** Worker, die aktiv umsetzen oder für eine weitere Anweisung bereit sind
- **Needs you:** blockierte Sessions, fehlende Eingaben, fehlgeschlagene CI, angeforderte Änderungen oder verlorene Signale
- **In review:** offene Pull Requests und Entwürfe, die auf Checks oder Reviews warten
- **Ready to merge:** genehmigte oder mergefähige Arbeit; zusammengeführte Sessions bleiben sichtbar, bis sie archiviert werden

Jede Karte hält Aufgabe, Agent, Branch, Aktivität, Pull Request und Status zusammen. Öffne sie, um Konversation oder Terminal, geänderte Dateien, PR-Zusammenfassung, Reviews und Vorschau zu prüfen. Das Board zeigt, was vorankommt, was blockiert ist und wo deine Aufmerksamkeit die größte Wirkung hat.

<img src="../docs/assets/readme/hero.png" alt="Orchestrator.inc Kanban showing workers grouped by live status" width="100%" />

## Ein Workflow von der Idee bis zum Merge

1. **Starte auf der richtigen Ebene.** Gib eine klare Aufgabe direkt an einen Worker oder entwickle ein größeres Vorhaben mit dem Projekt-Orchestrator und lass ihn den Plan ausarbeiten.
2. **Delegiere fokussierte Arbeit.** Starte Worker selbst oder lass den Orchestrator sie mit dem nötigen Kontext und klarer Zuständigkeit erstellen.
3. **Arbeite isoliert.** Jeder Git-basierte Worker erhält einen eigenen Branch und Worktree. Scratch-Worker erhalten von Orchestrator.inc verwaltete Verzeichnisse ohne Branch.
4. **Überwache den Live-Status.** Orchestrator.inc verfolgt Agentenaktivität, Pull Requests, CI, Review-Feedback und Merge-Konflikte und bildet diese Fakten im Kanban ab.
5. **Schließe den Feedback-Zyklus.** Prüfe jeden Worker direkt, triff projektweite Entscheidungen mit dem Orchestrator und gib umsetzbare Fehler oder Review-Kommentare an den zuständigen Agenten zurück.

Orchestrator.inc arbeitet mit den Coding-Agenten und dem Versionsverwaltungs-Workflow, die du bereits nutzt. Agenten behalten ihre jeweiligen Stärken. Orchestrator.inc liefert Projektkontext, isolierte Ausführung, Koordination und die operative Übersicht, die aus ihnen ein System machen.

## Produkthighlights

- **Ein Orchestrator-Agent, der plant und delegiert.** Der Orchestrator jedes Projekts macht aus einem Ziel einen Plan, startet Worker mit gezieltem Kontext und verfolgt ihre Pull Requests. Öffne ihn über die Seitenleiste oder mit ⌘⇧O unter macOS bzw. Ctrl+Shift+G unter Windows und Linux.
- **Ein autonomer Pull-Request-Zyklus.** Fehlgeschlagene CI-Läufe, Review-Kommentare und Merge-Konflikte gehen an den Worker zurück, dem die Änderung gehört.
- **Integriertes AO-Review.** Ein kritischer Reviewer-Agent auf einem beliebigen Review-fähigen Harness prüft den Pull Request eines Workers und hinterlässt für jede nötige Änderung einen Inline-Kommentar. Diese Kommentare gehen an den Worker zurück.
- **37 Coding-Agenten, pro Projekt wählbar.** Wähle unterschiedliche Agenten für Worker, Orchestrator und Reviewer. Nutze strukturierten Chat oder die native Terminal-Oberfläche des Agenten und wechsle zwischen beiden, wo der Harness das unterstützt.
- **Ein Chat, der mithält.** Mit Tab übernimmst du eine vorgeschlagene nächste Nachricht. Im Developer mode bieten Side Chats im Inspector eine Vorschau auf native Konversations-Forks, wo der Anbieter sie unterstützt.
- **Artefakte.** Agenten können statt eines Pull Requests Markdown-Berichte, SVGs, Bilder und HTML-Seiten liefern. Sie öffnen sich gerendert in einem bearbeitbaren Vollbild-Viewer mit Versionsverlauf und Wiederherstellung, und die neuesten erscheinen auf den Kanban-Karten.
- **Automatisierungen.** Plane wiederkehrende Prompts und verwende Projektanweisungen und Shell-Befehle als Cues wieder.
- **Desktop und Mobil.** Desktop-Apps für macOS, Windows und Linux sowie Apps für iPhone, iPad und Android, die mehrere Rechner verwalten, Sessions starten und Push-Benachrichtigungen senden. Unter macOS hält Stay Awake den Rechner erreichbar, auf unterstützten Modellen sogar mit zugeklapptem MacBook.
- **Konten und Einrichtung.** Installiere und aktualisiere Agenten-CLIs in den Settings, nutze mehrere Codex-Abos und teste ordnerbasierte Konten für Claude Code und Codex als Vorschau im Developer mode. Wähle aus 20 hellen und dunklen Farbthemen.

<table>
  <tr>
    <td width="36%" valign="middle">
      <h3>Pull Requests und Agenten-Reviews</h3>
      <p>Halte CI, Mergefähigkeit, Reviewer-Status und interaktive Agenten-Reviews beim Worker und gib angeforderte Änderungen an denselben Verantwortlichen zurück.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/review.png" alt="Worker-Session mit Pull Request, CI und Agenten-Review-Status in Orchestrator.inc" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Von Agenten steuerbarer Browser</h3>
      <p>Zeige die lokale App eines Workers neben seiner Oberfläche an und prüfe sie dort. Browser-Profile sind pro Worker isoliert, damit parallele UI-Aufgaben keinen Status teilen.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/browser.png" alt="Ein Worker steuert seine isolierte Browser-Vorschau in der App" width="100%" />
    </td>
  </tr>
  <tr>
    <td width="36%" valign="middle">
      <h3>Native Oberflächen, eine Aufsicht</h3>
      <p>Nutze strukturierten Chat oder die native Terminal-Oberfläche des Agenten, während Orchestrator.inc Aufgaben, Workspace-Status und Feedback an einem Ort zusammenhält.</p>
    </td>
    <td width="64%">
      <img src="../docs/assets/readme/tui.png" alt="Agent terminal interface supervised inside Orchestrator.inc" width="100%" />
    </td>
  </tr>
</table>

<a id="mobile-and-cross-device"></a>

## Mobil und geräteübergreifend

Orchestrator.inc gibt es als Begleit-App für iPhone, iPad und Android:

- [Im App Store laden](https://apps.apple.com/app/ao-mobile/id6792552173) (iPhone und iPad)
- [Bei Google Play herunterladen](https://play.google.com/store/apps/details?id=aoagents.dev) (Android)

Das Smartphone ist eine Fernbedienung für die Desktop-App. Deine Repositories, Worktrees und Coding-Agenten laufen weiter auf deinem eigenen Rechner, und das Smartphone verbindet sich mit diesem Rechner statt mit einer gehosteten Kopie deines Codes. Zum Koppeln öffnest du in der Desktop-App **Settings → Connect Mobile**, wählst **Generate** und scannst den QR-Code mit der mobilen App. Wenn Scannen nicht möglich ist, gibst du Adresse, Port und Verbindungspasswort von Hand ein.

So erreicht das Smartphone deinen Rechner:

- **Gleiches Netzwerk (LAN):** funktioniert ohne weitere Einrichtung in einem vertrauenswürdigen WLAN zu Hause oder im Büro. Diese Verbindung ist unverschlüsseltes HTTP, geschützt durch das Verbindungspasswort; nutze sie daher nicht in nicht vertrauenswürdigen Netzwerken.
- **Tailscale:** Ist der Rechner in einem Tailnet, enthält der QR-Code zusätzlich seine Tailscale-Adresse für Smartphones im selben Tailnet. iPhone und iPad können sie nicht nutzen, weil iOS unverschlüsselte Verbindungen zu Tailscale-Adressen blockiert. Die Android-App erlaubt diese Verbindung, sie wurde aber noch nicht auf einem echten Android-Gerät geprüft. Für einen getesteten Weg von unterwegs nutze die HTTPS-Option unten.
- **Verwalteter HTTPS-Tunnel:** Connect Mobile führt `cloudflared` aus (in der macOS-App enthalten, unter Windows und Linux mit einem Klick installierbar) und stellt so eine HTTPS-Adresse bereit, die du von unterwegs mit jedem Gerät nutzen kannst. Auf diesem Weg terminiert Cloudflare TLS.

Das Smartphone probiert jede Adresse im QR-Code aus und nutzt die erste, die antwortet. Die Haupt-API der Desktop-App verlässt nie `127.0.0.1`. Connect Mobile ist ein separater Listener, der standardmäßig aus ist, das Verbindungspasswort verlangt und sich jederzeit abschalten oder mit einem neuen Passwort versehen lässt.

Vom Smartphone aus kannst du jedes Projekt und jeden Worker öffnen, strukturierten Chat oder die Terminal-Oberfläche des Agenten nutzen, neue Sessions starten, Freigaben und Rückfragen beantworten, Agenten und Modelle wechseln, per Sprache diktieren, Dateien anhängen, Pull Requests, Reviews und Reviewer-Läufe verfolgen und Push-Benachrichtigungen erhalten.

Auch eine andere Orchestrator.inc-Desktop-App kann sich als Remote-Host mit diesem Rechner verbinden. Führe auf diesem Rechner `ao remote-host` aus. Auf dem anderen Rechner aktivierst du **Settings → General → Developer mode** und nutzt **Settings → Remote hosts → Add host**. Unter [Remote access and mobile](https://docs.orchestrator.inc/configuration/remote-access) findest du eine Schritt-für-Schritt-Anleitung, das Sicherheitsmodell und Hilfe bei Problemen.

**Orchestrator Cloud ist jetzt in der Desktop-App verfügbar** (aktiviere **Settings → General → Developer mode** und dann **Cloud**). Wähle **Sign in to AO Cloud** und führe Agenten in der Cloud aus, in gehosteten Sandboxes statt auf deinem Rechner. Alle können sich anmelden und sofort loslegen. Cloud-Sessions laufen in entfernten Sandboxes, die langsam starten oder nicht committete Arbeit verlieren können; committe und pushe daher regelmäßig. Siehe den [Cloud-Leitfaden](https://docs.orchestrator.inc/guides/cloud) und die [Tarife](#plans).

<a id="supported-agents"></a>

## Unterstützte Agenten

Coding-Agenten arbeiten in einem gemeinsamen Workflow. Die aktuellen Agenten und Modi stehen im [Funktionskatalog](https://docs.orchestrator.inc/plugins/agents).

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

[Einrichtungsanleitungen für Agenten ansehen →](https://docs.orchestrator.inc/plugins/agents)

**Nutze die Oberfläche, die gerade passt: strukturierten Chat oder die native Terminal-Oberfläche des Agenten.**

## Installation

Lade die neueste Orchestrator.inc-Desktop-App für deine Plattform herunter. Orchestrator.inc sucht automatisch nach Updates.

| Plattform             | Download                                                                                                                           |
| --------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| macOS (Apple silicon) | [Herunterladen](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.dmg)   |
| macOS (Intel)         | [Herunterladen](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.dmg)     |
| Windows               | [Herunterladen](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [Herunterladen](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [Herunterladen](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [Herunterladen](https://github.com/OrchestratorInc/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

Öffne Orchestrator.inc und wähle das Repository aus, das Orchestrator.inc verwalten soll. Die Desktop-App führt den Daemon für dich aus, eine CLI ist daher nicht erforderlich. Im [Installationsleitfaden](https://docs.orchestrator.inc/installation) findest du Hinweise zur Einrichtung der Agenten-CLI und zur Fehlerbehebung.

<a id="plans"></a>

## Tarife

Die lokale Nutzung ist kostenlos: unbegrenzt lokale Agenten mit jedem unterstützten Harness sowie Orchestrator, Worker und PR-Reviews. Für lokale Agenten gelten keine Tariflimits. Kostenpflichtige Tarife bringen AO-Cloud-Agenten; ein Cloud-Relay für geräteübergreifende Verbindungen folgt in Kürze.

| Tarif | Preis | Enthalten |
| --- | --- | --- |
| **Free** | $0 | Unbegrenzt lokale Agenten auf deinem Rechner. Orchestrator, Worker und PR-Reviews. Verbindung im selben Netzwerk oder von überall über dein eigenes Tailscale oder den verwalteten HTTPS-Tunnel. Keine Cloud-Agenten. |
| **Starter** | $20/Monat | Alles aus Free, dazu Cloud-Agenten. Cloud-Agenten arbeiten weiter, während dein Rechner schläft, mit Pull Requests, CI-Korrekturen und Reviews in der Cloud. 1 Cloud-Projekt und 1 gleichzeitiger Cloud-Agent. Demnächst: ein Cloud-Relay für den Zugriff über Geräte hinweg. |
| **Pro** | $40/Monat | Alles aus Starter. Eigenes Coder-Deployment nutzbar. 5 Cloud-Projekte und 15 gleichzeitige Cloud-Agenten. |
| **Enterprise** | Individuell | Unbegrenzt Cloud-Projekte und gleichzeitige Cloud-Agenten, keine Obergrenze für Session-Stunden, gemeinsame Arbeit an Cloud-Agenten, SSO und Teamverwaltung sowie bevorzugter Support mit SLA. |

Abgerechnet wird monatlich; siehe [orchestrator.inc/pricing](https://orchestrator.inc/pricing). Einen Tarif wählst oder änderst du in der Desktop-App unter **Settings → Upgrade**, nachdem du dich bei AO Cloud angemeldet hast. Für Enterprise nutzt du **Talk to founder** auf derselben Seite oder trägst dich in die [Teams-&-Enterprise-Warteliste](https://orchestrator.inc/waitlist) ein.

## FAQ

### Was ist Orchestrator.inc (Agent Orchestrator, AO)?

Orchestrator.inc, früher Agent Orchestrator (AO), ist eine Open-Source-Desktop-App mit CLI. Sie führt Claude Code, Codex und andere Coding-Agenten parallel aus und überwacht sie, jeden in einem eigenen Git-Worktree. Außerdem verfolgt sie deren Pull Requests, CI und Reviews.

### Unter welcher Lizenz steht es?

Apache-2.0. Siehe [LICENSE](../LICENSE).

### Welche Coding-Agenten werden unterstützt?

37 Coding-Agent-Harnesses (35+), darunter Claude Code, Codex, Cursor, Gemini CLI, GitHub Copilot, opencode, Aider, Amp, Droid, Goose, Cline und Kimi. Siehe die Tabelle [Unterstützte Agenten](#supported-agents) oben. Wie gut ein Agent als Worker, im Chat und als Reviewer unterstützt wird, ist unterschiedlich.

### Auf welchen Plattformen läuft es?

Die Desktop-App läuft unter macOS (Apple silicon und Intel), Windows und Linux (AppImage, `.deb` und `.rpm`).

### Gibt es eine mobile App?

Ja. Orchestrator.inc gibt es im [App Store](https://apps.apple.com/app/ao-mobile/id6792552173) für iPhone und iPad und bei [Google Play](https://play.google.com/store/apps/details?id=aoagents.dev) für Android. Die App koppelt sich mit deiner Desktop-App über dein lokales Netzwerk oder einen verwalteten HTTPS-Tunnel. Tailscale-Adressen werden ebenfalls angeboten, iPhone und iPad können sie aber nicht nutzen, und unter Android wurde das noch nicht auf einem echten Gerät geprüft. Siehe [Mobil und geräteübergreifend](#mobile-and-cross-device).

### Ist es kostenlos?

Ja. Die lokale App ist kostenlos: Desktop-App, CLI und mobile Apps kosten nichts, und der Quellcode ist unter Apache-2.0 offen. Du nutzt deine eigenen Abos oder API-Schlüssel für die Coding-Agenten. Kostenpflichtige AO-Cloud-Tarife beginnen bei $20/Monat (Starter); Pro kostet $40/Monat, Enterprise wird individuell bepreist. Siehe [Tarife](#plans). Teams und Unternehmen können sich in die [Teams-&-Enterprise-Warteliste](https://orchestrator.inc/waitlist) eintragen.

### Ist das dasselbe Projekt wie `ComposioHQ/agent-orchestrator`?

Ja. Das Projekt lag früher unter `github.com/ComposioHQ/agent-orchestrator` und ist jetzt unter [github.com/OrchestratorInc/agent-orchestrator](https://github.com/OrchestratorInc/agent-orchestrator) zu finden. Es steht unter der Apache-2.0-Lizenz.

## Einen Bug melden

[Melde einen Bug](https://github.com/OrchestratorInc/agent-orchestrator/issues/new?template=bug_report.yml) über dein eigenes GitHub-Konto. Ein paar Sätze in deinen eigenen Worten darüber, was du getan hast und was schiefgelaufen ist, reichen aus. Falls vorhanden, ergänze deine Erwartung, Schritte zur Reproduktion, Orchestrator.inc-Version und Betriebssystem sowie einen Screenshot. Diese Angaben helfen, sind aber keine Voraussetzung.

Ein lokaler Coding-Agent kann dir mit dem [Bug-Triage-Skill](https://github.com/OrchestratorInc/agent-orchestrator/blob/main/.agents/skills/bug-triage/SKILL.md) helfen, den Bericht zu präzisieren und Belege zu sammeln. Beschränke den Inhalt der Issue auf deine Beobachtungen. Vom Agenten gesammelte Logs, Datenbankauszüge und Untersuchungsnotizen gehören in klar gekennzeichnete Anhänge. Prüfe jeden Entwurf, bevor du ihn über dein eigenes Konto einreichst. Bitte lass keinen Bot Issues in deinem Namen einreichen, damit die Zuordnung zum Melder erhalten bleibt.

Wenn du Hilfe beim Beschreiben eines Problems brauchst, nutze den [Bug-Triage-Kanal auf Discord](https://discord.com/channels/1476302178913357958/1491735678156013588). Weitere Hinweise findest du in den [Beitragsrichtlinien](../CONTRIBUTING.md#bugs-and-features).

## Entwickeln und mitwirken

Beiträge zu Code, Dokumentation, Triage, Beispielen und Tests sind willkommen.

```bash
git clone https://github.com/OrchestratorInc/agent-orchestrator.git
cd agent-orchestrator
```

Der [Entwicklungsleitfaden](../docs/development.md) erklärt Voraussetzungen, lokale Einrichtung und Testbefehle. Lies [CONTRIBUTING.md](../CONTRIBUTING.md), bevor du einen Pull Request öffnest, und nutze [GitHub Issues](https://github.com/OrchestratorInc/agent-orchestrator/issues) für Bugs und Funktionswünsche.

## Dokumentation

| Dokument                                                            | Hier findest du                                                                            |
| ------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| [Produktdokumentation](https://docs.orchestrator.inc)                      | Installation, Einrichtung von Agenten und tägliche Produktnutzung.                         |
| [docs/architecture.md](../docs/architecture.md)                     | Backend-Mentalmodell, Lifecycle, Persistenz, CDC, Statusableitung und Daemon-Grenzen.      |
| [docs/backend-code-structure.md](../docs/backend-code-structure.md) | Paketverantwortung und Zuordnung der einzelnen Backend-Bereiche.                           |
| [docs/cli/README.md](../docs/cli/README.md)                         | CLI-Verhalten und Zuordnung der Daemon-Routen.                                             |
| [docs/development.md](../docs/development.md)                       | Voraussetzungen, Build-Schritte, Testausführung und Fehlerbehebung für lokale Entwicklung. |
| [docs/STATUS.md](../docs/STATUS.md)                                 | Was aktuell auf `main` ausgeliefert wird und woran noch gearbeitet wird.                   |

## Community

Komm auf unseren [Discord](https://discord.com/invite/UZv7JjxbwG), wenn du Hilfe suchst oder dich mit anderen Mitwirkenden austauschen möchtest. Folge [@useOrchestrator](https://x.com/useOrchestrator) für Neuigkeiten oder starte eine Diskussion in den [GitHub Issues](https://github.com/OrchestratorInc/agent-orchestrator/issues).

## Produkttelemetrie

Orchestrator.inc erfasst Nutzungs- und Zuverlässigkeitsdaten ohne Projektinhalte. Der GitHub-Projektinhaber und der angemeldete GitHub-Nutzername können jedoch Personen identifizieren; die Telemetrie ist daher nicht anonym. Beim Abschalten werden auch diese Angaben nicht mehr gesendet. [Details und Einstellungen](../docs/telemetry.md).

## Lizenz

Orchestrator.inc steht unter der [Apache License 2.0](../LICENSE) zur Verfügung.
