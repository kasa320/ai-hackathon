# 普段の空き時間・AI下書き・開催回例外の実装（feat/availability-drafts）

決定日：2026-09-22。ブランチ：`feat/availability-drafts`。担当境界と要件は
`.agent/kasa/prompts/2026-09-22-availability-drafts.md`（[並列開発runbook](../workflows/parallel-worktree-development.md)、
[確定仕様](discord-availability-home-refresh.md)）に従う。

## 実装した内容

- `POST /api/me/weekly-availability/interpretations` をフロント（`/weekly.html`）から呼ぶ「文章から下書きを作る」を追加。
  結果は曜日フォームへ反映するだけで、保存は既存の保存ボタンでのみ行う。原文は保存しない。
  このエンドポイントのバックエンドはDiscord担当の実装で、URL・入出力の形はここでは変更していない。
- セッション参加条件に、登録済みの普段の空き時間の表示（`GET /api/me/weekly-availability`）と
  「普段の空き時間を編集」リンクを追加。今回だけの例外（今回だけ参加できない日＝`unavailable_dates`、
  今回だけ参加できる日時＝`date_windows`）を別見出しで示す。
- 参加条件の保存に `update_weekly_availability`（既定 `false`）を追加（`apitypes.Preparation`）。
  `true` は本人がこの回で週間の曜日・時間帯を明示的に入力・変更したときだけフロントが送る
  （`availability.js` の `weeklyTouched()`：初期値の読み込みや自由文下書きの流し込みだけでは立たない。
  自由文下書きの結果を本人がその後review・送信した場合は「明示的な入力」として扱う）。
  保存は参加条件と同じDBトランザクションで普段の空き時間を全置換する。有効な週間枠がなければ422。
  直接PUTとtaskの`decision=submit`は共通の`submitPreparation`を通るため同じ結果になる。
- 用途固有のWeeklyWindows抽出は`coord.WeeklyAvailabilityExtractor`という新しい任意インターフェースに分離し、
  `playbook/reading/weekly_update.go`で実装。セッションの曜日番号（0=日〜6=土）から週間契約の番号
  （1=月〜7=日）への変換もここで行う。`internal/playbook/reading/rules.go`の担当承認要件は変更していない。
- 既存不具合の修正：`schedule`が省略、または`status=unknown`を明示的に答えた場合でも、参加を明示していれば
  登録済みの普段の空き時間を候補計算に使うようにした（`internal/playbook/reading/availability.go`）。
  修正前は、明示的な`unknown`のときだけ普段の空き時間を無視して候補なし扱いにしていた
  （省略時は既に正しく動いていた）。未回答は引き続き参加可能扱いにしない。
- 参加条件の自由文placeholderを「参加します。普段は水曜と金曜の19時〜22時が空いています。10月7日は参加できません。」に変更。

## Discord担当との統合点・契約差分

- 契約差分なし。`POST /api/me/weekly-availability/interpretations` はフロントから呼ぶだけで、URL・入出力の形は
  `discord-availability-home-refresh.md` の記載どおり。
- `apitypes.Preparation` に `update_weekly_availability`（省略可・既定false）を追加した。Discord側が
  参加条件を保存する経路（`SaveDialogPreparation` / `putPreparation`）を使う場合、この項目を省略すれば
  今まで通り普段の空き時間は変わらない。Discord側の会話フローで週間予定の明示変更に相当する入力を
  扱うようになったときは、この項目をtrueにすれば同じ全置換処理が使える（新規実装は不要）。
- `internal/discord/**`・`internal/notify/**`・通知actionスキーマ、`index.js`・`book.js`は変更していない。

## 未完了・既知の限界

- 週間予定の明示変更を示す確認画面は、参加条件フォーム内の常設の注記（「この内容で保存すると、普段の空き時間も
  全置換されます」）として実装した。別ダイアログでの最終確認ステップは追加していない。
- ホームでの普段の空き時間の再確認導線（`RECONFIRM_AFTER_DAYS`）は既存のまま。ブック登録直後のDM依頼などは
  「整合性・ホーム担当」の範囲。
- 参加条件フォームのUIは大きめの変更になったため、実ブラウザでの見た目確認は未実施（開発サーバー起動なし）。
  DOM構造とCSSクラスは既存のもの（`.notice`・`.draft`・`.form-details`・`.availability__row`等）を再利用しており、
  新規CSSは追加していない。
