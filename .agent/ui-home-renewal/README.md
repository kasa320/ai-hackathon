# ホーム画面（ログイン前）の作り直し

`compare.html` は見た目と文言を選ぶための比較用。使い捨てで、ここを本実装にはしない。
決めた案を `src/frontend/index.html`・`src/frontend/css/styles.css` に入れる。

## 見るとき

```sh
python3 -m http.server -d .agent/ui-home-renewal 8000
# http://localhost:8000/compare.html
```

## 3案の違い

| 案 | 見た目 | 向いている点 |
| --- | --- | --- |
| A | 写真を全面に敷き、左下を濃い幕で沈める | いちばん強い。写真の出来に結果が左右される |
| B | 左に文言・右に写真。チャットが予定に変わる図を添える | 何をしてくれるのかが一目で伝わる |
| C | ブランド色のグラデーションが主役、写真は奥に薄く | 写真が未定でも成立する。テーマの広さを前に出せる |

## 決まっていないこと

- 写真（`img/meeting-*.jpg` は `.agent/ui-login-screen/` からの仮置き。Unsplash License）
- 見出しの文言。3案でそれぞれ変えてあるので、案と文言は別々に選べる
