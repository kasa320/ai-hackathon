# 旅行エージェント競合調査

- 確認日: 2026-09-19
- 対象: 旅行プラン生成、予約管理、旅行中の再計画、現地ガイド

## 結論

「希望を聞いて旅程を作る」「地図上に並べる」「現地のおすすめを案内する」は非常に競合が多い。旅行前の計画と旅行中のガイドを一体化したサービスもすでに存在するため、この組み合わせだけでは新規性にならない。

差別化するなら、旅行中に計画が崩れた瞬間、複数人の利害調整、特定利用者の制約、根拠の検証、予約変更などの実行まで踏み込む必要がある。

## 主要競合

| 競合 | 計画 | 予約・整理 | 旅行中 | グループ | ガイド |
|---|---|---|---|---|---|
| Mindtrip | 個人化旅程、移動時間を含む時系列 | 航空、宿泊、飲食、体験、確認書・領収書 | 地図、リアルタイム推薦、現地探索 | 共同編集、グループチャット、嗜好調整 | 現地推薦・イベント |
| Expedia Romie | メールや嗜好から旅程構築 | Expedia予約との接続 | 天候・直前トラブルを監視し、代替案。旅程をリアルタイム更新 | グループチャット | ホテル周辺の飲食・活動提案 |
| Google Gemini / Maps | Maps、Flights、Hotels、Gmail等から詳細旅程 | Gmail監視、予約情報整理、予約フォーム補助 | Mapsナビ中の会話、現在地を使った案内 | 限定的 | 徒歩・自転車中のガイド、周辺質問 |
| Layla | 日付、予算、嗜好から日別旅程 | 航空・ホテル等の検索・予約、人間専門家との併用 | 旅程変更 | 限定的 | 推薦 |
| Booking.com AI Trip Planner | 宿泊を中心とした会話型旅行計画 | Booking.com商品との接続 | 旅行支援 | 限定的 | 推薦 |
| NAVITIME Travel AI | 観光スポット、経路、所要時間を考慮した国内旅程 | 飛行機・宿泊予約 | 経路案内 | 旅程共有 | スポット情報 |
| GuideGeek | 個人化旅程 | メッセージ上で支援 | WhatsApp等でリアルタイム旅行助言 | 会話共有 | ローカル情報・多言語案内 |
| VoiceMap | 固定ルート | ツアー購入 | GPS連動で音声を自動再生、オフライン対応 | 同一ツアー共有 | 現地制作者による高品質音声ガイド |
| Waiz / Sorom等 | AI旅程、移動時間検証 | 予約メール取り込み | 天気、現地ガイド、旅程変更 | 変更の同期 | 現地情報 |

## 競合から分かること

### 新規性にならない要素

- 希望、予算、日程から旅程を自動生成
- 移動時間を考慮してスポットを並べる
- 地図、写真、レビューを一画面にまとめる
- 予約メールを読み、旅程表にする
- 現在地周辺のおすすめを答える
- GPSで音声ガイドを再生する
- 雨天時に代替スポットを提案する
- グループで旅程を共有・編集する

### まだ差別化しやすい領域

1. **グループ内の公平な利害調整**
   - 各人の必須・避けたい・疲労・予算を個別に受け取り、全員の納得度を最適化する
   - 誰か一人の声の大きさで予定が決まる問題を解く
2. **制約を守る旅程修復**
   - 固定予約、最終交通、歩行可能距離、食事制限などを保護したまま最小変更で再計画する
3. **現地状態の継続取得**
   - 実際の位置、遅れ、天候、混雑、疲労を受けて、旅程との差を検出する
4. **提案後の実行**
   - 同意後に予約変更、共有旅程更新、参加者通知、チケット整理まで進める
5. **検証済み現地ガイド**
   - 観光公式サイト、施設資料、現地専門家の情報だけを根拠にし、出典と鮮度を表示する
6. **特定利用者・旅行目的への垂直化**
   - 子連れ、車椅子、高齢者、食物制限、推し活遠征、修学旅行等

## 新規性の暫定評価

| 企画 | 新規性 | コメント |
|---|---:|---|
| 汎用AI旅行プランナー | 1/5 | 激戦。LLMラッパーに見えやすい |
| プラン＋現地AIガイド | 1.5/5 | Mindtrip、Google、GuideGeek等と重なる |
| 旅行中の自動再計画 | 2.5/5 | Expedia Romie等が存在。実行・制約処理で差が必要 |
| グループ旅行コンダクター | 3.5/5 | 共同編集は既存だが、公平性とライブ再計画に余地 |
| 特定制約向け旅行エージェント | 4/5 | 課題が明確。ただしデータの正確性が必要 |
| 検証済み物語ガイド＋旅程修復 | 3.5/5 | ガイド品質と適応を一体化できれば差別化可能 |

## 参照先

- Mindtrip: https://mindtrip.ai/
- Mindtrip FAQ: https://resources.mindtrip.ai/travelers/help/traveler-faqs
- Expedia Romie: https://www.expedia.com/newsroom/spring-product-release-2024/
- Google Gemini travel planning: https://blog.google/products-and-platforms/products/gemini/how-gemini-plans-trips/
- Gemini in Google Maps: https://support.google.com/maps/answer/6041199
- Google Maps walking and cycling guide: https://blog.google/products-and-platforms/products/maps/gemini-navigation-biking-walking/
- Layla: https://layla.ai/
- Booking Holdings 2026 investor presentation: https://s201.q4cdn.com/865305287/files/doc_presentations/2026/Investor-Presentation-February-2026.pdf
- NAVITIME Travel AI: https://corporate.navitime.co.jp/topics/pr/202403/29_5725.html
- GuideGeek: https://guidegeek.com/about
- VoiceMap: https://voicemap.me/about
- Waiz: https://getwaiz.com/ja
- Sorom: https://www.sorom.co/ja/

