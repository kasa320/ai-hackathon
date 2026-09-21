# ログイン前トップ画面（抜き出し）

`feat/frontend-login-screen`（`6d2d499 feat: add the pre-login top screen`）の `src/frontend/` から、`index.html` の表示に必要なファイルだけを**そのまま**取り出したもの。内容は未編集で、元ブランチと同一。

採用したい部分を切り出しておく置き場であり、ここを直接編集して本実装にはしない。

## 入っているもの

| ファイル | 役割 |
| --- | --- |
| `index.html` | ログイン前のトップ。組み直しの実例を帯グラフで見せ、Discordログインへ誘導する |
| `css/base.css` | 色・余白・文字などの土台 |
| `css/site.css` | トップ画面の見た目（`lede`・`bar`・`sec` など） |
| `img/meeting-1600.jpg` `img/meeting-900.jpg` | 見出しの背景写真（Unsplash License、装飾なので `alt` は空） |
| `js/index.js` | トップの制御。未ログインなら静的な内容のまま、ログイン後はグループ一覧へ切り替える |
| `js/client.js` | 本物のAPIとモックを同じ呼び出し方で差し替える（`?mock=&as=`） |
| `js/api.js` | API呼び出し。CSRF・Idempotency-Key の付与、`ApiError` |
| `js/mock.js` | モックデータ |
| `js/dom.js` | 小さな描画ヘルパー |
| `js/components/chrome.js` | ヘッダー・デモバー・接続表示 |

## 入れなかったもの

元ブランチにあるが `index.html` から到達しないため外した。

- `css/style.css` — `index.html` から読み込まれていない
- `js/components/health.js`, `js/features/index.js`, `js/features/reading/index.js` — コメントだけの雛形

## 見るとき

`<script type="module">` を使うので `file://` では動かない。

```sh
python3 -m http.server -d .agent/ui-login-screen 8000
# http://localhost:8000/
```

APIには繋がらないので、ログイン状態の確認に失敗して未ログインの表示のままになる。バックエンドと一緒に見るなら `FRONTEND_DIR` をこのディレクトリに向ける。

## 引き継ぐときの注意

- `index.html` は `session.html` と `meeting.html` へのリンクを持つが、どちらもこの抜き出しには無い。
- `js/api.js` の冒頭コメントが `.agent/decisions/api.md` を参照している。このファイルは [docs/api-endpoint.md](../../docs/api-endpoint.md) と [docs/data-structure.md](../../docs/data-structure.md) に分割済みなので、本実装へ移すときに直す。
- 画面の語彙が「輪読調整」で、[`../ui-prototype/`](../ui-prototype/) の「Marunage」とは別のデザイン体系になっている。どちらに寄せるかを決めてから統合する。
