# Notion Agentと知識ベース予定管理

- 確認日: 2026-09-19
- 目的: Notionが特定の知識集を参照し、タスク・予定を自律管理できるかを確認する

## 結論

現在のNotionは、かなりの範囲で実現できる。特にCustom Agentsを使うと、指定したNotionページ・データベースを知識源として、定期・イベント起点で動き、Calendarや外部MCPへ読み書きできる。

したがって「特定の知識ベースを読むAIが予定を管理する」だけでは、ハッカソン上の新規性は弱い。

## Notion Agent

通常のNotion Agentは次が可能。

- 現在のページ、指定ページ、データベース、接続アプリをコンテキストとして使う
- ワークスペース内を検索し、複数の情報源を突き合わせる
- ページ・データベースを作成・編集する
- タスクと担当者を含む計画を生成する
- Calendarを接続し、会議の候補提示・作成、予定確認、タスクのタイムブロック、優先度変更時の予定組み直しを会話から行う
- instructionsページで振る舞い、覚えてほしい情報、優先参照先を指定する
- MCPサーバー経由で外部アプリを読み書きする

制限として、リマインダー作成、本人が主催者でない予定の編集・取消、モバイルからの予定作成・取消などには制約がある。

## Custom Agents

Custom Agentsは通常のNotion Agentより今回の案に近い。

- 指定したNotionページ・データベースをコンテキストにする
- 毎日、毎週、毎月等の定期スケジュールでバックグラウンド実行する
- ページ・データベースの追加やプロパティ変更をトリガーにする
- Calendarの予定作成・更新・取消をトリガーにする
- Notion、Slack、Calendar、MCP経由の外部システムへアクションを行う
- アクティビティログで実行理由、操作、エラーを確認する
- 読み書き権限と、書き込み前に確認を求めるかを設定する
- 他のCustom Agentへ処理の一部を引き渡す

Calendar接続では、Google Calendar、Apple Calendar、Microsoft Outlook / M365、Notion Calendarを扱い、予定の読取・作成・更新・取消、招待、参加者の空き時間探索、RSVPができる。Custom AgentsのCalendar接続はBusiness・Enterpriseプラン向け。

MCP接続もBusiness・Enterpriseプラン向け。事前設定済みサービスのほか、公開URLを持つカスタムMCPサーバーを接続できる。読み書きツール単位で自動実行または確認必須を設定できる。

## タスクとカレンダー

- NotionのTask databaseは、少なくとも状態、担当者、期限を持つ
- 日付プロパティを持つNotionデータベースはNotion Calendarに表示できる
- Notion Calendar上でタスクを移動すると、元のデータベースの日付へ反映される
- Notion Agentは会話からタスクを時間枠へ配置できる
- Custom Agentは定期実行やCalendarイベント起点で予定を操作できる

## 想定できる実装例

```text
Notion pages
  - 長期目標
  - 授業・プロジェクト資料
  - 個人ルール
  - 見積もり・振り返り

Notion task database
  - 期限
  - 所要時間
  - 優先度
  - 固定／移動可能
  - 状態

Calendar
  - 固定予定
  - タスク時間枠

Custom Agent
  - 毎朝実行
  - 知識とタスクを読む
  - 今日の計画を作成
  - Calendarへ反映
  - ブリーフィングを生成
```

この範囲なら、現在のNotionだけでもかなり近いものを構築できる。

## Kasaの秘書との差

| 観点 | Notion Custom Agents | Kasaの現行秘書 |
|---|---|---|
| 知識源 | Notion中心、接続アプリ、MCP | ローカルVault、Google Tasks、Calendar、任意ファイル |
| 定期実行 | 標準機能 | systemd＋Codex |
| Calendar操作 | 標準Calendar接続 | Google Calendar MCP |
| タスク | Notion task database中心 | Google Tasksを正本として利用 |
| 個人ルール | Agent instructions、ページ | AGENTS.md、operating_rules.md、workflows.md |
| 実行ログ | Activity log | daily、active_work、systemd journal |
| 失敗からの学習 | 指示とDB設計で構築可能 | 原因分類とplanning_calibrationを明示実装 |
| 配置アルゴリズム | 製品内部で不透明 | 独自ルール。今後は制約ソルバー化可能 |
| ベンダーロックイン | Notion中心 | ファイル・ツールを差し替え可能 |

## ハッカソン上の示唆

次の訴求は弱い。

- 自分のノートを読んで予定を作る
- 毎朝ブリーフィングを出す
- NotionやCalendarを更新する
- タスクを時間割へ入れる

差別化候補は次。

1. **計画失敗の原因学習**: 未完了を本人の遅さと決めつけず、外的要因、配分過多、見積もり不足等に分類する
2. **説明可能な計画修復**: 何を守り、なぜ何を動かしたかを示す
3. **制約ソルバー**: LLM生成ではなく、締切、移動、食事、容量を決定論的に検証する
4. **知識基盤非依存**: Notion、Obsidian、Google Drive、LMS等を同じ正本モデルで扱う
5. **ローカルファースト**: 個人情報をローカルに置き、外部へ渡す内容を制御する
6. **計画品質の評価**: 過密時間、衝突数、所要時間誤差、持ち越し率を継続測定する

## 参照先

- Notion Agent: https://www.notion.com/help/notion-agent
- Custom Agents: https://www.notion.com/help/custom-agents
- Calendar connection for Custom Agents: https://www.notion.com/help/connect-calendar-to-custom-agents
- MCP connections for Custom Agents: https://www.notion.com/help/mcp-connections-for-custom-agents
- Notion tasks and sprints: https://www.notion.com/help/sprints
- Notion Calendar database connection: https://www.notion.com/help/use-notion-calendar-with-notion
- Custom Agent best practices: https://www.notion.com/help/best-practices-for-creating-and-optimizing-a-custom-agent

