# 既存の計画・秘書エージェント調査

- 確認日: 2026-09-19
- 目的: `secretary-bot` をハッカソン作品にする場合の競合と差別化を確認する

## 結論

タスクの自動配置、カレンダーの最適化、予定変更時の再計画を行う既存製品は複数あり、この機能だけでは新規性が弱い。一方で、主要製品の公開機能を見る限り、次を一体化した企画には差別化の余地がある。

- カレンダーとタスクリスト以外の原資料や長期目標も読む
- 未完了の理由を分類し、単に翌日へ積み上げない
- 過去の計画誤差から、本人の容量や配分規則を更新する
- 固定予定、移動、食事、学習順序など、本人固有の制約を継続的に守る
- 計画だけでなく、Google Calendar／Tasksなどの正本へ安全に反映する
- 判断理由と変更履歴を説明可能な形で残す

したがって、「AIタスク管理」ではなく「計画が崩れた理由を学び、次の計画を現実に近づける適応型Personal Ops Agent」として位置づける。

## 主要な既存製品

| 製品 | 公開されている主な機能 | 今回の案との重なり | 差別化候補 |
|---|---|---|---|
| Motion | 期限、優先度、所要時間、依存関係からタスクを時間割化し、変更時に日・週を再計算。文書からタスク生成も行う | 非常に大きい | 未完了理由の明示的な原因学習、生活・学習の原資料との接続 |
| Reclaim 2.0 | Focus、Habit、Buffer、Meeting等のエージェントが継続的にカレンダーを最適化。変更を適用前に確認できるPreview Mode | 大きい | タスクの発見・生成、複数分野の目標理解、失敗からの規則更新 |
| Trevor AI | 所要時間予測、日・週の自動計画、期限超過の再配置提案、進捗メール、利用ごとの適応 | 非常に大きい | 原因別キャリブレーション、長期目標と学習記録からの次タスク生成、説明可能性 |
| SkedPal | 目標、優先度、好み、制約からTo-doを適応的な時間割へ変換 | 大きい | 会話・資料・実績を横断するコンテキストと実行後学習 |
| Todoist | タスク管理、自然言語の日付入力、カレンダー同期、AIによるタスク分解 | 中程度 | 自律的な全体再計画と外部状態への働きかけ |
| ChatGPT Scheduled tasks | 定期・単発・監視・イベント起点のバックグラウンド実行と対応アプリ操作 | 実行基盤として重なる | 個人計画に特化した状態、制約、評価、再計画ループ |
| Gemini Scheduled Actions | 定期実行、Calendar・To-do・メール等の日次ダイジェスト | 朝のブリーフィングと重なる | ダイジェスト後の計画変更、原因学習、正本更新 |

## 参照先

- Motion AI Calendar: https://www.usemotion.com/features/ai-calendar
- Motion: https://www.usemotion.com/
- Reclaim 2.0 overview: https://help.reclaim.ai/en/articles/14846468-reclaim-ai-2-0-overview
- Reclaim Habits: https://help.reclaim.ai/en/articles/4129152-habits-overview-auto-schedule-flexible-time-for-your-routines
- Trevor AI: https://www.trevorai.com/
- SkedPal: https://www.skedpal.com/
- Todoist Calendar integration: https://www.todoist.com/help/todoist/integrations/use-the-calendar-integration-rCqwLCt3G
- ChatGPT scheduled tasks: https://help.openai.com/en/articles/10291617-chatgpt-tasks
- Gemini Scheduled Actions: https://support.google.com/gemini/answer/16316416

## 調査上の注意

- 上記は各社の公式サイト・公式ヘルプに掲載された機能を比較したもの。
- 製品内部のアルゴリズムや、公開されていない機能までは確認できない。
- 「原因を学習する競合が存在しない」とは断定しない。公開上の主訴として明確に打ち出している主要製品が見当たらない、という範囲の判断である。

## 既存ローカル実装から確認した事実

個人データの内容は企画資料へ転記せず、仕組みだけを記録する。

- `/home/kasa/src/personal/secretary-bot/briefing.sh` が毎朝のCodex実行を起動する
- systemd user timerで毎朝03:00 JSTに実行される
- Google Calendar、Google Tasks、Vault内の指定資料を参照する
- 期限超過タスクがある場合、固定予定を含む複数日をゼロから再配置する
- 再配置後に予定を再取得し、重複を検査する
- 変更できない場合は重複作成せず「未反映」として扱う
- `planning_calibration.md` に観測、原因、調整、学習を残す
- 原因を、見積もり不足、エージェントの配分過多、外的要因、完了記録漏れ、本人の速度、不明に分ける
- 2026-08-29から2026-09-18までに、通常名の日次ファイルが16件存在する

これは単なるcron付き要約ではなく、状態取得、計画、外部操作、検証、記憶更新を含むエージェントのプロトタイプと評価できる。

