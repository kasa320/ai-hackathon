# メンバーの脱退：行は消さず、開始前の開催回からは「欠席」として外れる

決定日：2026-09-20。関連：[スキーマ変更の方式](schema-migrations.md)、[認証とDiscord連携の方式](auth-and-discord-linking.md)。

## 背景

所属の追加・削除はMVP外としていた。実際の利用事例（固定の幹事がいない輪読会）では、人の出入りが起きても不思議ではなく、「抜けたい人が抜けられない」とデモで説明しにくい。

一方、`members` の行を物理削除する実装は取れない。`session_members`・`preparations`・`tasks` の3つから外部キーで参照されており、削除は外部キー制約で弾かれる。無理に子から消すと、**過去の開催回で誰が担当したかの履歴が消える**。

スキーマ変更の仕組みが入ったので、列を足す選択肢が取れるようになった。

## 決めたこと

`members.left_at` による論理削除にする（マイグレーション `0002_add_member_left_at.sql`）。

1. **行は消さない。** `left_at` に日時を入れるだけ。参照は壊れず、過去の回の顔ぶれと担当が残る。
2. **脱退できるのは本人だけ。** `POST /api/groups/{group_id}/leave`。他人を外すAPIは作らない。
3. **管理者（owner）は脱退できない**（`409 invalid_state`）。
4. **在籍者が2人を下回る脱退は断る**（`MinGroupSize`）。
5. **開始前**の開催回では、本人を `attendance=absent` にして辞退と同じ経路（`onInputChanged`）へ流す。未回答の依頼は `obsolete` にする。
6. **開始済み**の開催回には触らない。

### 既存の仕組みに乗せた理由

引き受け・投票の対象者は `Snapshot.Attending()`（＝`attendance=attending` の人）から決まる。脱退者を `absent` にするだけで、**用途側（`reading`）を1行も変えずに**分母から外れ、担当候補からも外れる。`normalizeRequirements` の「分母をあとから縮めない」性質もそのまま効く。

専用の「脱退者リスト」を作って各所で除外する実装も検討したが、除外を書き漏らした箇所が静かにバグになる。既存の1つの概念（参加予定かどうか）に寄せるほうが漏れにくい。

### 見え方の切り分け

| 取得先 | 脱退者を含むか |
| --- | --- |
| `tx.Members` / `GET /api/groups/{id}` | **含まない**（在籍者のみ） |
| `tx.MemberByUser` | **含まない**（脱退後は所属なし扱い → 404） |
| `tx.Member`（ID指定） | 含む（履歴の表示に必要） |
| `tx.SessionMembers` / `SessionDetail.members` | **含む**（固定時の顔ぶれを保つ。`left: true` で見分ける） |

`ActivateMemberships`（ログインのたびに走る所属の有効化）にも `left_at IS NULL` を足した。これがないと、**脱退した人がログインし直すだけで復帰してしまう**。

## 却下した選択肢

### 物理削除（子テーブルから順に消す）

外部キーの順序を守れば消せるが、`preparations`・`tasks` を消すと過去の回の記録が失われる。さらに `proposals.data` の `presenter_member_id` や `notifications.mentions` はJSON内の文字列で外部キーがないため、消えたIDが残って画面に「不明なメンバー」が出る。履歴を正とする方針と両立しない。

### 管理者の脱退時に、残りメンバーの先頭へ自動で管理者を引き継ぐ

オーナーも抜けられるようになるが、**本人の同意なしに管理者にされる**。「重要な操作には人の承認を」という方針と衝突する。管理者交代APIを作るのが筋だが今回の範囲外とし、現状は拒否する。

### 管理者による除名（`DELETE /api/groups/{id}/members/{member_id}`）

連絡が取れない人を外す用途はあるが、MVPのデモフローに必要ない。権限の設計（誰がいつ外せるか、外された人への通知）を詰める必要があり、範囲を広げるより自主脱退を先に出す。

### 開始済みの開催回からも外す

履歴が変わってしまう。「第3回はDさんが担当した」という事実は、Dが後で抜けても変わらない。

### 脱退時に `session_members` から行を消す

開始前の回だけなら消せるが、`preparations`・`tasks` の複合外部キーが先に効くので、結局それらも消すことになる。`absent` を入れるだけで同じ効果が得られ、しかも「一度は対象だった」という記録が残る。

## 実装の範囲

- `internal/store/migrations/0002_add_member_left_at.sql`
- `internal/store/groups.go`：`Member.LeftAt` / `Left()`、`Members`・`MemberByUser`・`GroupsForUser` を在籍者に限定、`LeaveGroup`
- `internal/store/users.go`：`ActivateMemberships` を在籍中の所属に限定
- `internal/store/sessions.go`：`SessionMembers` は脱退者も返す
- `internal/coord/leave.go`：`LeaveGroup`、開始前の開催回の処理
- `internal/api/api.go`：`POST /api/groups/{group_id}/leave`
- `apitypes`：`LeaveGroupInput` / `LeaveGroupResult`、`Member.left`
- フロント：`api.js` の `leaveGroup`、`mock.js`（画面は未実装）
- ドキュメント：`docs/api-endpoint.md`、`docs/data-structure.md`

## 残る確認事項

- **画面（UI）は未実装。** APIクライアントとモックまで。脱退ボタンをどこに置くか（グループ画面か設定か）、確認ダイアログの文言、脱退後の遷移先はフロント担当と決める。
- 管理者交代APIを作るかどうか。作らない限り、管理者は永久に脱退できない。
- 脱退を他メンバーへ通知するか。現状は開催回の実行履歴（activity）に残るだけで、Discord通知は出していない。
